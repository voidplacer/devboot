# Validation results — 2026-10-25

The checks below cover the development of the Codex-generated installer.
Release binaries use Go 1.27.3, `CGO_ENABLED=0`, `-trimpath` and
`-ldflags='-s -w'`. See the devboot release verification section for the
checks after the final download and speed improvements.

## Earlier compatibility regression

| Linux x86_64 system | Final install, functional checks, rerun and backup checks |
| --- | --- |
| Ubuntu 20.04 | PASS |
| Ubuntu 24.04 | PASS |
| Fedora 44 | PASS |
| Arch Linux | PASS |

Testing began with official minimal container images and real package managers.
Those clean-system runs exposed compatibility problems that were fixed. The
final regression reused the disposable distro fixtures and ran the corrected
installer twice per distro, with functional checks between runs and an original
configuration backup check afterward. The final matrix finished successfully in
416.7 seconds. No host directories or input devices were mounted into the test
containers. All test containers were removed after validation.

The functional checks exercised:

- Your `cout`, `gpp` and `jvc` helpers compiling and running C, C++ and Java.
- Neovim's minimum version/LuaJIT, LazyVim startup, supplied Lua config files,
  and plugin synchronization with explicit failure detection.
- An executable tree-sitter CLI, Node LTS and Java 21.
- The exact CaskaydiaMono Nerd Font family through Fontconfig.
- Kitty loading your full configuration through its Python config parser.
- Zed loading its executable and resolving its shared libraries.
- The Zsh login shell, installed config paths, compiled hwdb rules and backup
  preservation across repeated installs.

Neovim's latest stable binary failed its glibc check on Ubuntu 20.04; its source
build succeeded. The latest tree-sitter prebuilt CLI also failed there; the
Rust source build succeeded and executed correctly. Kitty 0.42.2 was verified
with your complete config and is selected automatically when current Kitty
cannot run. Ubuntu 20.04's Clang accepted `c++20` and `c2x`; the deployed shell
configuration selected these flags and compiled both test programs successfully.

## Other checks

| Check | Result |
| --- | --- |
| `go test -v -count=1 ./...` | PASS |
| `go test -race -count=1 ./...` | PASS |
| `go vet ./...` | PASS |
| `sh -n get.sh install.sh` | PASS |
| Live release-page discovery and SHA-256 metadata for all five GitHub upstreams | PASS |
| SHA-256 verification for both release binaries | PASS |
| x86_64 and ARM64 ELF architecture and absence of dynamic loader/dependencies | PASS |
| x86_64 binary dry run outside the repository, including an empty environment | PASS |
| AT, USB and Bluetooth hwdb parsing/query checks in an isolated filesystem | PASS |
| Sudo argument forwarding and nonzero exit propagation using a mock sudo | PASS |

Regression tests also cover repeated backup preservation, external symlink
targets, staging permissions under restrictive file permissions, corrupted
downloads, missing release assets, compiler-standard fallback, user process
environments, package-manager failure handling, compatible CLI retention and
Kitty's glibc fallback.

The checks found and fixed missing Zed ALSA dependencies on Fedora/Arch,
GitHub's shared-IP API quota failure, incompatible Kitty and tree-sitter
binaries on older Ubuntu, fragile backup collisions and staging permissions,
and silent Neovim/plugin failure reporting. Release discovery now uses GitHub's
release pages while retaining published SHA-256 verification.

## Reproduction and remaining hardware checks

See [README.md](README.md) for commands to build releases, run unit/race checks,
verify live upstream metadata, and run the fresh-container integration matrix.
The final regression additionally used
`BOOTSTRAP_TEST_REUSE_PREFIX=myeditorconfig-test-` with the existing disposable
fixtures. That optional mode replaces the fixture user's test configurations;
it is intended only for test containers.

The ARM64 binary was cross-compiled and inspected; execution needs an ARM64
machine. GPU window rendering and physical Caps Lock/Escape behavior need a
desktop or VM with the relevant hardware. Containers share the host kernel,
so the matrix validates distro userlands rather than each distro's own kernel.

Detailed logs from this session are retained in the ignored `.validation/`
directory and the `/tmp/myeditorconfig-<distro>-*.log` paths recorded in its
integration logs. Release hashes are in `dist/SHA256SUMS`.

## devboot v1.0.0 release verification

After adding parallel setup, Ubuntu package/download reductions, runtime reuse,
the standalone release downloader and explicit config embedding, a new matrix
started from official minimal images on all four distributions. Each completed
a real install, the functional checks above, a rerun and a preserved-original
backup check. All four passed. The final release binary was staged for the
reruns after a small concurrent-directory creation fix; that fix also has a
concurrent regression test. Fresh installs exercised the optimized installer;
the final release binary exercised the existing-install path.

The release also passed:

- Unit tests, race tests and `go vet` from the dedicated `devboot` source tree.
- Static ELF architecture/dependency and SHA-256 checks for both release binaries.
- Live release/digest discovery for Neovim, eza, zoxide, fzf and Nerd Fonts.
- Shell syntax checks for `get.sh` and `install.sh`.
- Downloader argument forwarding, checksum rejection, cleanup and working-directory
  preservation; the interactive Zsh handoff was checked in a pseudo-terminal
  with harmless substitute installer/Zsh commands.
- The x86_64 binary's dry run from outside the repository with an empty environment.

Ubuntu 20.04 performed fresh successful Neovim and tree-sitter source builds.
Ubuntu 24.04 used the current upstream Neovim binary. The actual release
binaries are `devboot-linux-amd64` and `devboot-linux-arm64`; `SHA256SUMS` also
covers the release's `get.sh`. No Go installation is needed to execute them.
The fresh-install test matrix is validation of functionality, not a five-minute
performance guarantee. Remaining ARM64, physical-key and GPU limits described
above still apply. All disposable test containers were removed.
