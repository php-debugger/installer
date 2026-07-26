#!/bin/sh
# php-debugger installer bootstrap (Linux/macOS). Always installs the latest release.
#
# Usage:
#   curl -fsSL https://github.com/php-debugger/installer/releases/latest/download/install.sh | sh
#   wget -qO- https://github.com/php-debugger/installer/releases/latest/download/install.sh | sh
#
# Environment overrides:
#   INSTALL_DIR=/path   where to place the binary (default: current directory)
#
# Because the archive is fetched with curl/wget (not a browser), the binary is
# not quarantined, so macOS Gatekeeper does not block it. As a safety net the
# script also clears the quarantine attribute if present.
set -eu

REPO="php-debugger/installer"
BIN="php-debugger"
INSTALL_DIR="${INSTALL_DIR:-$(pwd)}"

info() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

download() { # download URL FILE
	if have curl; then curl -fsSL "$1" -o "$2"
	elif have wget; then wget -q "$1" -O "$2"
	else fail "need curl or wget to download"; fi
}

# --- detect OS ---
kernel="$(uname -s)"
case "$kernel" in
	Linux) OS="linux" ;;
	Darwin) OS="macos" ;;
	*) fail "unsupported operating system: $kernel (see the build-from-source instructions)" ;;
esac

# --- detect architecture ---
machine="$(uname -m)"
case "$machine" in
	x86_64 | amd64) ARCH="amd64" ;;
	arm64 | aarch64) ARCH="arm64" ;;
	*) fail "unsupported architecture: $machine" ;;
esac

ASSET="php-debugger-${OS}-${ARCH}.tar.gz"
# The releases/latest/download URL always resolves to the newest release.
URL="https://github.com/$REPO/releases/latest/download/$ASSET"

info "Installing $BIN (latest, $OS/$ARCH)"

# --- download + extract ---
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM
download "$URL" "$tmp/$ASSET" || fail "failed to download $URL"
tar -xzf "$tmp/$ASSET" -C "$tmp" || fail "failed to extract $ASSET"
[ -f "$tmp/$BIN" ] || fail "archive did not contain $BIN"

# --- install ---
mkdir -p "$INSTALL_DIR"
mv "$tmp/$BIN" "$INSTALL_DIR/$BIN"
chmod +x "$INSTALL_DIR/$BIN"
# Clear any quarantine attribute so macOS Gatekeeper does not block it.
if [ "$OS" = "macos" ]; then
	xattr -d com.apple.quarantine "$INSTALL_DIR/$BIN" 2>/dev/null || true
fi

info ""
info "Installed to $INSTALL_DIR/$BIN"
case ":$PATH:" in
	*":$INSTALL_DIR:"*)
		info "Run:  $BIN install"
		;;
	*)
		info "Add $INSTALL_DIR to your PATH, then run '$BIN install':"
		info "    export PATH=\"$INSTALL_DIR:\$PATH\""
		;;
esac
