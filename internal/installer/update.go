package installer

import (
	"context"
	"errors"
	"fmt"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// Update reinstalls whatever is installed — the active interpreter or the
// extension — against the latest release, skipping it if already up to date. The
// interpreter and the extension are never installed at once (see the invariant in
// InstallInterpreter), so there is nothing to disambiguate.
func Update(ctx context.Context, opts Options) error {
	env, err := opts.env()
	if err != nil {
		return err
	}
	layout, err := platform.Resolve(env, opts.Scope)
	if err != nil {
		return err
	}
	m, err := manifest.Load(layout.ManifestPath())
	if err != nil {
		return err
	}
	if err := reconcileInvariant(layout, m, opts); err != nil {
		return err
	}

	hasInterp := m.Active() != ""
	hasExt := m.Extension != nil
	if !hasInterp && !hasExt {
		return errors.New("nothing installed to update")
	}

	client := opts.Client
	if client == nil {
		client = release.NewClient()
	}
	rel, err := client.LatestRelease(ctx)
	if err != nil {
		return err
	}

	if hasInterp {
		return updateInterpreter(ctx, opts, m, rel, client)
	}
	return updateExtension(ctx, opts, m, rel, client)
}

func updateInterpreter(ctx context.Context, opts Options, m *manifest.Manifest, rel *release.Release, client *release.Client) error {
	key := m.Active()
	it, _ := m.Interpreter(key)
	if it.ReleaseTag == rel.TagName {
		opts.logf("Interpreter php %s is already up to date (release %s).", it.Series, rel.TagName)
		return nil
	}
	opts.logf("Updating interpreter php %s (%s): %s -> %s ...",
		it.Series, threading(it.ZTS), it.ReleaseTag, rel.TagName)

	io := opts
	io.PHPVersion = it.Series
	io.ZTS = it.ZTS
	io.BinDir = m.BinDir
	io.Client = client
	io.preloadedRelease = rel
	// Force past the "already provided" short-circuit: the active php is our own
	// interpreter for this series, so alreadyProvided would otherwise skip the
	// reinstall (it does not compare release tags) and leave the old binary.
	io.Force = true
	return InstallInterpreter(ctx, io)
}

func updateExtension(ctx context.Context, opts Options, m *manifest.Manifest, rel *release.Release, client *release.Client) error {
	ext := m.Extension
	if ext.ReleaseTag == rel.TagName {
		opts.logf("Extension for php %s is already up to date (release %s).", ext.Series, rel.TagName)
		return nil
	}
	opts.logf("Updating extension for php %s: %s -> %s ...", ext.Series, ext.ReleaseTag, rel.TagName)

	io := opts
	io.Force = true
	io.Client = client
	io.preloadedRelease = rel
	if err := InstallExtension(ctx, io); err != nil {
		return fmt.Errorf("updating extension: %w", err)
	}
	return nil
}
