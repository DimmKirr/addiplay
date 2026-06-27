// Package config persists user preferences and favorites under
// $XDG_CONFIG_HOME/addiplay/config.yml. Writes are atomic (.tmp + rename).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const currentSchemaVersion = 1

// Favorite is a (network, channel) tuple in the user's favorites list.
type Favorite struct {
	Network string `yaml:"network"`
	Channel string `yaml:"channel"`
}

// Config is the persisted preferences blob.
type Config struct {
	SchemaVersion int        `yaml:"schema_version"`
	LastNetwork   string     `yaml:"last_network,omitempty"`
	LastChannel   string     `yaml:"last_channel,omitempty"`
	Volume        int        `yaml:"volume"` // 0..100
	Favorites     []Favorite `yaml:"favorites,omitempty"`
}

// Default returns a Config seeded with reasonable defaults.
func Default() Config {
	return Config{
		SchemaVersion: currentSchemaVersion,
		LastNetwork:   "di",
		Volume:        65,
	}
}

// Load reads the config from disk; on first run returns Default() without an error.
func Load() (Config, error) {
	path, err := filePath()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	c = migrate(c)
	if c.Volume == 0 {
		c.Volume = 65
	}
	return c, nil
}

// Save atomically writes the config back to disk.
func (c Config) Save() error {
	c.SchemaVersion = currentSchemaVersion
	path, err := filePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "config-*.yml.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	enc := yaml.NewEncoder(tmp)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := enc.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// AddFavorite inserts (network, channel) idempotently.
func (c *Config) AddFavorite(network, channel string) {
	for _, f := range c.Favorites {
		if f.Network == network && f.Channel == channel {
			return
		}
	}
	c.Favorites = append(c.Favorites, Favorite{Network: network, Channel: channel})
}

// RemoveFavorite removes (network, channel) idempotently.
func (c *Config) RemoveFavorite(network, channel string) {
	out := c.Favorites[:0]
	for _, f := range c.Favorites {
		if f.Network == network && f.Channel == channel {
			continue
		}
		out = append(out, f)
	}
	c.Favorites = out
}

// IsFavorite reports whether (network, channel) is starred.
func (c Config) IsFavorite(network, channel string) bool {
	for _, f := range c.Favorites {
		if f.Network == network && f.Channel == channel {
			return true
		}
	}
	return false
}

// migrate brings older schema versions up to current. No-op for v1.
func migrate(c Config) Config {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = currentSchemaVersion
	}
	return c
}

func filePath() (string, error) {
	if override := os.Getenv("ADDICTUNED_CONFIG_DIR"); override != "" {
		return filepath.Join(override, "config.yml"), nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "addiplay", "config.yml"), nil
}
