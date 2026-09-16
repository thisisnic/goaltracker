package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/thisisnic/goaltracker/internal/update"
	"github.com/thisisnic/goaltracker/internal/version"
)

func updateCmd() *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update to the latest release",
		Long: `Download the latest release from GitHub, verify it against the release's
published checksums, and replace this binary in place.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := update.Update(cmd.Context(), update.Options{
				Current: version.String(),
				Check:   check,
				Force:   force,
			})
			out := cmd.OutOrStdout()
			if err != nil {
				return fmt.Errorf("update: %w", err)
			}
			switch {
			case res.Updated:
				fmt.Fprintf(out, "updated %s -> %s (%s)\n", res.Current, res.Latest, res.Path)
			case res.Current == res.Latest:
				fmt.Fprintf(out, "already the latest release, %s\n", res.Latest)
			default:
				fmt.Fprintf(out, "update available: %s -> %s; run without --check to install\n", res.Current, res.Latest)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already on the latest release")
	return cmd
}
