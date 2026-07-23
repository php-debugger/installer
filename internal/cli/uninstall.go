package cli

import "github.com/spf13/cobra"

// uninstallOptions holds flags specific to the uninstall command.
type uninstallOptions struct {
	// Extension uninstalls the debugger extension.
	Extension bool
	// Interpreter uninstalls a debugger interpreter (optionally a specific
	// version given as a positional argument).
	Interpreter bool
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
			return errNotImplemented("uninstall")
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&opts.Extension, "extension", "e", false, "uninstall the debugger extension")
	f.BoolVarP(&opts.Interpreter, "interpreter", "i", false, "uninstall the debugger interpreter")

	return cmd
}
