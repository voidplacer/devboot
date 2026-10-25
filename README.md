# devboot

**Codex-generated** Go installer for my Linux development environment. Source,
installer, tests and documentation were generated with OpenAI Codex around my
existing personal editor configs.

## One command on a new machine

Paste this **single line** into your normal user terminal, then enter your sudo
password when asked:

```sh
sh -c 'f=$(mktemp) || exit; cleanup(){ rm -f "$f"; }; trap cleanup EXIT; u=https://github.com/voidplacer/devboot/releases/latest/download/get.sh; if command -v curl >/dev/null 2>&1; then curl -fsSL --retry 3 "$u" -o "$f"; else wget --tries=3 -O "$f" "$u"; fi && sh "$f"'
```

The downloader detects x86_64/ARM64, fetches the latest standalone binary,
verifies its SHA-256, and runs it. The binary detects Ubuntu, Fedora or Arch and
requests sudo itself. There are no distro questions and no Go installation or
language runtime required to start it. At completion, an interactive terminal
opens your configured Zsh immediately. Future logins use Zsh too; reboot for
Caps Lock → Escape on all keyboards. Escape stays Escape.

You need working internet access, sudo privileges, working distro repositories,
and either `curl` or `wget` to fetch the release. The shell downloader uses normal
Linux utilities (`sh`, `mktemp`, `sha256sum`, etc.). The binary itself has no
external runtime requirement. It embeds the configs; no repository clone is
needed. This is an online installer, not an offline bundle of the applications.

## Speed and last-minute setups

The release route skips downloading/building Go. Independent downloads and
setup run in parallel, Ubuntu avoids duplicate Kitty installs and unnecessary
recommended packages, and existing compatible large runtimes are reused.
Source-build dependencies are installed only when required on Ubuntu.

**A complete install is not guaranteed within five minutes.** Download speed,
repository mirrors, hardware, plugin downloads and existing packages determine
the time. Ubuntu 20.04 needs source builds for current Neovim and tree-sitter,
which can take many minutes. Arch performs a full system upgrade. Prepare a
machine ahead of time whenever possible; rerunning on an already configured
machine avoids the expensive source builds.

## Standalone downloads and preview

[Latest release](https://github.com/voidplacer/devboot/releases/latest) contains:

| File | Purpose |
| --- | --- |
| `devboot-linux-amd64` | Ordinary 64-bit Intel/AMD Linux PCs |
| `devboot-linux-arm64` | ARM64 Linux machines |
| `SHA256SUMS` | SHA-256 checksums of the binaries and downloader |
| `get.sh` | Architecture detection, checked download and launch |

You can download a binary alone, restore its executable permission, and run it:

```sh
chmod +x devboot-linux-amd64
./devboot-linux-amd64 --dry-run
./devboot-linux-amd64
```

`--dry-run` prints the plan without changes, downloads or sudo. Use the ARM64
filename on ARM machines. Direct root logins must select the intended non-root
account: `./devboot-linux-amd64 --user alice`. Normal users simply run the binary;
`sudo ./devboot-linux-amd64` works too.

## Building from source

With Go 1.22+:

```sh
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/devboot-linux-amd64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/devboot-linux-arm64 .
cp get.sh dist/get.sh
(cd dist && sha256sum devboot-linux-amd64 devboot-linux-arm64 get.sh > SHA256SUMS)
```

Both binaries are statically linked and embed the selected configs, including
`.zshrc` and `.clang-format`. Rebuild after editing source or configs.
`sudo ./install.sh` is an alternative source launcher that downloads a temporary,
checksum-verified official Go toolchain. The release downloader above is faster.

## What it installs

- Zsh, Oh My Zsh, autosuggestions and syntax highlighting; makes Zsh the login shell.
- Kitty, Zed stable, CaskaydiaMono Nerd Font, font cache, and your settings/keymaps.
  Zed automatically installs the Catppuccin theme/icon extensions on first launch.
- Neovim and LazyVim starter, your Lua files, and the C/C++ and Java extras from
  `temp_neovim/LazyExtras.txt`. Plugins are synchronized headlessly as your user.
- Clang/Clang++, clang-format, clangd, C/C++ development libraries and build tools.
- eza, zoxide, current fzf, fd, ripgrep, tmux and your tmux configuration.
- Official Node LTS, tree-sitter CLI and Temurin Java 21 for editor language tools
  and the Java helper in your Zsh config.
- Your home `.clang-format`; Caps Lock remapping using udev hwdb for AT and HID
  keyboards, across X11, Wayland and Linux consoles.

It detects Ubuntu, Fedora and Arch using `/etc/os-release`. On Ubuntu it enables
Universe when needed and **downloads the latest stable upstream Neovim**, never the older
Ubuntu Neovim package. If that binary cannot run on the host's glibc, it builds
the same release from source with a temporary current CMake. Fedora/Arch use
their Neovim packages, with an upstream fallback if they do not meet LazyVim's
minimum version. Arch runs `pacman -Syu` to avoid a partial system upgrade.
Release discovery uses GitHub release pages and their published asset digests,
so it does not consume the unauthenticated GitHub API quota shared by an IP.
Kitty uses its current official binary on Ubuntu, falling back to official
Kitty 0.42.2 when the current binary needs a newer glibc (as on Ubuntu 20.04).
Both the executable and supplied config are checked in the integration tests.
Other distro packages come from their configured repositories.

## Existing settings and reruns

Changed user files are moved under
`~/.local/state/myeditorconfig/backups/<timestamp>/`, preserving their paths and
symlinks. Existing Neovim config/data/state/cache are backed up on the first
managed install. Later runs preserve that managed installation and resynchronize
plugins, while replacing supplied config files. Existing Oh My Zsh and custom
plugin checkouts are retained. A matching Node LTS, Java 21, compatible Kitty and tree-sitter CLI, installed
font, and matching functional upstream Neovim are retained, avoiding repeated
large downloads and source builds. An existing system Java 21 is reused when
available. Other tools and plugins can still be refreshed on reruns. Existing system links are backed up beside the original.

Your original repository configs are left unchanged. The deployed `.zshrc` gains
PATH/JAVA_HOME/Clang settings. On Fedora/Ubuntu it removes Arch-only package
maintenance aliases and uses the matching cleanup command. Older Clang versions
may reject `-std=c++26` or `-std=c23`: the installer probes accepted flags and
uses the newest supported spelling/standard in the deployed config, reporting
the selection. This does not add newer language features to an older compiler.

## Compatibility and recovery

Target Ubuntu 20.04+ on x86_64, Ubuntu 22.04+ on ARM64, and maintained Fedora/Arch
releases with working repositories. EOL distributions with disabled repositories
are not repaired automatically. Zed requires a supported Vulkan GPU/driver;
its [Linux requirements](https://zed.dev/docs/linux) currently specify glibc 2.31+
on x86_64 and 2.35+ on ARM64. Very old Ubuntu releases cannot be promised full
compatibility with current upstream software. University machines still need
sudo access and their administrative policies must permit installation.

Failures stop with the failing step and a nonzero exit status. Rerun after fixing
network/repository issues. If the prebuilt tree-sitter CLI cannot run, the
installer builds it using a minimal Rust toolchain in your account. A failed CLI
build or plugin synchronization stops installation. Mason language servers may
finish downloading on first use. Run `:LazyHealth` inside Neovim to inspect the
resulting environment.
Existing packages are not rolled back if a later step fails.

To undo the keyboard mapping, remove
`/etc/udev/hwdb.d/90-myeditorconfig-caps-escape.hwdb`, rebuild hwdb using
`sudo systemd-hwdb update` (older Ubuntu: `sudo udevadm hwdb --update`), then
reboot. Restore desired config files from the timestamped backup directory.

## Validation

See [VALIDATION.md](VALIDATION.md) for the completed checks and their scope.

```sh
go test ./...
go vet ./...
go test -race ./...
sh -n get.sh install.sh
```

Tests cover distro detection and package selection, failed commands, download
checksums and permissions, embedded hidden configs, backups/reruns, symlink
preservation, sudo forwarding and exit codes, compiler flag fallback, tree-sitter
fallback, real hwdb parsing and queries, and release checksums/static linking.
A compiled binary's dry run makes no changes or downloads. Race tests require
a C compiler on the development machine; release binaries require none.

To run real installs and reruns in disposable rootless Podman containers:

```sh
BOOTSTRAP_INTEGRATION=1 go test -v -run TestContainerInstall -timeout 60m
BOOTSTRAP_LIVE_METADATA=1 go test -v -run TestLiveReleaseMetadata
```

The matrix uses Ubuntu 20.04, Ubuntu 24.04, Fedora 44 and Arch x86_64. It installs
real packages and upstream tools, compiles and runs C/C++/Java using your shell
helpers, checks the Nerd Font and Kitty config, checks Zed's shared libraries,
and verifies backups after rerunning. This downloads several GB and can take
many minutes. Logs are saved in `/tmp/myeditorconfig-<distro>-*.log`. The tests
do not mount host directories/devices or change your desktop configuration.
Physical key behavior, GPU rendering, and ARM64 execution need real hardware or
VM tests; the container matrix cannot verify them.

Upstream references: [Neovim installation](https://neovim.io/doc/install/),
[LazyVim installation](https://www.lazyvim.org/installation),
[Kitty installation](https://sw.kovidgoyal.net/kitty/binary/),
[Zed installation](https://zed.dev/docs/linux),
[Nerd Fonts releases](https://github.com/ryanoasis/nerd-fonts/releases).
