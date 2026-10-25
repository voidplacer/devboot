#!/bin/sh
# Small launcher only: provisioning and configuration live in main.go.
# A temporary, checksum-verified Go toolchain avoids old Ubuntu Go packages.
set -eu
if [ "$(id -u)" -ne 0 ]; then
    exec sudo -- "$0" "$@"
fi
cd -- "$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
case "$(uname -m)" in
    x86_64) goarch=amd64 ;;
    aarch64|arm64) goarch=arm64 ;;
    *) echo 'Only x86_64 and ARM64 Linux are supported.' >&2; exit 1 ;;
esac
case "$(uname -s)" in Linux) ;; *) echo 'Linux is required.' >&2; exit 1 ;; esac
if ! command -v curl >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1; then
    # No prompts; the Go executable repeats distro detection before provisioning.
    . /etc/os-release
    case " $ID ${ID_LIKE:-} " in
        *' ubuntu '*) apt-get update; DEBIAN_FRONTEND=noninteractive apt-get install -y curl ca-certificates python3 tar gzip ;;
        *' fedora '*) dnf install -y curl ca-certificates python3 tar gzip ;;
        *' arch '*) pacman -Syu --needed --noconfirm curl ca-certificates python tar gzip ;;
        *) echo 'Supported distributions: Ubuntu, Fedora, Arch.' >&2; exit 1 ;;
    esac
fi
stage=$(mktemp -d /tmp/myeditorconfig-launcher.XXXXXXXX)
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
curl --fail --location --retry 3 --proto '=https' --proto-redir '=https' \
    'https://go.dev/dl/?mode=json' -o "$stage/releases.json"
python3 - "$stage" "$goarch" <<'PY'
import hashlib, json, os, sys, tarfile, urllib.request
stage, arch = sys.argv[1:]
with open(os.path.join(stage, 'releases.json')) as f:
    releases = json.load(f)
release = next(r for r in releases if r['stable'])
asset = next(a for a in release['files']
             if a['os'] == 'linux' and a['arch'] == arch and a['kind'] == 'archive')
archive = os.path.join(stage, 'go.tar.gz')
print('Downloading temporary Go toolchain:', release['version'], flush=True)
urllib.request.urlretrieve('https://go.dev/dl/' + asset['filename'], archive)
h = hashlib.sha256()
with open(archive, 'rb') as f:
    for block in iter(lambda: f.read(1024 * 1024), b''):
        h.update(block)
if h.hexdigest() != asset['sha256']:
    raise SystemExit('Go toolchain checksum mismatch')
with tarfile.open(archive) as t:
    # Go archives use relative paths. Reject path escapes even on older Python.
    root = os.path.realpath(stage) + os.sep
    for member in t.getmembers():
        if not os.path.realpath(os.path.join(stage, member.name)).startswith(root):
            raise SystemExit('Unsafe archive path')
        if member.issym() or member.islnk():
            raise SystemExit('Unexpected archive link')
    t.extractall(stage)
PY
mkdir "$stage/cache"
CGO_ENABLED=0 GOTOOLCHAIN=local GOTELEMETRY=off GOENV=off GOPATH="$stage/gopath" GOCACHE="$stage/cache" "$stage/go/bin/go" build -trimpath -o "$stage/bootstrap" .
"$stage/bootstrap" "$@"
