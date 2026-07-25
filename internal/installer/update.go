package installer

import (
	"context"
	"errors"
	"fmt"

	"github.com/php-debugger/installer/internal/manifest"
	"github.com/php-debugger/installer/internal/platform"
	"github.com/php-debugger/installer/internal/release"
)

// Update reinstalls the active interpreter and/or the extension against the
// latest release. With neither wantInterp nor wantExt set it updates whatever is
// installed, erroring if both are present (ambiguous). Each target is skipped if
// already on the latest release.
func Update(ctx context.Context, opts Options, wantInterp, wantExt bool) error {
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

	hasInterp := m.Active() != ""
	hasExt := m.Extension != nil

	if !wantInterp && !wantExt {
		switch {
		case hasInterp && hasExt:
			return errors.New("both an interpreter and an extension are installed; " +
				"specify --interpreter or --extension")
		case hasInterp:
			wantInterp = true
		case hasExt:
			wantExt = true
		default:
			return errors.New("nothing installed to update")
		}
	}
	if wantInterp && !hasInterp {
		return errors.New("no interpreter installed to update")
	}
	if wantExt && !hasExt {
		return errors.New("no extension installed to update")
	}

	// Fetch the latest release once and reuse it for the install(s).
	client := opts.Client
	if client == nil {
		client = release.NewClient()
	}
	rel, err := client.LatestRelease(ctx)
	if err != nil {
		return err
	}

	if wantInterp {
		if err := updateInterpreter(ctx, opts, m, rel, client); err != nil {
			return err
		}
	}
	if wantExt {
		if err := updateExtension(ctx, opts, m, rel, client); err != nil {
			return err
		}
	}
	return nil
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
