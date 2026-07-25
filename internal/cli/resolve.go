package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/php-debugger/installer/internal/php"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
	"github.com/spf13/cobra"
)

// resolveOptions mirrors the install flags that affect asset selection.
type resolveOptions struct {
	PHPVersion    string
	ZTS           bool
	ExtensionOnly bool
}

// newResolveCmd is a hidden diagnostic command: it detects the host, fetches the
// latest release, and prints the asset it would download — without installing
// anything. Useful for verifying platform detection and asset selection.
func newResolveCmd() *cobra.Command {
	opts := &resolveOptions{}

	cmd := &cobra.Command{
		Use:    "resolve",
		Short:  "Print the release asset that would be used for this host (diagnostic)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResolve(cmd, opts)
		},
	}

	f := cmd.Flags()
	f.StringVarP(&opts.PHPVersion, "php", "p", "", "PHP version (default: latest available)")
	f.BoolVarP(&opts.ZTS, "zts", "z", false, "thread-safe build")
	f.BoolVarP(&opts.ExtensionOnly, "extension-only", "e", false, "resolve the extension instead of the interpreter")

	return cmd
}

func runResolve(cmd *cobra.Command, opts *resolveOptions) error {
	p, err := platform.Detect()
	if err != nil {
		return err
	}

	kind := release.Interpreter
	if opts.ExtensionOnly {
		kind = release.Extension
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()

	client := release.NewClient()
	rel, err := client.LatestRelease(ctx)
	if err != nil {
		return err
	}

	series := opts.PHPVersion
	if series == "" {
		series, err = release.LatestSeries(rel.Assets, kind, opts.ZTS, p.OS, p.Arch)
		if err != nil {
			return err
		}
	}

	asset, err := release.SelectAsset(rel.Assets, release.Selector{
		Kind:   kind,
		Series: series,
		ZTS:    opts.ZTS,
		OS:     p.OS,
		Arch:   p.Arch,
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "host:      %s\n", p)
	fmt.Fprintf(out, "release:   %s\n", rel.TagName)
	fmt.Fprintf(out, "kind:      %s\n", kind)
	fmt.Fprintf(out, "php:       %s (zts=%t)\n", series, opts.ZTS)
	fmt.Fprintf(out, "asset:     %s\n", asset.Name)
	fmt.Fprintf(out, "url:       %s\n", asset.DownloadURL)
	if asset.Size > 0 {
		fmt.Fprintf(out, "size:      %.1f MiB\n", float64(asset.Size)/(1024*1024))
	}

	printExistingPHP(ctx, out)
	return nil
}

// printExistingPHP prints facts about the php interpreter currently on PATH, for
// diagnostics. It never fails the command.
func printExistingPHP(ctx context.Context, out io.Writer) {
	path, err := php.Detect()
	if err != nil {
		fmt.Fprintf(out, "existing:  none on PATH\n")
		return
	}
	info, err := php.Query(ctx, path)
	if err != nil {
		fmt.Fprintf(out, "existing:  %s (could not query: %v)\n", path, err)
		return
	}
	loaded := info.Ini.LoadedFile
	if loaded == "" {
		loaded = "(none)"
	}
	fmt.Fprintf(out, "existing:  %s\n", info.Path)
	fmt.Fprintf(out, "  version:       %s (series %s, zts=%t)\n", info.Version, info.Series, info.ZTS)
	fmt.Fprintf(out, "  loaded ini:    %s\n", loaded)
	fmt.Fprintf(out, "  extension_dir: %s\n", info.ExtensionDir)
	if len(info.Ini.AdditionalFiles) > 0 {
		fmt.Fprintf(out, "  extra ini:     %d file(s)\n", len(info.Ini.AdditionalFiles))
	}
}
