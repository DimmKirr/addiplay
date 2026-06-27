package config_test

import (
	"testing"

	"github.com/dimmkirr/addiplay/internal/config"
)

func TestLoad_firstRunReturnsDefault(t *testing.T) {
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.LastNetwork != "di" || c.Volume != 65 {
		t.Errorf("default = %+v", c)
	}
}

func TestSaveLoad_roundtrip(t *testing.T) {
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())
	c := config.Config{LastNetwork: "rockradio", LastChannel: "80s_rock", Volume: 80}
	c.AddFavorite("di", "classicrock")
	c.AddFavorite("jazzradio", "bebop")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.LastChannel != "80s_rock" || got.Volume != 80 || len(got.Favorites) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestFavorites_idempotent(t *testing.T) {
	c := config.Default()
	c.AddFavorite("di", "classicrock")
	c.AddFavorite("di", "classicrock")
	if got := len(c.Favorites); got != 1 {
		t.Errorf("favorites = %d, want 1", got)
	}
	c.RemoveFavorite("di", "classicrock")
	c.RemoveFavorite("di", "classicrock") // remove twice = no-op
	if got := len(c.Favorites); got != 0 {
		t.Errorf("favorites after remove = %d, want 0", got)
	}
	if c.IsFavorite("di", "classicrock") {
		t.Error("IsFavorite should be false after remove")
	}
}

func TestSchemaVersionStamp(t *testing.T) {
	t.Setenv("ADDICTUNED_CONFIG_DIR", t.TempDir())
	c := config.Config{LastNetwork: "di"}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", got.SchemaVersion)
	}
}
