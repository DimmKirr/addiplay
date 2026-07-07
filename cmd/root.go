package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dimmkirr/addiplay/internal/cache"
)

// Version is the binary version, injected from main via ldflags.
var Version = "dev"

// Persistent flags (apply to every invocation).
var (
	cfgFile    string
	verbose    bool
	debug      bool
	debugLog   string
	forceASCII bool
)

// Action flags. Mutually exclusive — pick one or none (default = TUI).
// Subcommands were collapsed into these flags so the CLI surface is
// `addiplay [--action] [--global flags...]` instead of nested verbs.
var (
	actionDemo       bool
	actionDoctor     bool
	actionLogout     bool
	actionWhoami     bool
	actionClearCache bool
	actionPlay       string // "<network>/<channel>" — non-empty means run headless play
)

var rootCmd = &cobra.Command{
	Use:   "addiplay",
	Short: "Terminal player for AudioAddict networks (DI.fm, RockRadio, JazzRadio, …)",
	Long: `addiplay is a small TUI music player that streams from the AudioAddict
network (DI.fm, RadioTunes, RockRadio, JazzRadio, ClassicalRadio, ZenRadio,
FrescaTune) over your own paid subscription.

Running ` + "`addiplay`" + ` with no flags launches the TUI; the login overlay
auto-pops on first run or when the saved listen_key gets rejected. Use the
action flags below for headless operation.`,
	SilenceUsage: true,
	RunE: func(c *cobra.Command, _ []string) error {
		// Action dispatch. MarkFlagsMutuallyExclusive guarantees at most
		// one action flag is set; the order here is just the order of
		// checks for readability.
		switch {
		case actionPlay != "":
			return runHeadlessPlay(c.Context(), actionPlay, c.OutOrStdout())
		case actionDemo:
			return runDemo(c.Context())
		case actionDoctor:
			return runDoctor(c.Context(), c.OutOrStdout())
		case actionLogout:
			return runLogout(c.OutOrStdout())
		case actionWhoami:
			return runWhoami(c.OutOrStdout())
		case actionClearCache:
			dir, err := cache.DefaultDir()
			if err != nil {
				return fmt.Errorf("resolve cache dir: %w", err)
			}
			return runClearCache(c.OutOrStdout(), dir)
		}
		return runTUI(c.Context())
	},
}

// Execute is the entry point called from main.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// `--version` is wired automatically by cobra when Version is set.
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	// Global flags.
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: $XDG_CONFIG_HOME/addiplay/config.yml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "write debug log (mpv verbose output + internal events) to --debug-log")
	rootCmd.PersistentFlags().StringVar(&debugLog, "debug-log", "./debug.log", "where to write the debug log when --debug is set")
	rootCmd.PersistentFlags().BoolVar(&forceASCII, "ascii", false, "force ASCII half-block fanart even on terminals that support Kitty graphics")

	// Action flags (former subcommands).
	rootCmd.Flags().BoolVar(&actionDemo, "demo", false, "launch TUI with mock data — no creds, no network, no mpv required")
	rootCmd.Flags().BoolVar(&actionDoctor, "doctor", false, "diagnose mpv / terminal / auth / network and exit")
	rootCmd.Flags().BoolVar(&actionLogout, "logout", false, "forget the saved AudioAddict credentials and exit")
	rootCmd.Flags().BoolVar(&actionWhoami, "whoami", false, "print the stored AudioAddict account and exit")
	rootCmd.Flags().StringVar(&actionPlay, "play", "", "headless play of <network>/<channel> (e.g. --play di/classicrock)")
	rootCmd.Flags().BoolVar(&actionClearCache, "clear-cache", false, "wipe the addiplay disk cache (channel JSON, thumbnails, track metadata) and exit")

	rootCmd.MarkFlagsMutuallyExclusive("demo", "doctor", "logout", "whoami", "play", "clear-cache")
}

// ApplyFanartFlags translates --ascii into the env-var the fanart
// package reads. Called once from runTUI / runHeadlessPlay / runDemo
// before constructing the model so DetectMode() returns the right value.
func ApplyFanartFlags() {
	if forceASCII {
		_ = os.Setenv("ADDIPLAY_FANART_MODE", "ascii")
	}
}

