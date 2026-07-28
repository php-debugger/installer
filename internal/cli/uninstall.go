package cli

import (
	"github.com/php-debugger/installer/internal/installer"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/spf13/cobra"
)

// uninstallOptions holds flags specific to the uninstall command.
type uninstallOptions struct {
	// ZTS selects the thread-safe variant when a version is given.
	ZTS bool
}

func newUninstallCmd() *cobra.Command {
	opts := &uninstallOptions{}

	cmd := &cobra.Command{
		Use:   "uninstall [version]",
		Short: "Uninstall the debugger, restoring any backup",
		Long: "Remove the installed debugger. The interpreter and the extension are never\n" +
			"installed at once, so the kind is detected automatically; pass a version to\n" +
			"select a specific installed interpreter variant. If a backup of a previously\n" +
			"replaced interpreter exists, it is restored.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var version string
			if len(args) == 1 {
				version = args[0]
			}
			return installer.Uninstall(cmd.Context(), installer.Options{
				Scope:     platform.ScopeFromUserFlag(globalOpts.User),
				AssumeYes: globalOpts.Yes,
				Out:       cmd.OutOrStdout(),
				In:        cmd.InOrStdin(),
			}, version, opts.ZTS)
		},
	}

	cmd.Flags().BoolVarP(&opts.ZTS, "zts", "z", false, "select the thread-safe (ZTS) variant of the given version")

	return cmd
}
