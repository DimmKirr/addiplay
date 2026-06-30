package ui_test

import (
	"strings"
	"testing"

	"github.com/dimmkirr/addiplay/internal/ui"
)

func TestThemeFor_knownNetwork(t *testing.T) {
	di := ui.ThemeFor("di")
	if di.Slug != "di" || di.Display != "DI.fm" {
		t.Errorf("di theme = %+v", di)
	}
	rr := ui.ThemeFor("rockradio")
	if rr.Accent == di.Accent {
		t.Errorf("rockradio and di share accent: %v", di.Accent)
	}
}

func TestThemeFor_unknownNetworkFallsBackToDi(t *testing.T) {
	got := ui.ThemeFor("nonsense")
	if got.Slug != "di" {
		t.Errorf("unknown slug fallback = %q, want di", got.Slug)
	}
}

func TestThemeFor_allDeclaredNetworksResolve(t *testing.T) {
	slugs := []string{"di", "radiotunes", "rockradio", "jazzradio", "classicalradio", "zenradio", "frescatune"}
	seen := map[string]bool{}
	for _, s := range slugs {
		th := ui.ThemeFor(s)
		if th.Accent == "" {
			t.Errorf("%s: empty accent", s)
		}
		key := strings.ToLower(string(th.Accent))
		if seen[key] {
			t.Errorf("%s shares accent %q with another network", s, th.Accent)
		}
		seen[key] = true
	}
}
