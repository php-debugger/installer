package cli

import (
	"github.com/php-debugger/installer/internal/installer"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/spf13/cobra"
)

// switchOptions holds flags specific to the switch command.
type switchOptions struct {
	// ZTS selects the thread-safe variant of the target version.
	ZTS bool
}

func newSwitchCmd() *cobra.Command {
	opts := &switchOptions{}

	cmd := &cobra.Command{
		Use:   "switch <version>",
		Short: "Switch the active PHP version (installing it first if needed)",
		Long: "Point the active PHP symlink at the given version (and optionally its\n" +
			"thread-safe variant). If that variant is not installed yet, it is installed\n" +
			"first.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return installer.Switch(cmd.Context(), installer.Options{
				Scope:      platform.ScopeFromUserFlag(globalOpts.User),
				PHPVersion: args[0],
				ZTS:        opts.ZTS,
				AssumeYes:  globalOpts.Yes,
				Out:        cmd.OutOrStdout(),
				In:         cmd.InOrStdin(),
			})
		},
	}

	cmd.Flags().BoolVarP(&opts.ZTS, "zts", "z", false,
		"select the thread-safe (ZTS) variant of the target version")

	return cmd
}
