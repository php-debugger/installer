package cli

import (
	"fmt"
	"runtime"

	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the installer version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			plat := runtime.GOOS + "/" + runtime.GOARCH
			if p, err := platform.Detect(); err == nil {
				plat = p.String()
			}
			fmt.Fprintf(cmd.OutOrStdout(), "php-debugger %s (%s)\n", version.Version, plat)
			return nil
		},
	}
}
