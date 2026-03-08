#!/bin/sh
set -e

# trawl installer
# Usage: curl -fsSL https://raw.githubusercontent.com/akdavidsson/trawl/main/install.sh | sh

REPO="akdavidsson/trawl"
INSTALL_DIR="/usr/local/bin"
BINARY="trawl"

main() {
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) echo "Error: unsupported architecture: $arch" >&2; exit 1 ;;
    esac

    case "$os" in
        linux) os="linux" ;;
        darwin) os="darwin" ;;
        *) echo "Error: unsupported OS: $os" >&2; exit 1 ;;
    esac

    # Get latest release tag
    tag="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)"
    if [ -z "$tag" ]; then
        echo "Error: could not determine latest release" >&2
        exit 1
    fi

    echo "Installing trawl ${tag} (${os}/${arch})..."

    tarball="trawl_${os}_${arch}.tar.gz"
    url="https://github.com/${REPO}/releases/download/${tag}/${tarball}"

    tmpdir="$(mktemp -d)"
    trap 'rm -rf "$tmpdir"' EXIT

    curl -fsSL "$url" -o "${tmpdir}/${tarball}"
    tar -xzf "${tmpdir}/${tarball}" -C "$tmpdir"

    # Install binary
    if [ -w "$INSTALL_DIR" ]; then
        mv "${tmpdir}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
    else
        echo "Installing to ${INSTALL_DIR} (requires sudo)..."
        sudo mv "${tmpdir}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
    fi

    chmod +x "${INSTALL_DIR}/${BINARY}"

    echo "trawl installed to ${INSTALL_DIR}/${BINARY}"
    echo ""
    echo "Get started:"
    echo "  export ANTHROPIC_API_KEY=sk-ant-..."
    echo "  trawl \"https://books.toscrape.com\" --fields \"title, price, rating\""
}

main
