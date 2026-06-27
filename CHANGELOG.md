# Changelog

All notable changes are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is
[SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added — v0.1
- AudioAddict API client covering all 7 networks (DI.fm, RadioTunes,
  RockRadio, JazzRadio, ClassicalRadio, ZenRadio, FrescaTune): authenticate,
  list channels, resolve stream URL, currently-playing track. Typed errors:
  `ErrAuth`, `ErrOAuthOnly`, `ErrUnauthorized`, `ErrNotFound`, `ErrRateLimit`.
- `addiplay login` / `logout` / `whoami` subcommands; credentials persist
  in the OS keyring with a `~/.config/addiplay/creds.json` fallback.
- `addiplay login --refresh` to re-authenticate when a `listen_key` is
  revoked.
- Headless `addiplay play <network>/<channel>` for scripting and smoke tests.
- mpv subprocess + JSON-IPC playback engine.
- Bubble Tea TUI with per-network theme swap (network brand colors), favorites
  tab, fuzzy channel filter, status bar with current track and volume.
- Persisted prefs (last network, last channel, volume, favorites) in
  `~/.config/addiplay/config.yml`.
- Test harness (`internal/testutil`): `teatest` wrappers, golden-file helper,
  AudioAddict httptest fake, mpv JSON-IPC fake socket, `SkipIfNoLiveCreds`
  gate for integration tests.
- Project scaffolding: cobra-based CLI, Taskfile, golangci-lint v2,
  goreleaser, pre-commit hooks, GitHub Actions CI (unit on every PR,
  integration nightly with secrets).
