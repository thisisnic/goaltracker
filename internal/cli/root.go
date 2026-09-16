// Package cli defines the lifeo command tree.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thisisnic/lifeo/internal/config"
	"github.com/thisisnic/lifeo/internal/goal"
	"github.com/thisisnic/lifeo/internal/tui"
)

// DefaultDBPath is where the database lives unless overridden by --db or
// the LIFEO_DB environment variable: $XDG_DATA_HOME/lifeo/lifeo.db, falling
// back to ~/.local/share/lifeo/lifeo.db.
func DefaultDBPath() string {
	if p := os.Getenv("LIFEO_DB"); p != "" {
		return p
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "lifeo", "lifeo.db")
}

// New builds the root command.
func New() *cobra.Command {
	var dbPath, cfgPath string
	var private bool
	root := &cobra.Command{
		Use:   "lifeo",
		Short: "A personal tracker for goals, plans and time",
		Long: `lifeo is a local tracker for goals, plans and time.

Run it with no arguments to open the terminal UI. Subcommands give the same
data a scriptable interface; add --json to any list or show command for
machine-readable output.

Somewhere you'd rather not have people read over your shoulder, start with
--private (or set LIFEO_PRIVATE=1) to hide the why, amounts and notes.
Press x in the UI to toggle it.

Backups are encrypted snapshots written to a folder you choose. Run
lifeo key new once to set that up; with on_quit set in the config the
TUI writes one every time it exits.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The config only affects backups, so a broken one must not
			// keep the UI from opening. It is reported on exit instead.
			cfg, cfgErr := config.Load(cfgPath)
			store, err := goal.Open(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := tui.Run(cmd.Context(), store, tui.Options{Private: private}); err != nil {
				return err
			}
			if cfgErr != nil {
				return fmt.Errorf("no backup on quit: %w", cfgErr)
			}
			if cfg.Backup.OnQuit && cfg.Backup.Configured() {
				return runBackup(cmd, store, cfg.Backup)
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&dbPath, "db", DefaultDBPath(), "path to the SQLite database (env LIFEO_DB)")
	root.PersistentFlags().StringVar(&cfgPath, "config", config.Path(), "path to the config file")
	root.Flags().BoolVar(&private, "private", envFailClosed("LIFEO_PRIVATE"), "start with the why, amounts and notes hidden; x toggles (env LIFEO_PRIVATE=1)")
	root.AddCommand(goalCmd(&dbPath), keyCmd(), backupCmd(&dbPath, &cfgPath), restoreCmd(&dbPath, &cfgPath))
	return root
}

// Execute runs the root command and exits non-zero on error.
func Execute() {
	root := New()
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "lifeo:", err)
		os.Exit(1)
	}
}

// envFailClosed reads an on/off environment variable for a privacy setting.
// It fails closed: an unset or empty variable is off, an explicit off value
// (0, false, no, off, case-insensitive) is off, and anything else that is
// set, even a typo or just spaces, is on. Do not reuse it for settings where
// a typo turning them on would be harmful.
func envFailClosed(name string) bool {
	raw, set := os.LookupEnv(name)
	if !set || raw == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "f", "false", "n", "no", "off":
		return false
	}
	return true
}

func openStore(path *string) (*goal.Store, error) {
	return goal.Open(*path)
}
