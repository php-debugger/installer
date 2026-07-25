# php-debugger installer

A cross-platform command-line installer for the [PHP debugger](https://github.com/php-debugger/php-debugger).
It installs either a self-contained PHP interpreter with the debugger compiled in
(the default), or just the debugger extension into your existing PHP. It always
pulls the **latest release** and auto-detects your OS and architecture.

## Installation

### Download a prebuilt binary

Grab the archive for your platform from the
[Releases page](https://github.com/php-debugger/installer/releases):

| Platform | Asset |
| --- | --- |
| macOS (Apple Silicon) | `php-debugger-macos-arm64.tar.gz` |
| macOS (Intel) | `php-debugger-macos-amd64.tar.gz` |
| Linux (x86_64) | `php-debugger-linux-amd64.tar.gz` |
| Linux (arm64) | `php-debugger-linux-arm64.tar.gz` |
| Windows (x64) | `php-debugger-windows-amd64.zip` |

On macOS/Linux, extract **in the terminal** (not by double-clicking in Finder).
Command-line `tar` keeps the executable bit and avoids macOS Gatekeeper flagging
the binary — no `chmod` or "allow anyway" needed:

```bash
tar -xzf php-debugger-macos-arm64.tar.gz
mv php-debugger /usr/local/bin/    # or ~/.local/bin
php-debugger --help
```

On Windows, unzip the archive and put `php-debugger.exe` somewhere on your PATH.

### Build from source

Requires Go 1.25+:

```bash
go build -o php-debugger .
# optionally move it onto your PATH
mv php-debugger /usr/local/bin/   # or ~/.local/bin, etc.
```

## Quick start

```bash
# Install the latest debugger interpreter (replacing your current php, with a backup)
php-debugger install

# Or install just the extension into your current php
php-debugger install --extension-only

# Switch the active PHP version (installs it if needed)
php-debugger switch 8.4

# Update to the latest release
php-debugger update

# Remove it (restoring the interpreter you had before, if any)
php-debugger uninstall
```

## Commands

| Command | What it does |
| --- | --- |
| `install` | Install the debugger **interpreter** (default) or the **extension** (`-e`). |
| `switch <version>` | Make an installed version active, installing it first if needed. |
| `update` | Reinstall the active interpreter and/or extension against the latest release. |
| `uninstall [version]` | Remove an interpreter (or the extension), restoring any backup. |

### Flags

Global:

- `-u, --user` — install into a per-user directory (no sudo). Default is system-wide.
- `-y, --yes` — assume "yes" for prompts (non-interactive / CI).
- `-V, --verbose` — extra output.

`install`:

- `-p, --php <x.y>` — PHP version to install (default: latest; interpreter only).
- `-z, --zts` — thread-safe build (default: non-thread-safe; interpreter only).
- `-e, --extension-only` — install only the extension into the current php.

`update` / `uninstall`:

- `-e, --extension` / `-i, --interpreter` — pick the target when both are installed.
- `uninstall` also takes an optional `<version>` and `-z, --zts` to target a variant.

## Install locations

Interpreters are installed under a versioned directory and the active one is
symlinked onto your PATH.

**System-wide (default, needs sudo/admin):**

| Platform | Install root | Active `php` |
| --- | --- | --- |
| macOS arm64 | `/opt/php-debugger` | `/opt/homebrew/bin` (or `/usr/local/bin`) |
| macOS Intel / Linux | `/usr/local/php-debugger` | `/usr/local/bin` |
| Windows | `%ProgramFiles%\php-debugger` | `<root>\bin` |

**Per-user (`--user`, no sudo):**

| Platform | Install root | Active `php` |
| --- | --- | --- |
| macOS | `~/Library/Application Support/php-debugger` | `~/.local/bin` |
| Linux | `$XDG_DATA_HOME` or `~/.local/share/php-debugger` | `~/.local/bin` |
| Windows | `%LOCALAPPDATA%\php-debugger` | `<root>\bin` |

If the chosen bin directory isn't on your `PATH`, the installer prints the exact
line to add.

## How it works

- **Verification first.** The downloaded interpreter is run (`php -v`) before any
  change is made. If it can't run on your system, nothing is installed and you're
  advised to use `--extension-only` or build from source.
- **Replace with a backup.** If you already have a `php`, it is backed up and
  replaced in place, so `php` immediately resolves to the debugger build.
  `uninstall` restores it.
- **Config carried over.** Your existing ini configuration is copied to the new
  interpreter's config path. Xdebug loaders are removed (the debugger provides its
  own simulated xdebug), other extension loaders are commented out (a
  self-contained build can't load foreign `.so` files), and `xdebug.mode` is
  constrained to `off`/`debug` (with a prompt if other modes are present).
- **Post-install check + rollback.** After activating, it confirms the new `php`
  runs and reports the `php_debugger` module. Any failure rolls everything back to
  the prior working state.
- **Multiple versions coexist.** `switch` flips the active version instantly via
  the symlink; installed versions are kept side by side.

## Platform support

Linux, macOS and Windows; x86_64 and arm64. On Windows, where symlinks require
elevated privileges, the active `php` falls back to a generated `.cmd` shim. On
Apple Silicon, an x86_64 build still installs native arm64 binaries.

## Development

```bash
go build ./...
go test -race ./...
gofmt -l .
```

The code is organized under `internal/`: `platform` (OS/arch, paths, symlinks),
`release` (GitHub API + asset selection), `php` (interpreter introspection),
`ini` (config rewriting), `manifest` (on-disk state), `installer` (orchestration),
and `cli` (commands). A hidden `php-debugger resolve` command prints what would be
downloaded for the current host — handy for debugging.
