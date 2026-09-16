package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/thisisnic/lifeo/internal/backup"
	"github.com/thisisnic/lifeo/internal/config"
	"github.com/thisisnic/lifeo/internal/goal"
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
		Short: "Write an encrypted snapshot of the database",
		Long: `Write a consistent, encrypted snapshot of the database into the backup
folder. Nothing is written if the database is unchanged since the last
snapshot. The folder and key come from the config file unless given here.`,
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
				return fmt.Errorf("no backup folder or key configured; run `lifeo key new` and follow its instructions, or pass --dir and --recipient")
			}
			store, err := goal.Open(*dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			return runBackup(cmd, store, cfg.Backup)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "backup folder (overrides config)")
	cmd.Flags().StringVar(&recipient, "recipient", "", "age public key (overrides config)")
	return cmd
}

// runBackup takes a snapshot and reports what happened on stdout.
func runBackup(cmd *cobra.Command, store *goal.Store, b config.Backup) error {
	res, err := backup.Run(cmd.Context(), store, b.Dir, b.Recipient, time.Now())
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if res.Skipped {
		fmt.Fprintln(cmd.OutOrStdout(), "backup: no changes since the last snapshot")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "backup: wrote %s\n", res.Path)
	return nil
}

func restoreCmd(dbPath, cfgPath *string) *cobra.Command {
	var identity string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore SNAPSHOT",
		Short: "Replace the database with a decrypted snapshot",
		Long: `Decrypt a snapshot with your private key and put it in place of the current
database. The current database is kept next to it as lifeo.db.bak. Close any
running lifeo first.`,
		Args: cobra.ExactArgs(1),
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
			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(), "replace %s with %s? The current database is kept as .bak [y/N] ", *dbPath, args[0])
				var answer string
				fmt.Fscanln(cmd.InOrStdin(), &answer)
				if answer != "y" && answer != "Y" && answer != "yes" {
					fmt.Fprintln(cmd.OutOrStdout(), "kept")
					return nil
				}
			}
			bak, err := backup.Restore(args[0], config.ExpandHome(identity), *dbPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored %s\n", *dbPath)
			if bak != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "previous database kept at %s\n", bak)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&identity, "identity", "", "age private key file (overrides config)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
