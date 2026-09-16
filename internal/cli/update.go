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
			case res.Updated && res.State == update.Newer:
				fmt.Fprintf(out, "downgraded %s -> %s (%s)\n", res.Current, res.Latest, res.Path)
			case res.Updated:
				fmt.Fprintf(out, "updated %s -> %s (%s)\n", res.Current, res.Latest, res.Path)
			case res.State == update.Current:
				fmt.Fprintf(out, "already the latest release, %s\n", res.Latest)
			case res.State == update.Newer:
				fmt.Fprintf(out, "running %s, which is ahead of the latest release %s\n", res.Current, res.Latest)
			case res.State == update.Unknown:
				fmt.Fprintf(out, "running %s, which is not a release version; latest release is %s. Pass --force to install it\n", res.Current, res.Latest)
			default:
				fmt.Fprintf(out, "update available: %s -> %s; run without --check to install\n", res.Current, res.Latest)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether a newer release exists")
	cmd.Flags().BoolVar(&force, "force", false, "install the latest release even if this build is already current, newer, or not a release build")
	return cmd
}
