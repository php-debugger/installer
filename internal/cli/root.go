package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// GlobalOptions holds flags shared by every subcommand.
type GlobalOptions struct {
	// User selects the no-sudo, per-user install scope instead of the default
	// system-wide scope (/opt on Linux/macOS, Program Files on Windows).
	User bool
	// Yes assumes "yes" for every interactive confirmation (for CI).
	Yes bool
	// Verbose enables extra diagnostic output.
	Verbose bool
}

var globalOpts GlobalOptions

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "php-debugger",
		Short: "Install and manage the PHP debugger",
		Long: "php-debugger installs the PHP debugger from the php-debugger/php-debugger\n" +
			"GitHub releases: either a self-contained PHP interpreter with the debugger\n" +
			"compiled in (default), or the debugger extension for an existing PHP.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := rootCmd.PersistentFlags()
	pf.BoolVarP(&globalOpts.User, "user", "u", false,
		"install into a per-user directory (no sudo); default is system-wide")
	pf.BoolVarP(&globalOpts.Yes, "yes", "y", false,
		"assume yes for all prompts (non-interactive)")
	pf.BoolVarP(&globalOpts.Verbose, "verbose", "V", false,
		"enable verbose output")

	rootCmd.AddCommand(
		newInstallCmd(),
		newUpdateCmd(),
		newUninstallCmd(),
		newSwitchCmd(),
	)

	return rootCmd
}

// Execute runs the root command and exits the process with an appropriate code.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// errNotImplemented is returned by subcommand stubs until they are built out in
// later steps of the roadmap.
func errNotImplemented(name string) error {
	return fmt.Errorf("%q is not implemented yet", name)
}
