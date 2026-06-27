// Command specgen is the build-time tool that prepares AudioAddict's
// OpenAPI spec for oapi-codegen. It is invoked from
// internal/audioaddict/spec/gen.go via //go:generate, so `go generate`
// against the spec package re-runs the full pipeline.
//
// Two subcommands:
//
//	specgen fetch        — download upstream openapi.json, canonicalize, write
//	                       to ./openapi.json. Strips `example` fields whose
//	                       timestamp values regenerate on every fetch (would
//	                       otherwise create noise in `git diff`).
//
//	specgen normalize    — read ./openapi.json (Swagger 2.0), convert to
//	                       OpenAPI 3.x, patch upstream quirks (phantom Track
//	                       schema, `type: "text"`, type-as-string $refs,
//	                       Rails-style path params), write ./openapi.v3.json.
//
// The fetch step is intentionally NOT in the //go:generate chain — that
// chain stays hermetic (no network) and runs against the vendored
// openapi.json. To refresh from upstream:
//
//	go run ./internal/audioaddict/spec/specgen fetch
//	go generate ./internal/audioaddict/spec/...
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"
)

const (
	upstreamURL  = "https://api.audioaddict.com/openapi.json"
	userAgent    = "addiplay-specgen/1.0"
	vendorPath   = "openapi.json"
	convertedDst = "openapi.v3.json"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "fetch":
		mustRun(cmdFetch)
	case "normalize":
		mustRun(cmdNormalize)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: specgen fetch | normalize")
	os.Exit(2)
}

func mustRun(f func() error) {
	if err := f(); err != nil {
		fmt.Fprintln(os.Stderr, "specgen:", err)
		os.Exit(1)
	}
}

// ----------------------------------------------------------------------
// fetch
// ----------------------------------------------------------------------

func cmdFetch() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", upstreamURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: status %d", upstreamURL, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		return fmt.Errorf("parse upstream JSON: %w", err)
	}
	stripExamples(spec)

	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(vendorPath, append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("specgen: wrote %s (%d bytes)\n", vendorPath, len(out)+1)
	return nil
}

// stripExamples removes `example` / `examples` fields recursively. Upstream
// regenerates timestamp examples on every fetch; without this every refetch
// produces a noisy diff.
func stripExamples(node any) {
	switch n := node.(type) {
	case map[string]any:
		delete(n, "example")
		delete(n, "examples")
		for _, v := range n {
			stripExamples(v)
		}
	case []any:
		for _, v := range n {
			stripExamples(v)
		}
	}
}

// ----------------------------------------------------------------------
// normalize
// ----------------------------------------------------------------------

var railsPathParam = regexp.MustCompile(`:([A-Za-z_][A-Za-z_0-9]*)`)

func cmdNormalize() error {
	raw, err := os.ReadFile(vendorPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", vendorPath, err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fmt.Errorf("parse %s: %w", vendorPath, err)
	}

	stats := normalize(spec)

	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(convertedDst, append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("specgen normalize: wrote %s — %s\n", convertedDst, stats)
	return nil
}

type normalizeStats struct {
	pathsStripped       int
	typeToRef           int
	textToString        int
	missingTrackAdded   bool
	railsToOpenAPIPaths int
}

func (s normalizeStats) String() string {
	track := "track schema already defined"
	if s.missingTrackAdded {
		track = "added phantom Track schema"
	}
	return fmt.Sprintf("stripped %d paths, %d rails-style path params normalized (unused — kept for completeness), %s, %d type-as-string→$ref, %d text→string",
		s.pathsStripped, s.railsToOpenAPIPaths, track, s.typeToRef, s.textToString)
}

// normalize converts a Swagger-2.0 / OpenAPI-3 spec map in-place into a form
// that oapi-codegen can model-generate without choking on AudioAddict's
// upstream quirks.
func normalize(spec map[string]any) normalizeStats {
	var st normalizeStats

	// 1. Promote `swagger: "2.0"` → `openapi: "3.0.0"` and migrate definitions.
	if _, ok := spec["swagger"]; ok {
		delete(spec, "swagger")
		spec["openapi"] = "3.0.0"
	}
	if defs, ok := spec["definitions"].(map[string]any); ok {
		comps, _ := spec["components"].(map[string]any)
		if comps == nil {
			comps = map[string]any{}
			spec["components"] = comps
		}
		schemas, _ := comps["schemas"].(map[string]any)
		if schemas == nil {
			schemas = map[string]any{}
			comps["schemas"] = schemas
		}
		for k, v := range defs {
			schemas[k] = v
		}
		delete(spec, "definitions")
	}
	// Rewrite all $ref strings: #/definitions/X → #/components/schemas/X
	walk(spec, func(node map[string]any) {
		if ref, ok := node["$ref"].(string); ok {
			node["$ref"] = swap(ref, "#/definitions/", "#/components/schemas/")
		}
	})

	// 2. Strip paths — AudioAddict's spec has several path/param mismatches
	//    that block oapi-codegen validation. We only consume schemas.
	if paths, ok := spec["paths"].(map[string]any); ok {
		st.pathsStripped = len(paths)
		// Convert rails-style :name → {name} for diagnostics only; we'll
		// then wipe the map. Counts go in stats.
		for k := range paths {
			if railsPathParam.MatchString(k) {
				st.railsToOpenAPIPaths++
			}
		}
		spec["paths"] = map[string]any{}
	}

	comps, _ := spec["components"].(map[string]any)
	if comps == nil {
		comps = map[string]any{}
		spec["components"] = comps
	}
	schemas, _ := comps["schemas"].(map[string]any)
	if schemas == nil {
		schemas = map[string]any{}
		comps["schemas"] = schemas
	}

	// 3. Add the phantom Track schema (referenced by LightEpisodeList but
	//    not defined upstream). Shape derived from observed responses.
	if _, ok := schemas["Track"]; !ok {
		schemas["Track"] = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":         map[string]any{"type": "integer"},
				"track":      map[string]any{"type": "string", "description": "raw 'Artist - Title' string"},
				"artist":     map[string]any{"type": "string"},
				"title":      map[string]any{"type": "string"},
				"duration":   map[string]any{"type": "integer"},
				"art_url":    map[string]any{"type": "string"},
				"started_at": map[string]any{"type": "string", "format": "date-time"},
			},
		}
		st.missingTrackAdded = true
	}

	// 4. Patch non-standard primitive/reference types in every node.
	schemaNames := map[string]struct{}{}
	for k := range schemas {
		schemaNames[k] = struct{}{}
	}
	primitives := map[string]struct{}{
		"string": {}, "integer": {}, "number": {}, "boolean": {},
		"object": {}, "array": {}, "null": {},
	}
	walk(spec, func(node map[string]any) {
		t, ok := node["type"].(string)
		if !ok {
			return
		}
		if t == "text" {
			node["type"] = "string"
			st.textToString++
			return
		}
		if _, isPrim := primitives[t]; isPrim {
			return
		}
		if _, isSchema := schemaNames[t]; isSchema {
			delete(node, "type")
			delete(node, "items") // arrays of misuse: drop the items, $ref replaces it
			node["$ref"] = "#/components/schemas/" + t
			st.typeToRef++
		}
	})

	return st
}

// walk visits every map in node and calls fn on it.
func walk(node any, fn func(map[string]any)) {
	switch n := node.(type) {
	case map[string]any:
		fn(n)
		for _, v := range n {
			walk(v, fn)
		}
	case []any:
		for _, v := range n {
			walk(v, fn)
		}
	}
}

func swap(s, old, new string) string {
	if len(old) > len(s) || s[:len(old)] != old {
		return s
	}
	return new + s[len(old):]
}
