package cli

import (
	"github.com/php-debugger/installer/internal/installer"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/spf13/cobra"
)

// updateOptions holds flags specific to the update command.
type updateOptions struct {
	// Extension updates the installed debugger extension.
	Extension bool
	// Interpreter updates the active debugger interpreter.
	Interpreter bool
}

func newUpdateCmd() *cobra.Command {
	opts := &updateOptions{}

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the installed interpreter or extension to the latest release",
		Long: "Re-install the currently active interpreter (or the extension) against the\n" +
			"latest release. Use --extension or --interpreter to disambiguate when both\n" +
			"are installed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installer.Update(cmd.Context(), installer.Options{
				Scope:     platform.ScopeFromUserFlag(globalOpts.User),
				AssumeYes: globalOpts.Yes,
				Out:       cmd.OutOrStdout(),
				In:        cmd.InOrStdin(),
			}, opts.Interpreter, opts.Extension)
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&opts.Extension, "extension", "e", false, "update the debugger extension")
	f.BoolVarP(&opts.Interpreter, "interpreter", "i", false, "update the debugger interpreter")

	return cmd
}
