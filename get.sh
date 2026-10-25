#!/bin/sh
# Codex-generated devboot release downloader. Installs no Go toolchain.
set -eu
case "$(uname -s)" in Linux) ;; *) echo 'devboot requires Linux.' >&2; exit 1;; esac
case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo 'devboot supports x86_64 and ARM64.' >&2; exit 1 ;;
esac
cache="${XDG_CACHE_HOME:-$HOME/.cache}/devboot"
mkdir -p "$cache"
stage=$(mktemp -d "$cache/download.XXXXXXXX")
cleanup() { rm -rf -- "$stage"; }
trap cleanup EXIT HUP INT TERM
fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl --fail --location --retry 3 --connect-timeout 30 --proto '=https' --proto-redir '=https' --output "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget --https-only --tries=3 --timeout=30 -O "$2" "$1"
    else
        echo 'curl or wget is required to download the release.' >&2
        return 1
    fi
}
asset="devboot-linux-$arch"
base=https://github.com/voidplacer/devboot/releases/latest/download
fetch "$base/$asset" "$stage/$asset"
fetch "$base/SHA256SUMS" "$stage/SHA256SUMS"
awk -v name="$asset" '$2 == name { print; found=1 } END { if (!found) exit 1 }' "$stage/SHA256SUMS" > "$stage/selected.sha256"
(cd "$stage" && sha256sum -c selected.sha256)
chmod 700 "$stage/$asset"
"$stage/$asset" "$@"
# Use the new config immediately in an interactive terminal; the parent shell
# cannot inherit PATH changes made by a child installer.
if [ "$#" -eq 0 ] && [ -t 0 ] && [ -t 1 ] && [ "$(id -u)" -ne 0 ] && command -v zsh >/dev/null 2>&1; then
    cleanup
    trap - EXIT HUP INT TERM
    echo 'devboot is ready. Starting your configured Zsh.'
    exec zsh -l
fi
