#!/bin/sh
set -eu

REPO="circlesac/prism-cli"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
	x86_64) ARCH="amd64" ;;
	aarch64) ARCH="arm64" ;;
esac

case "$OS-$ARCH" in
	darwin-arm64|darwin-amd64|linux-amd64|linux-arm64) ;;
	*)
		echo "Unsupported platform: $OS-$ARCH" >&2
		exit 1
		;;
esac

VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
	sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' |
	head -n 1)
if [ -z "$VERSION" ]; then
	echo "Could not determine the latest Prism CLI version" >&2
	exit 1
fi

mkdir -p "$INSTALL_DIR"
echo "Installing prism $VERSION..."
curl -fsSL "https://github.com/$REPO/releases/download/$VERSION/prism-$OS-$ARCH.tar.gz" |
	tar xz -C "$INSTALL_DIR"
chmod +x "$INSTALL_DIR/prism"
printf '%s\n' "standalone" > "$INSTALL_DIR/.prism-install-method"
echo "Installed to $INSTALL_DIR/prism"
