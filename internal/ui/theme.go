package ui

import "github.com/charmbracelet/lipgloss"

// Theme is the color palette for one AudioAddict network. Components read
// the active Theme from the root model — never hardcode colors.
//
// Field semantics:
//
//	Accent     — primary brand color: header badge, active tab underline,
//	             transport "play" block.
//	Secondary  — second brand color: selected channel row, network-picker
//	             cursor swatch. For single-color networks, equals Accent.
//	Pop        — accent highlight: favorite star, focus border. For
//	             single-color networks, equals Accent.
//	BG / BGAlt — terminal background and the dimmer status-bar background.
//
// **Color provenance:** values pulled from each network's published
// `application-*.css` bundle (the rails-style fingerprinted asset linked
// from the homepage), 2026-06-26, by extracting the top-frequency hex
// codes from the brand-color cluster. RGB values match the live sites at
// that date; if the brand refreshes, re-run the extraction (see
// docs/plans/theme-color-sourcing notes).
type Theme struct {
	Slug      string
	Display   string
	Accent    lipgloss.Color
	Secondary lipgloss.Color
	Pop       lipgloss.Color
	FG        lipgloss.Color
	FGMuted   lipgloss.Color
	BG        lipgloss.Color
	BGAlt     lipgloss.Color
	Success   lipgloss.Color
	Warn      lipgloss.Color
	Error     lipgloss.Color
}

// palettes — per-network brand colors sourced from each site's main CSS.
//
// Frequencies in the comments are the count of occurrences of that hex in
// the network's `application-*.css` (top-frequency hex codes; widget /
// third-party styles excluded).
var palettes = map[string]Theme{
	// DI.fm — 4-color brand: vivid royal blue dominant, dark navy
	// secondary, cyan-teal pop, deep blue-black background.
	// Source: di.fm/assets/application-*.css (#288cfb ×156, #343b51 ×30,
	// #09d3b7 ×29, #151b25 ×17, #212737 ×18).
	"di": {
		Slug:      "di",
		Display:   "DI.fm",
		Accent:    "#288cfb", // vivid royal blue
		Secondary: "#343b51", // dark blue-grey
		Pop:       "#09d3b7", // cyan-teal pop
		BG:        "#151b25", // near-black blue
		BGAlt:     "#212737", // panel bg
	},

	// RadioTunes — orange dominant, blue counter-accent, dark navy bg.
	// Source: radiotunes.com/assets/application-*.css (#eb8700 ×57,
	// #ffa224 ×12, #0e61a7 ×25, #0e2539 ×16).
	"radiotunes": {
		Slug:      "radiotunes",
		Display:   "RadioTunes",
		Accent:    "#eb8700", // vivid orange
		Secondary: "#0e61a7", // royal blue
		Pop:       "#ffa224", // light orange pop
		BG:        "#0e2539", // dark navy
		BGAlt:     "#1a3550",
	},

	// RockRadio — yellow + black brand, not red. Source:
	// rockradio.com/assets/application-*.css (#f9dc00 ×84, #ecc31f ×17,
	// #1c1c1c ×33, #2e2e2e ×29).
	"rockradio": {
		Slug:      "rockradio",
		Display:   "RockRadio",
		Accent:    "#f9dc00", // vivid yellow
		Secondary: "#ecc31f", // darker yellow
		Pop:       "#f9dc00",
		BG:        "#1c1c1c", // near-black
		BGAlt:     "#2e2e2e",
	},

	// JazzRadio — amber + burgundy brand. Source:
	// jazzradio.com/assets/application-*.css (#e8a548 ×80, #5e3437 ×25,
	// #401417 ×21, #feb95b ×14).
	"jazzradio": {
		Slug:      "jazzradio",
		Display:   "JazzRadio",
		Accent:    "#e8a548", // amber
		Secondary: "#5e3437", // deep burgundy
		Pop:       "#feb95b", // light amber pop
		BG:        "#401417", // burgundy near-black
		BGAlt:     "#4d272a",
	},

	// ClassicalRadio — mint-teal dominant with deep-teal bg and orange
	// accent pop. NOT gold, despite the genre's traditional aesthetic.
	// Source: classicalradio.com/assets/application-*.css (#3ac6a1 ×52,
	// #033043 ×19, #ff7e00 ×14).
	"classicalradio": {
		Slug:      "classicalradio",
		Display:   "ClassicalRadio",
		Accent:    "#3ac6a1", // mint teal
		Secondary: "#033043", // deep teal
		Pop:       "#ff7e00", // orange accent
		BG:        "#033043",
		BGAlt:     "#054a64",
	},

	// ZenRadio — teal dominant (not green), warm orange pop. Source:
	// zenradio.com/assets/application-*.css (#15afa7 ×135, #146870 ×19,
	// #ff9c25 ×13).
	"zenradio": {
		Slug:      "zenradio",
		Display:   "ZenRadio",
		Accent:    "#15afa7", // teal
		Secondary: "#146870", // dark teal
		Pop:       "#ff9c25", // orange pop
		BG:        "#0e3d42", // very dark teal
		BGAlt:     "#146870",
	},

	// FrescaTune — site unreachable (2026-06-26, DNS / no response). The
	// network is listed in AudioAddict's catalog but the consumer site
	// appears dead. Values below are educated approximations; replace when
	// the site comes back or when verifying against the mobile app.
	"frescatune": {
		Slug:      "frescatune",
		Display:   "FrescaTune",
		Accent:    "#ec8f3c", // warm orange (guess)
		Secondary: "#b06a2d", // dark orange (guess)
		Pop:       "#ec8f3c",
	},
}

// commonColors are shared across every theme; per-network palettes only
// override Accent / Secondary / Pop (and optionally BG / BGAlt).
var commonColors = Theme{
	FG:      "#e6e6e6",
	FGMuted: "#888888",
	BG:      "#0e0e10",
	BGAlt:   "#1a1a1c",
	Success: "#3fb46c",
	Warn:    "#e2a52c",
	Error:   "#e0303e",
}

// ThemeFor returns the Theme for the given network slug, defaulting to
// di.fm's palette if the slug is unknown.
func ThemeFor(slug string) Theme {
	p, ok := palettes[slug]
	if !ok {
		p = palettes["di"]
	}
	if p.BG == "" {
		p.BG = commonColors.BG
	}
	if p.BGAlt == "" {
		p.BGAlt = commonColors.BGAlt
	}
	p.FG = commonColors.FG
	p.FGMuted = commonColors.FGMuted
	p.Success = commonColors.Success
	p.Warn = commonColors.Warn
	p.Error = commonColors.Error
	return p
}

// styles bundles the lipgloss.Style values derived from a Theme. Computed
// once per render to avoid re-allocating in every component.
type styles struct {
	app         lipgloss.Style
	header      lipgloss.Style
	tabActive   lipgloss.Style
	tabInactive lipgloss.Style
	paneFocused lipgloss.Style
	paneBlurred lipgloss.Style
	nowPlaying  lipgloss.Style
	channelRow  lipgloss.Style
	channelSel  lipgloss.Style
	statusBar   lipgloss.Style
	keyHint     lipgloss.Style
	toast       lipgloss.Style
	accentBlock lipgloss.Style
	muted       lipgloss.Style
	star        lipgloss.Style
}

func newStyles(t Theme) styles {
	return styles{
		app:         lipgloss.NewStyle().Background(t.BG).Foreground(t.FG),
		header:      lipgloss.NewStyle().Foreground(t.FG).Padding(0, 1),
		tabActive:   lipgloss.NewStyle().Foreground(t.Accent).Bold(true).Underline(true).Padding(0, 1),
		tabInactive: lipgloss.NewStyle().Foreground(t.FGMuted).Padding(0, 1),
		paneFocused: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.Pop),
		paneBlurred: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.FGMuted),
		nowPlaying:  lipgloss.NewStyle().Padding(0, 1),
		channelRow:  lipgloss.NewStyle().Foreground(t.FG).Padding(0, 1),
		channelSel:  lipgloss.NewStyle().Foreground(t.BG).Background(t.Secondary).Bold(true).Padding(0, 1),
		statusBar:   lipgloss.NewStyle().Background(t.BGAlt).Foreground(t.FG).Padding(0, 1),
		keyHint:     lipgloss.NewStyle().Foreground(t.FGMuted),
		toast:       lipgloss.NewStyle().Background(t.Error).Foreground(t.FG).Padding(0, 1).Bold(true),
		accentBlock: lipgloss.NewStyle().Background(t.Accent).Foreground(t.BG).Padding(0, 1).Bold(true),
		muted:       lipgloss.NewStyle().Foreground(t.FGMuted),
		star:        lipgloss.NewStyle().Foreground(t.Pop),
	}
}
