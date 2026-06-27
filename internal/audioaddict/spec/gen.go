// Package spec contains Go types generated from AudioAddict's published
// OpenAPI (Swagger 2.0) specification at https://api.audioaddict.com/openapi.json.
//
// To refresh from upstream (network-required, manual):
//
//	go run ./internal/audioaddict/spec/specgen fetch
//
// To regenerate Go types from the vendored spec (no network):
//
//	go generate ./internal/audioaddict/spec/...
//
// AudioAddict's published spec is incomplete. It documents the modern
// routes (channels, channel_filters, shows, oauth) but omits the legacy
// ones we depend on most: members/authenticate, currently_playing,
// track_history. Types for those remain hand-written in
// internal/audioaddict/audioaddict.go.
package spec

//go:generate go run ./specgen normalize
//go:generate go tool oapi-codegen -config cfg.yaml openapi.v3.json
//go:generate gofmt -w types.gen.go
