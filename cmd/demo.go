package cmd

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dimmkirr/addiplay/internal/demo"
	"github.com/dimmkirr/addiplay/internal/ui"
)

// runDemo launches the TUI against the embedded fixture set — no creds,
// no network, no mpv required. Invoked by `addiplay --demo`.
//
// Useful for:
//   - Trying addiplay without an AudioAddict account
//   - Showcasing the per-network theme swap (press 'n')
//   - Screenshots / GIFs
//   - Smoke-testing the UI on a machine without mpv installed
//
// All seven networks are populated with sample channels; the fake player
// emits the same state events the real mpv-backed player would.
func runDemo(ctx context.Context) error {
	client := demo.NewClient()
	newPlayer := func(ctx context.Context) (ui.AudioPlayer, error) {
		return demo.NewPlayer(ctx)
	}
	p := tea.NewProgram(
		ui.NewModel(ctx, demo.Creds(), client, newPlayer),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}
