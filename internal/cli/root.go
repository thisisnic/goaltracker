// Package cli defines the goaltracker command tree.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thisisnic/goaltracker/internal/config"
	"github.com/thisisnic/goaltracker/internal/goal"
	"github.com/thisisnic/goaltracker/internal/tui"
	"github.com/thisisnic/goaltracker/internal/version"
)

// DefaultDBPath is where the database lives unless overridden by --db or
// the GOALTRACKER_DB environment variable: $XDG_DATA_HOME/goaltracker/goaltracker.db, falling
// back to ~/.local/share/goaltracker/goaltracker.db.
func DefaultDBPath() string {
	if p := os.Getenv("GOALTRACKER_DB"); p != "" {
		return p
	}
	return dataDirDBPath()
}

// dataDirDBPath is the tool's own location for the database, ignoring
// GOALTRACKER_DB. Only a database here lives in a directory the tool owns.
func dataDirDBPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "goaltracker", "goaltracker.db")
}

// New builds the root command.
func New() *cobra.Command {
	var dbPath, cfgPath string
	var private bool
	root := &cobra.Command{
		Use:   "goaltracker",
		Short: "A personal tracker for goals, plans and time",
		Long: `goaltracker is a local tracker for goals, plans and time.

Run it with no arguments to open the terminal UI. Subcommands give the same
data a scriptable interface; add --json to any list or show command for
machine-readable output.

Somewhere you'd rather not have people read over your shoulder, start with
--private (or set GOALTRACKER_PRIVATE=1) to hide the why, amounts and notes.
Press x in the UI to toggle it.

Backups are encrypted snapshots written to a folder you choose. Run
goaltracker key new once to set that up; with on_quit set in the config the
TUI writes one every time it exits.`,
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The config only affects backups, so a broken one must not
			// keep the UI from opening. It is reported on exit instead.
			cfg, cfgErr := config.Load(cfgPath)
			store, err := openDB(dbPath)
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
				return runBackup(cmd, store, dbPath, cfg.Backup)
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&dbPath, "db", DefaultDBPath(), "path to the SQLite database (env GOALTRACKER_DB)")
	root.PersistentFlags().StringVar(&cfgPath, "config", config.Path(), "path to the config file")
	root.Flags().BoolVar(&private, "private", envFailClosed("GOALTRACKER_PRIVATE"), "start with the why, amounts and notes hidden; x toggles (env GOALTRACKER_PRIVATE=1)")
	root.SetVersionTemplate("goaltracker {{.Version}}\n")
	root.AddCommand(goalCmd(&dbPath), keyCmd(), backupCmd(&dbPath, &cfgPath), restoreCmd(&dbPath, &cfgPath), versionCmd(), updateCmd())
	return root
}

// openDB opens the database, vouching for its directory only when it is
// the tool's own default location.
func openDB(path string) (*goal.Store, error) {
	if path == dataDirDBPath() {
		return goal.Open(path, goal.OwnDir())
	}
	return goal.Open(path)
}

// Execute runs the root command and exits non-zero on error.
func Execute() {
	root := New()
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "goaltracker:", err)
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
	return openDB(*path)
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "goaltracker %s\n", version.String())
		},
	}
}
