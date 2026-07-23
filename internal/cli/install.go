package cli

import "github.com/spf13/cobra"

// installOptions holds flags specific to the install command.
type installOptions struct {
	// PHPVersion selects the PHP version to install (e.g. "8.3"); empty means
	// the latest available in the release. Interpreter installs only.
	PHPVersion string
	// ZTS installs the thread-safe build; default is non-thread-safe.
	// Interpreter installs only.
	ZTS bool
	// ExtensionOnly installs just the debugger extension into the current PHP
	// instead of a full interpreter.
	ExtensionOnly bool
}

func newInstallCmd() *cobra.Command {
	opts := &installOptions{}

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the PHP debugger interpreter (default) or just the extension",
		Long: "Install a self-contained PHP interpreter with the debugger compiled in\n" +
			"(default), or, with --extension-only, install just the debugger extension\n" +
			"into the currently active PHP.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented("install")
		},
	}

	f := cmd.Flags()
	f.StringVarP(&opts.PHPVersion, "php", "p", "",
		"PHP version to install, e.g. 8.3 (default: latest available; interpreter only)")
	f.BoolVarP(&opts.ZTS, "zts", "z", false,
		"install the thread-safe (ZTS) build; default is non-thread-safe (interpreter only)")
	f.BoolVarP(&opts.ExtensionOnly, "extension-only", "e", false,
		"install only the debugger extension into the current PHP")

	return cmd
}
