package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/thisisnic/goaltracker/internal/backup"
	"github.com/thisisnic/goaltracker/internal/config"
	"github.com/thisisnic/goaltracker/internal/goal"
)

func keyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage the backup encryption key",
	}
	cmd.AddCommand(keyNewCmd())
	return cmd
}

func keyNewCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Create an age keypair for encrypting backups",
		Long: `Create an age keypair. The private key is written to a file only you can
read. The public key is printed with a config file you can copy into place.

Keep a copy of the private key somewhere safe that is not this machine, such
as a password manager. Backups cannot be opened without it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if out == "" {
				out = config.DefaultIdentityFile()
			}
			recipient, err := backup.NewKey(out)
			if err != nil {
				if errors.Is(err, os.ErrExist) {
					return fmt.Errorf("%s already exists; pass --out to write somewhere else", out)
				}
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "private key written to %s\n", out)
			fmt.Fprintf(w, "public key: %s\n\n", recipient)
			fmt.Fprintf(w, "Copy the private key file's contents into your password manager now.\n\n")
			fmt.Fprintf(w, "Then put this in %s:\n\n%s", config.Path(), config.Example(recipient, out))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "where to write the private key (default "+config.DefaultIdentityFile()+")")
	return cmd
}

func backupCmd(dbPath, cfgPath *string) *cobra.Command {
	var dir, recipient string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write an encrypted copy of the database to the backup folder",
		Long: `Write a consistent, encrypted copy of the database as goaltracker.db.age in the
backup folder, replacing the previous one. Nothing is written if the
database is unchanged since the last backup. The folder and key come from
the config file unless given here.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			if dir != "" {
				cfg.Backup.Dir = config.ExpandHome(dir)
			}
			if recipient != "" {
				cfg.Backup.Recipient = recipient
			}
			if !cfg.Backup.Configured() {
				return fmt.Errorf("no backup folder or key configured; run `goaltracker key new` and follow its instructions, or pass --dir and --recipient")
			}
			store, err := goal.Open(*dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			return runBackup(cmd, store, *dbPath, cfg.Backup)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "backup folder (overrides config)")
	cmd.Flags().StringVar(&recipient, "recipient", "", "age public key (overrides config)")
	return cmd
}

// runBackup takes a snapshot and reports what happened on stdout.
func runBackup(cmd *cobra.Command, store *goal.Store, dbPath string, b config.Backup) error {
	res, err := backup.Run(cmd.Context(), store, backup.Options{
		Dir:       b.Dir,
		Recipient: b.Recipient,
		Marker:    backup.MarkerPath(config.Dir(), dbPath, b.Dir),
	})
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if res.Skipped {
		fmt.Fprintln(cmd.OutOrStdout(), "backup: no changes since the last backup")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "backup: wrote %s\n", res.Path)
	}
	if !b.Git {
		return nil
	}
	err = backup.Push(cmd.Context(), b.Dir, time.Now())
	switch {
	case err == nil:
		fmt.Fprintln(cmd.OutOrStdout(), "backup: pushed")
		return nil
	case errors.Is(err, backup.ErrPushFailed):
		// The backup and its commit are safe locally; the next run retries.
		fmt.Fprintf(cmd.ErrOrStderr(), "backup: committed locally but not pushed; will retry next time. %v\n", err)
		return nil
	default:
		return fmt.Errorf("backup git: %w", err)
	}
}

func restoreCmd(dbPath, cfgPath *string) *cobra.Command {
	var identity string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore [FILE]",
		Short: "Replace the database with a decrypted backup",
		Long: `Decrypt a backup with your private key and put it in place of the current
database. With no FILE, the goaltracker.db.age in the configured backup folder is
used. The current database is kept next to it as goaltracker.db.bak. Close any
running goaltracker first.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*cfgPath)
			if err != nil {
				return err
			}
			if identity == "" {
				identity = cfg.Backup.IdentityFile
			}
			if identity == "" {
				return fmt.Errorf("no private key: set identity_file in %s or pass --identity", *cfgPath)
			}
			var file string
			switch {
			case len(args) == 1:
				file = args[0]
			case cfg.Backup.Dir != "":
				file = filepath.Join(cfg.Backup.Dir, backup.FileName)
			default:
				return fmt.Errorf("no backup file given and no backup folder in %s", *cfgPath)
			}
			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(), "replace %s with %s? The current database is kept as .bak [y/N] ", *dbPath, file)
				var answer string
				fmt.Fscanln(cmd.InOrStdin(), &answer)
				if answer != "y" && answer != "Y" && answer != "yes" {
					fmt.Fprintln(cmd.OutOrStdout(), "kept")
					return nil
				}
			}
			kept, err := backup.Restore(file, config.ExpandHome(identity), *dbPath, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored %s from %s\n", *dbPath, file)
			if kept != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "previous database kept at %s\n", kept)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&identity, "identity", "", "age private key file (overrides config)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
