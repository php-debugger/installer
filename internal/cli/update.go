package cli

import (
	"github.com/php-debugger/installer/internal/installer"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the installed debugger to the latest release",
		Long: "Re-install whatever is installed — the active interpreter or the extension —\n" +
			"against the latest release. The two are never installed at once, so the kind\n" +
			"is detected automatically.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installer.Update(cmd.Context(), installer.Options{
				Scope:     platform.ScopeFromUserFlag(globalOpts.User),
				AssumeYes: globalOpts.Yes,
				Out:       cmd.OutOrStdout(),
				In:        cmd.InOrStdin(),
			})
		},
	}

	return cmd
}
