package cli

import (
	"github.com/php-debugger/installer/internal/installer"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/spf13/cobra"
)

// uninstallOptions holds flags specific to the uninstall command.
type uninstallOptions struct {
	// Extension uninstalls the debugger extension.
	Extension bool
	// Interpreter uninstalls a debugger interpreter (optionally a specific
	// version given as a positional argument).
	Interpreter bool
	// ZTS selects the thread-safe variant when a version is given.
	ZTS bool
}

func newUninstallCmd() *cobra.Command {
	opts := &uninstallOptions{}

	cmd := &cobra.Command{
		Use:   "uninstall [version]",
		Short: "Uninstall the interpreter or extension, restoring any backup",
		Long: "Remove a debugger interpreter (optionally a specific version) or the\n" +
			"debugger extension. If a backup of a previously replaced interpreter exists,\n" +
			"it is restored.",
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
			}, opts.Interpreter, opts.Extension, version, opts.ZTS)
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&opts.Extension, "extension", "e", false, "uninstall the debugger extension")
	f.BoolVarP(&opts.Interpreter, "interpreter", "i", false, "uninstall the debugger interpreter")
	f.BoolVarP(&opts.ZTS, "zts", "z", false, "select the thread-safe (ZTS) variant of the given version")

	return cmd
}
