package ui

import (
	"os"
	"strings"
)

// IconSet picks which glyphs the UI uses for vote indicators. Different
// codepoints render at very different optical sizes depending on what
// font the terminal falls back to:
//
//   - U+2665 ♥ (Misc Symbols, "BLACK HEART SUIT") — narrow, monoline
//   - U+2661 ♡ (Misc Symbols, "WHITE HEART SUIT") — almost always pulled
//     from a CJK fallback font; renders heavier and slightly wider
//   - U+2298 ⊘ (Math Operators) — yet another fallback, often narrow with
//     a different stroke weight
//
// Mixing those three in the same line caused the heart-on / heart-off /
// dislike glyphs to visibly jitter as the user voted. Picking glyphs
// from Nerd Fonts (the de facto 2026 standard) at well-known PUA
// codepoints eliminates the jitter.
//
// Selection order:
//
//  1. ADDIPLAY_ICONS=nerd|nerd-v3|unicode|ascii (explicit override)
//  2. Default: Nerd Font trio verified rendering on host 2026-06-29
type IconSet struct {
	// Name is a short label used in --debug / config dumps.
	Name string
	// HeartFilled is the "liked" indicator.
	HeartFilled string
	// HeartOutline is the "neutral / not voted yet" indicator.
	HeartOutline string
	// HeartBroken is the "disliked" indicator.
	HeartBroken string
}

// nerdIcons is the host-verified trio (the user confirmed all three
// codepoints render at matching sizes in their terminal font on
// 2026-06-29):
//
//   - U+F02D6 nf-md-heart      — Material Design heart filled (liked)
//   - U+EB05  nf-cod-heart     — Codicons heart                (neutral)
//   - U+F0759 nf-md-heart_off  — Material Design heart-off     (disliked)
//
// The trio crosses two glyph families (Codicons + Material Design),
// but liked/disliked are both pure MDI so they sit at identical
// metrics. Codicons heart matches MDI's stroke weight close enough
// to disappear at typical terminal sizes.
//
// Font support notes:
//   - U+EB05 — Nerd Fonts v3.2 (2024) added Codicons at U+EB00-U+EC00
//   - U+F02D6 / U+F0759 — Nerd Fonts v3 (2023) parked MDI at U+F0000+
//
// Pre-3.2 Nerd Fonts won't render Codicons; pre-3 Nerd Fonts won't
// render MDI. Either group should set ADDIPLAY_ICONS=unicode for the
// safe fallback.
var nerdIcons = IconSet{
	Name:         "nerd",
	HeartFilled:  "",    //  nf-fa-heart       — liked
	HeartOutline: "",         //  nf-cod-heart      — neutral
	HeartBroken:  "\U000F0759", // 󰝙 nf-md-heart_off  — disliked
}

// nerdV3Icons is the pure-MDI set in Nerd Fonts v3 — three glyphs from
// a single coherent designer. Opt in via ADDIPLAY_ICONS=nerd-v3.
var nerdV3Icons = IconSet{
	Name:         "nerd-v3",
	HeartFilled:  "\U000F02D1", // 󰋑 nf-md-heart
	HeartOutline: "\U000F02D5", // 󰋕 nf-md-heart_outline
	HeartBroken:  "\U000F0759", // 󰝙 nf-md-heart_off
}

// unicodeIcons is the pre-existing set kept as the fallback for users
// without Nerd Fonts. Slightly inconsistent metrics across terminals
// but always renders something.
var unicodeIcons = IconSet{
	Name:         "unicode",
	HeartFilled:  "♥",
	HeartOutline: "♡",
	HeartBroken:  "⊘",
}

// asciiIcons is the worst-case fallback for headless / CI / non-UTF
// terminals. Width-stable by construction.
var asciiIcons = IconSet{
	Name:         "ascii",
	HeartFilled:  "[L]",
	HeartOutline: "[ ]",
	HeartBroken:  "[X]",
}

// activeIconSet is resolved once at startup. Reading env on every render
// would be wasteful; the value can't meaningfully change mid-session
// anyway.
var activeIconSet = resolveIconSet()

// Icons returns the active icon set. Tests can override via SetIconSet.
func Icons() IconSet { return activeIconSet }

// SetIconSet replaces the active set. Intended for tests and for a
// hypothetical runtime config toggle; production code reads via Icons().
func SetIconSet(s IconSet) { activeIconSet = s }

func resolveIconSet() IconSet {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ADDIPLAY_ICONS"))) {
	case "nerd":
		return nerdIcons
	case "nerd-v3", "nerdv3", "mdi":
		return nerdV3Icons
	case "unicode":
		return unicodeIcons
	case "ascii":
		return asciiIcons
	}
	// Default: host-verified Nerd Fonts trio. Users on plain
	// non-NF fonts can opt out with ADDIPLAY_ICONS=unicode.
	return nerdIcons
}
