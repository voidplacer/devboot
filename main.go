// Bootstrap installs the editor environment. Build with CGO_ENABLED=0 go build .
package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hidden files (notably .zshrc and .clang-format) must be embedded too.
//
//go:embed shell/.zshrc shell/kitty.conf shell/tmux.conf zed/*.json temp_neovim/.clang-format temp_neovim/*.lua temp_neovim/LazyExtras.txt
var configs embed.FS

type installer struct {
	u                         *user.User
	distro, arch, tmp, backup string
	uid, gid                  int
	dry                       bool
	warnings                  []string
	command                   func(string, ...string) error
	metadata                  func(string) ([]byte, string, error)
	output                    func(string, ...string) ([]byte, error)
}

func main() {
	dry := flag.Bool("dry-run", false, "print the installation plan without changes or downloads")
	target := flag.String("user", "", "target account (defaults to SUDO_USER; required for direct root login)")
	flag.Parse()
	if !*dry && os.Geteuid() != 0 {
		binary, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Cannot locate this executable:", err)
			os.Exit(1)
		}
		fmt.Println("Administrator privileges are required; requesting sudo access.")
		cmd := exec.Command("sudo", append([]string{"--", binary}, os.Args[1:]...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err = cmd.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				os.Exit(exit.ExitCode())
			}
			fmt.Fprintln(os.Stderr, "Cannot elevate privileges:", err, "(alternatively, run as root with --user ACCOUNT)")
			os.Exit(1)
		}
		return
	}
	if err := start(*target, *dry); err != nil {
		fmt.Fprintln(os.Stderr, "Installation failed:", err)
		os.Exit(1)
	}
}

func start(name string, dry bool) error {
	if runtime.GOOS != "linux" {
		return errors.New("this installer requires Linux")
	}
	if name == "" {
		name = os.Getenv("SUDO_USER")
	}
	if name == "" && os.Geteuid() != 0 {
		current, err := user.LookupId(strconv.Itoa(os.Getuid()))
		if err != nil {
			return fmt.Errorf("current account: %w", err)
		}
		name = current.Username
	}
	u, err := user.Lookup(name)
	if err != nil {
		return fmt.Errorf("target account: %w (use sudo, or --user ACCOUNT)", err)
	}
	if u.Uid == "0" || !filepath.IsAbs(u.HomeDir) || u.HomeDir == "/" {
		return errors.New("choose a non-root user with an absolute home directory")
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return err
	}
	distro, err := detectDistro(string(data))
	if err != nil {
		return err
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" {
		return errors.New("only x86_64 and ARM64 are supported")
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid <= 0 {
		return fmt.Errorf("invalid target UID %q", u.Uid)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil || gid < 0 {
		return fmt.Errorf("invalid target GID %q", u.Gid)
	}
	i := &installer{u: u, distro: distro, arch: arch, uid: uid, gid: gid, dry: dry}
	fmt.Printf("Target: %s (%s), distro: %s, architecture: %s\n", u.Username, u.HomeDir, distro, arch)
	if dry {
		fmt.Println("Plan: install system dependencies; stable Neovim (Ubuntu: upstream binary, source fallback); Kitty; Zed; Oh My Zsh and plugins; zoxide/eza/fzf; Node LTS and Java 21; CaskaydiaMono Nerd Font; LazyVim with C/C++ and Java extras; copy configs with backups; set login shell; map Caps Lock to Escape system-wide using udev hwdb. Arch performs a full system upgrade. No changes made.")
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("run with sudo; use --dry-run to preview without root")
	}
	i.tmp, err = os.MkdirTemp("", "myeditorconfig-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(i.tmp)
	// User processes need access to staged downloads, never to root's environment.
	if err = os.Chmod(i.tmp, 0755); err != nil {
		return err
	}
	i.backup = filepath.Join(u.HomeDir, ".local/state/myeditorconfig/backups", time.Now().UTC().Format("20060102T150405.000000000Z"))
	fmt.Println("\n==> System packages")
	if err = i.packages(); err != nil {
		return fmt.Errorf("System packages: %w", err)
	}
	for _, p := range []string{".config", ".cache", ".local/bin", ".local/share", ".local/state"} {
		if err = i.mkdir(filepath.Join(u.HomeDir, p)); err != nil {
			return err
		}
	}
	if err = os.MkdirAll("/usr/local/bin", 0755); err != nil {
		return err
	}
	// Network-bound setup overlaps; the two potentially heavy source builds
	// remain in one group so older computers do not compile both at once.
	if err = parallelSetup([]setupStep{
		{"Neovim and runtimes", func() error {
			if err := i.neovim(); err != nil {
				return err
			}
			return i.runtimes()
		}},
		{"Shell tools", i.shellTools}, {"Oh My Zsh", i.ohmyzsh}, {"Nerd Font", i.font}, {"Kitty and Zed", i.editors},
	}); err != nil {
		return err
	}
	steps := []struct {
		name string
		run  func() error
	}{
		{"Configurations and LazyVim", i.configure},
		{"Caps Lock to Escape", i.keyboard}, {"Default shell", i.loginShell},
	}
	for _, step := range steps {
		fmt.Println("\n==>", step.name)
		if err = step.run(); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	fmt.Printf("\nInstalled. Backups: %s\nLog out and back in for your Zsh login shell. Reboot for the keyboard mapping on all attached devices.\n", i.backup)
	for _, w := range i.warnings {
		fmt.Println("NOTICE:", w)
	}
	return nil
}

type setupStep struct {
	name string
	run  func() error
}

func parallelSetup(steps []setupStep) error {
	var wg sync.WaitGroup
	errs := make([]error, len(steps))
	for n, step := range steps {
		wg.Add(1)
		go func(n int, step setupStep) {
			defer wg.Done()
			fmt.Println("\n==>", step.name)
			if err := step.run(); err != nil {
				errs[n] = fmt.Errorf("%s: %w", step.name, err)
			}
		}(n, step)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func detectDistro(data string) (string, error) {
	v := map[string]string{}
	for _, line := range strings.Split(data, "\n") {
		k, x, ok := strings.Cut(line, "=")
		if ok {
			v[k] = strings.Trim(x, "\"'")
		}
	}
	for _, id := range append([]string{v["ID"]}, strings.Fields(v["ID_LIKE"])...) {
		switch id {
		case "ubuntu":
			return "ubuntu", nil
		case "fedora":
			return "fedora", nil
		case "arch":
			return "arch", nil
		}
	}
	return "", fmt.Errorf("unsupported distribution %q; expected Ubuntu, Fedora or Arch", v["ID"])
}

func run(name string, args ...string) error {
	fmt.Printf("  %s %s\n", name, strings.Join(args, " "))
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = nil
	return c.Run()
}

func (i *installer) run(name string, args ...string) error {
	if i.command != nil {
		return i.command(name, args...)
	}
	return run(name, args...)
}

func (i *installer) userRun(name string, args ...string) error {
	return i.run("runuser", i.userArgs(name, args...)...)
}

func (i *installer) userArgs(name string, args ...string) []string {
	env := []string{"-u", i.u.Username, "--", "env", "-i", "--chdir=" + i.u.HomeDir, "HOME=" + i.u.HomeDir, "USER=" + i.u.Username, "LOGNAME=" + i.u.Username, "SHELL=/bin/zsh", "LANG=C.UTF-8",
		"PATH=" + i.u.HomeDir + "/.local/bin:/usr/local/bin:/usr/bin:/bin", "JAVA_HOME=" + i.u.HomeDir + "/.local/share/myeditorconfig/java", "CC=clang", "CXX=clang++"}
	return append(append(env, name), args...)
}

func (i *installer) commandOutput(name string, args ...string) ([]byte, error) {
	if i.output != nil {
		return i.output(name, args...)
	}
	return exec.Command(name, args...).Output()
}

func (i *installer) warn(err error) {
	if err != nil {
		i.warnings = append(i.warnings, err.Error())
		fmt.Println("  NOTICE:", err)
	}
}

func (i *installer) packages() error {
	common := []string{"zsh", "git", "curl", "wget", "unzip", "tar", "fontconfig", "ripgrep", "tmux", "xdg-utils"}
	switch i.distro {
	case "ubuntu":
		if err := i.run("apt-get", "update"); err != nil {
			return err
		}
		// Default Ubuntu desktops already have Universe. Avoid installing its
		// management tools and refreshing apt twice when it is already usable.
		if _, err := i.commandOutput("apt-cache", "show", "fd-find"); err != nil {
			if err = i.aptInstall("software-properties-common", "ca-certificates"); err != nil {
				return err
			}
			if err = i.run("add-apt-repository", "-y", "universe"); err != nil {
				return err
			}
			if err = i.run("apt-get", "update"); err != nil {
				return err
			}
		}
		p := append(common, "ca-certificates", "build-essential", "clang", "clangd", "clang-format", "fd-find", "python3", "xz-utils", "util-linux", "udev", "libvulkan1", "libegl1", "libgl1", "libxkbcommon0", "libxkbcommon-x11-0", "libxcb-cursor0", "libasound2-dev", "libglib2.0-bin", "libdbus-1-3", "libx11-xcb1", "libxcursor1", "libxrandr2", "libxi6", "libxinerama1")
		return i.aptInstall(p...)
	case "fedora":
		p := append(common, "kitty", "ca-certificates", "gcc-c++", "clang", "clang-tools-extra", "fd-find", "cmake", "ninja-build", "gettext", "libtool", "autoconf", "automake", "pkgconf-pkg-config", "openssl-devel", "python3", "xz", "util-linux", "systemd-udev", "vulkan-loader", "mesa-vulkan-drivers", "libxkbcommon", "libxcb", "alsa-lib", "glib2")
		return i.run("dnf", append([]string{"install", "-y"}, p...)...)
	default:
		p := append(common, "kitty", "ca-certificates", "base-devel", "clang", "neovim", "fd", "cmake", "ninja", "gettext", "python", "xz", "util-linux", "systemd", "vulkan-icd-loader", "libxkbcommon", "libxcb", "alsa-lib", "glib2")
		return i.run("pacman", append([]string{"-Syu", "--needed", "--noconfirm"}, p...)...)
	}
}

func (i *installer) aptInstall(packages ...string) error {
	return i.run("env", append([]string{"DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=a", "apt-get", "install", "-y", "--no-install-recommends"}, packages...)...)
}

func (i *installer) fetch(url, name string) (string, error) {
	if name == "." || name == "" || filepath.Base(name) != name {
		return "", errors.New("invalid download filename")
	}
	p := filepath.Join(i.tmp, name)
	err := i.run("curl", "--fail", "--location", "--retry", "3", "--connect-timeout", "30", "--max-time", "900", "--proto", "=https", "--proto-redir", "=https", "--output", p, url)
	if err == nil {
		err = os.Chmod(p, 0644)
	}
	return p, err
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func (i *installer) latest(repo string) (release, error) {
	// Release pages avoid the public API's shared-IP quota. The asset panels
	// publish the same per-file SHA-256 digests as the API.
	get := i.metadata
	if get == nil {
		get = getMetadata
	}
	_, final, err := get("https://github.com/" + repo + "/releases/latest")
	if err != nil {
		return release{}, err
	}
	u, err := url.Parse(final)
	if err != nil {
		return release{}, err
	}
	tag, ok := strings.CutPrefix(u.Path, "/"+repo+"/releases/tag/")
	if u.Host != "github.com" || !ok || tag == "" {
		return release{}, fmt.Errorf("unexpected release redirect %q", final)
	}
	b, _, err := get("https://github.com/" + repo + "/releases/expanded_assets/" + url.PathEscape(tag))
	if err != nil {
		return release{}, err
	}
	return parseReleaseAssets(repo, tag, string(b))
}

func getMetadata(address string) ([]byte, string, error) {
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("unsafe or excessive metadata redirects")
		}
		return nil
	}}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		fmt.Println("  Read release metadata:", address)
		resp, err := client.Get(address)
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			last = fmt.Errorf("release metadata returned HTTP %d: %s", resp.StatusCode, address)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		return b, resp.Request.URL.String(), nil
	}
	return nil, "", last
}

func parseReleaseAssets(repo, tag, body string) (release, error) {
	r := release{Tag: tag}
	rows := regexp.MustCompile(`(?s)<li\b[^>]*>(.*?)</li>`).FindAllStringSubmatch(body, -1)
	anchor := regexp.MustCompile(`href="([^"]+)"`)
	digest := regexp.MustCompile(`sha256:[0-9a-fA-F]{64}`)
	prefix := "/" + repo + "/releases/download/" + tag + "/"
	for _, row := range rows {
		for _, a := range anchor.FindAllStringSubmatch(row[1], -1) {
			u, err := url.Parse(html.UnescapeString(a[1]))
			if err != nil || u.IsAbs() || u.Host != "" {
				continue
			}
			name, ok := strings.CutPrefix(u.Path, prefix)
			if !ok || name == "" || filepath.Base(name) != name {
				continue
			}
			r.Assets = append(r.Assets, struct {
				Name   string `json:"name"`
				URL    string `json:"browser_download_url"`
				Digest string `json:"digest"`
			}{name, "https://github.com" + u.EscapedPath(), strings.ToLower(digest.FindString(row[1]))})
		}
	}
	if len(r.Assets) == 0 {
		return r, fmt.Errorf("no assets on GitHub release page for %s %s", repo, tag)
	}
	return r, nil
}

func (i *installer) asset(r release, match func(string) bool) (string, error) {
	for _, a := range r.Assets {
		if match(a.Name) {
			p, err := i.fetch(a.URL, a.Name)
			if err != nil {
				return "", err
			}
			if strings.HasPrefix(a.Digest, "sha256:") {
				f, err := os.Open(p)
				if err != nil {
					return "", err
				}
				h := sha256.New()
				_, err = io.Copy(h, f)
				f.Close()
				if err != nil {
					return "", err
				}
				if hex.EncodeToString(h.Sum(nil)) != strings.TrimPrefix(a.Digest, "sha256:") {
					return "", errors.New("release asset checksum mismatch")
				}
			}
			return p, nil
		}
	}
	return "", errors.New("no compatible release asset found")
}

func (i *installer) mkdir(p string) error {
	// Chown only newly created directories, never the user's whole home tree.
	if _, err := os.Lstat(p); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := i.mkdir(filepath.Dir(p)); err != nil {
		return err
	}
	if err := os.Mkdir(p, 0755); err != nil {
		// Independent setup jobs can create the same parent concurrently.
		if os.IsExist(err) {
			info, statErr := os.Stat(p)
			if statErr == nil && info.IsDir() {
				return nil
			}
		}
		return err
	}
	if strings.HasPrefix(p, i.u.HomeDir+string(filepath.Separator)) {
		return os.Chown(p, i.uid, i.gid)
	}
	return nil
}

func (i *installer) save(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	rel := strings.TrimPrefix(path, string(filepath.Separator))
	dst := filepath.Join(i.backup, rel)
	if !strings.HasPrefix(path, i.u.HomeDir+string(filepath.Separator)) {
		dst = path + ".myeditorconfig-backup-" + filepath.Base(i.backup)
	}
	if err := i.mkdir(filepath.Dir(dst)); err != nil {
		return err
	}
	for suffix := 1; ; suffix++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			break
		} else if err != nil {
			return err
		}
		dst = filepath.Join(filepath.Dir(dst), filepath.Base(path)+".backup-"+strconv.Itoa(suffix))
	}
	// The destination lives on the target home filesystem for user configs.
	if err := os.Rename(path, dst); err != nil {
		return fmt.Errorf("back up %s: %w", path, err)
	}
	return nil
}

func (i *installer) write(path string, b []byte) error {
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, b) {
		return nil
	}
	if err = i.mkdir(filepath.Dir(path)); err != nil {
		return err
	}
	if err = i.save(path); err != nil {
		return err
	}
	if err = os.WriteFile(path, b, 0644); err != nil {
		return err
	}
	return os.Chown(path, i.uid, i.gid)
}

func (i *installer) copyConfig(src, dst string) error {
	b, err := configs.ReadFile(src)
	if err != nil {
		return err
	}
	return i.write(filepath.Join(i.u.HomeDir, dst), b)
}

func (i *installer) link(src, dst string) error {
	if old, err := os.Readlink(dst); err == nil && old == src {
		return nil
	}
	if err := i.mkdir(filepath.Dir(dst)); err != nil {
		return err
	}
	if err := i.save(dst); err != nil {
		return err
	}
	return os.Symlink(src, dst)
}

func (i *installer) neovim() error {
	if i.distro == "arch" {
		if err := i.checkNeovim("/usr/bin/nvim"); err == nil {
			return nil
		}
		fmt.Println("Distribution Neovim is too old for LazyVim; using upstream stable.")
	}
	if i.distro == "fedora" {
		if err := i.run("dnf", "install", "-y", "neovim"); err != nil {
			return err
		}
		if err := i.checkNeovim("/usr/bin/nvim"); err == nil {
			return nil
		}
		fmt.Println("Distribution Neovim is too old for LazyVim; using upstream stable.")
	}
	r, err := i.latest("neovim/neovim")
	if err != nil {
		return err
	}
	cached := "/opt/myeditorconfig/nvim"
	if b, err := i.commandOutput(cached+"/bin/nvim", "--version"); err == nil && strings.HasPrefix(string(b), "NVIM "+r.Tag+"\n") {
		if _, err = os.Stat(cached + "/share/nvim/runtime/doc/help.txt"); err == nil {
			if err = i.checkNeovim(cached + "/bin/nvim"); err == nil {
				fmt.Println("Upstream Neovim", r.Tag, "is already installed and functional.")
				return i.link(cached+"/bin/nvim", "/usr/local/bin/nvim")
			}
		}
	}
	a := i.arch
	if a == "aarch64" {
		a = "arm64"
	}
	p, err := i.asset(r, func(s string) bool { return s == "nvim-linux-"+a+".tar.gz" })
	if err != nil {
		return err
	}
	d := filepath.Join(i.tmp, "nvim")
	if err = os.Mkdir(d, 0755); err != nil {
		return err
	}
	if err = os.Chmod(d, 0755); err != nil {
		return err
	}
	if err = i.run("tar", "-xzf", p, "-C", d, "--strip-components=1"); err != nil {
		return err
	}
	if err = i.run(filepath.Join(d, "bin/nvim"), "--version"); err != nil {
		fmt.Println("Upstream binary cannot run here; building the same stable release from source.")
		if i.distro == "ubuntu" {
			if err = i.aptInstall("ninja-build", "cmake", "gettext", "libtool", "autoconf", "automake", "pkg-config", "libssl-dev", "python3-venv"); err != nil {
				return err
			}
		}
		src, err := i.fetch("https://github.com/neovim/neovim/archive/refs/tags/"+r.Tag+".tar.gz", "nvim-source.tar.gz")
		if err != nil {
			return err
		}
		build := filepath.Join(i.tmp, "nvim-source")
		if err = os.Mkdir(build, 0777); err != nil {
			return err
		}
		if err = os.Chown(build, i.uid, i.gid); err != nil {
			return err
		}
		if err = i.userRun("tar", "-xzf", src, "-C", build, "--strip-components=1"); err != nil {
			return err
		}
		venv := filepath.Join(build, ".build-tools")
		if err = i.userRun("python3", "-m", "venv", venv); err != nil {
			return err
		}
		if err = i.userRun(filepath.Join(venv, "bin/pip"), "install", "cmake"); err != nil {
			return err
		}
		jobs := runtime.NumCPU()
		if jobs > 4 {
			jobs = 4
		}
		if err = i.userRun("make", "-C", build, "-j"+strconv.Itoa(jobs), "CMAKE_BUILD_TYPE=Release", "CMAKE_INSTALL_PREFIX="+build+"/install", "CMAKE_PRG="+venv+"/bin/cmake"); err != nil {
			return err
		}
		if err = i.userRun("make", "-C", build, "install", "CMAKE_PRG="+venv+"/bin/cmake"); err != nil {
			return err
		}
		d = filepath.Join(build, "install")
	}
	// Stage, then switch only after the replacement has been validated.
	dst := "/opt/myeditorconfig/nvim"
	if err = os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err = os.RemoveAll(dst + ".new"); err != nil {
		return err
	}
	if err = i.run("cp", "-a", d, dst+".new"); err != nil {
		return err
	}
	if err = i.run("chown", "-R", "root:root", dst+".new"); err != nil {
		return err
	}
	if err = os.RemoveAll(dst); err != nil {
		return err
	}
	if err = os.Rename(dst+".new", dst); err != nil {
		return err
	}
	if err = i.link(dst+"/bin/nvim", "/usr/local/bin/nvim"); err != nil {
		return err
	}
	return i.run("/usr/local/bin/nvim", "--version")
}

func (i *installer) checkNeovim(path string) error {
	return i.run(path, "--headless", "-u", "NONE", "-i", "NONE", "--cmd", `lua if vim.fn.has("nvim-0.11.2") ~= 1 or jit == nil then vim.cmd("cquit 1") end`, "+qa")
}

func (i *installer) installTool(repo, tool, marker string) error {
	r, err := i.latest(repo)
	if err != nil {
		return err
	}
	p, err := i.asset(r, func(s string) bool { return strings.Contains(s, marker) && strings.HasSuffix(s, ".tar.gz") })
	if err != nil {
		return err
	}
	d := filepath.Join(i.tmp, tool+"-unpack")
	if err = os.Mkdir(d, 0755); err != nil {
		return err
	}
	if err = i.run("tar", "-xzf", p, "-C", d); err != nil {
		return err
	}
	var binary string
	err = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Name() == tool && !e.IsDir() {
			binary = p
		}
		return nil
	})
	if err != nil {
		return err
	}
	if binary == "" {
		return fmt.Errorf("%s missing from archive", tool)
	}
	if err = i.run(binary, "--version"); err != nil {
		return fmt.Errorf("downloaded %s cannot run: %w", tool, err)
	}
	if err = i.save("/usr/local/bin/" + tool); err != nil {
		return err
	}
	return i.run("install", "-m", "0755", binary, "/usr/local/bin/"+tool)
}

func (i *installer) shellTools() error {
	if err := os.MkdirAll("/usr/local/bin", 0755); err != nil {
		return err
	}
	for _, t := range []struct{ repo, tool string }{{"eza-community/eza", "eza"}, {"ajeetdsouza/zoxide", "zoxide"}} {
		marker := i.arch + "-unknown-linux-musl"
		if t.tool == "eza" && i.arch == "aarch64" {
			marker = "aarch64-unknown-linux-gnu_no_libgit"
		}
		if err := i.installTool(t.repo, t.tool, marker); err != nil {
			return err
		}
		if err := i.run("/usr/local/bin/"+t.tool, "--version"); err != nil {
			return err
		}
	}
	a := "amd64"
	if i.arch == "aarch64" {
		a = "arm64"
	}
	if err := i.installTool("junegunn/fzf", "fzf", "linux_"+a); err != nil {
		return err
	}
	if i.distro == "ubuntu" {
		if err := i.link("/usr/bin/fdfind", "/usr/local/bin/fd"); err != nil {
			return err
		}
	}
	return nil
}

func (i *installer) runtimes() error {
	// Official Node LTS tarballs support older glibc than many distro packages.
	p, err := i.fetch("https://nodejs.org/dist/index.json", "node-index.json")
	if err != nil {
		return err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var versions []struct {
		Version string      `json:"version"`
		LTS     interface{} `json:"lts"`
	}
	if err = json.Unmarshal(b, &versions); err != nil {
		return err
	}
	version := ""
	for _, v := range versions {
		if s, ok := v.LTS.(string); ok && s != "" {
			version = v.Version
			break
		}
	}
	if version == "" {
		return errors.New("no Node LTS release found")
	}
	base := filepath.Join(i.u.HomeDir, ".local/share/myeditorconfig")
	if err = i.mkdir(base); err != nil {
		return err
	}
	nodeReady := false
	if out, e := i.commandOutput("runuser", i.userArgs(base+"/node/bin/node", "--version")...); e == nil && strings.TrimSpace(string(out)) == version {
		_, npmErr := os.Stat(base + "/node/bin/npm")
		_, npxErr := os.Stat(base + "/node/bin/npx")
		nodeReady = npmErr == nil && npxErr == nil
	}
	a := "x64"
	if i.arch == "aarch64" {
		a = "arm64"
	}
	file := "node-" + version + "-linux-" + a + ".tar.xz"
	if !nodeReady {
		p, err = i.fetch("https://nodejs.org/dist/"+version+"/"+file, file)
		if err != nil {
			return err
		}
		sums, err := i.fetch("https://nodejs.org/dist/"+version+"/SHASUMS256.txt", "node-sha256.txt")
		if err != nil {
			return err
		}
		checks, err := os.ReadFile(sums)
		if err != nil {
			return err
		}
		expected := ""
		for _, line := range strings.Split(string(checks), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[1] == file {
				expected = f[0]
			}
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if expected == "" || hex.EncodeToString(h.Sum(nil)) != expected {
			return errors.New("Node checksum mismatch")
		}
	}
	for _, name := range []string{"node", "java"} {
		archive := p
		dst := filepath.Join(base, name)
		ready := nodeReady
		if name == "java" {
			ready, err = i.reuseJava(dst)
			if err != nil {
				return err
			}
			if !ready {
				ja := "x64"
				if i.arch == "aarch64" {
					ja = "aarch64"
				}
				archive, err = i.fetch("https://api.adoptium.net/v3/binary/latest/21/ga/linux/"+ja+"/jdk/hotspot/normal/eclipse", "java.tar.gz")
				if err != nil {
					return err
				}
			}
		}
		if !ready {
			stage := dst + ".new"
			if err = os.RemoveAll(stage); err != nil {
				return err
			}
			if err = i.mkdir(stage); err != nil {
				return err
			}
			if err = i.userRun("tar", "-xf", archive, "-C", stage, "--strip-components=1"); err != nil {
				return err
			}
			if err = os.RemoveAll(dst); err != nil {
				return err
			}
			if err = os.Rename(stage, dst); err != nil {
				return err
			}
		}
		bins := []string{"node", "npm", "npx"}
		if name == "java" {
			bins = []string{"java", "javac"}
		}
		for _, bin := range bins {
			if err = i.link(dst+"/bin/"+bin, filepath.Join(i.u.HomeDir, ".local/bin", bin)); err != nil {
				return err
			}
		}
	}
	return i.treeSitter()
}

func java21Version(version string) bool {
	return regexp.MustCompile(`(?m)^javac 21(?:\.|\s|$)`).MatchString(strings.TrimSpace(version))
}

func (i *installer) reuseJava(dst string) (bool, error) {
	if b, err := i.commandOutput("runuser", i.userArgs(dst+"/bin/javac", "-version")...); err == nil && java21Version(string(b)) {
		if _, err = os.Stat(dst + "/bin/java"); err == nil {
			fmt.Println("Keeping installed Java 21.")
			return true, nil
		}
	}
	if b, err := i.commandOutput("/usr/bin/javac", "-version"); err == nil && java21Version(string(b)) {
		real, err := filepath.EvalSymlinks("/usr/bin/javac")
		if err != nil {
			return false, err
		}
		home := filepath.Dir(filepath.Dir(real))
		if _, err = os.Stat(home + "/bin/java"); err != nil {
			return false, nil
		}
		if err = i.link(home, dst); err != nil {
			return false, err
		}
		fmt.Println("Using system Java 21:", home)
		return true, nil
	}
	return false, nil
}

func (i *installer) treeSitter() error {
	if b, err := i.commandOutput("runuser", i.userArgs("tree-sitter", "--version")...); err == nil && compatibleTreeSitter(string(b)) {
		fmt.Println("Keeping compatible tree-sitter CLI:", strings.TrimSpace(string(b)))
		return nil
	}
	if err := i.userRun("npm", "install", "--global", "--prefix", filepath.Join(i.u.HomeDir, ".local"), "tree-sitter-cli"); err == nil {
		if err = i.userRun("tree-sitter", "--version"); err == nil {
			return nil
		}
	}
	fmt.Println("Tree-sitter's prebuilt CLI cannot run here; compiling it for this machine.")
	if i.distro == "ubuntu" {
		if err := i.aptInstall("pkg-config", "libssl-dev"); err != nil {
			return err
		}
	}
	cargo := filepath.Join(i.u.HomeDir, ".cargo/bin/cargo")
	if _, err := os.Stat(cargo); os.IsNotExist(err) {
		p, err := i.fetch("https://sh.rustup.rs", "rustup.sh")
		if err != nil {
			return err
		}
		if err = i.userRun("sh", p, "-y", "--profile", "minimal", "--default-toolchain", "stable", "--no-modify-path"); err != nil {
			return err
		}
	}
	if err := i.userRun(cargo, "install", "--locked", "--force", "--jobs", "4", "--root", filepath.Join(i.u.HomeDir, ".local"), "tree-sitter-cli"); err != nil {
		return err
	}
	return i.userRun("tree-sitter", "--version")
}

func compatibleTreeSitter(version string) bool {
	m := regexp.MustCompile(`^tree-sitter ([0-9]+)\.([0-9]+)\.([0-9]+)`).FindStringSubmatch(strings.TrimSpace(version))
	if len(m) != 4 {
		return false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	c, _ := strconv.Atoi(m[3])
	return a > 0 || b > 26 || (b == 26 && c >= 1)
}

func (i *installer) clone(repo, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := i.mkdir(filepath.Dir(dst)); err != nil {
		return err
	}
	return i.userRun("git", "clone", "--depth=1", "https://github.com/"+repo+".git", dst)
}

func (i *installer) ohmyzsh() error {
	base := filepath.Join(i.u.HomeDir, ".oh-my-zsh")
	if err := i.clone("ohmyzsh/ohmyzsh", base); err != nil {
		return err
	}
	for _, p := range []string{"zsh-autosuggestions", "zsh-syntax-highlighting"} {
		if err := i.clone("zsh-users/"+p, base+"/custom/plugins/"+p); err != nil {
			return err
		}
	}
	return nil
}

func (i *installer) font() error {
	if b, err := i.commandOutput("runuser", i.userArgs("fc-match", "-f", "%{family}", "CaskaydiaMono Nerd Font")...); err == nil {
		for _, family := range strings.Split(string(b), ",") {
			if strings.TrimSpace(family) == "CaskaydiaMono Nerd Font" {
				fmt.Println("CaskaydiaMono Nerd Font is already installed.")
				return nil
			}
		}
	}
	r, err := i.latest("ryanoasis/nerd-fonts")
	if err != nil {
		return err
	}
	p, err := i.asset(r, func(s string) bool { return s == "CascadiaMono.tar.xz" })
	if err != nil {
		return err
	}
	dst := filepath.Join(i.u.HomeDir, ".local/share/fonts/CaskaydiaMono")
	if err = i.mkdir(dst); err != nil {
		return err
	}
	if err = i.userRun("tar", "-xf", p, "-C", dst); err != nil {
		return err
	}
	return i.userRun("fc-cache", "-f")
}

func (i *installer) editors() error {
	// Use Kitty's current official build on Ubuntu: old packages predate some supplied settings.
	if i.distro == "ubuntu" {
		p, err := i.fetch("https://sw.kovidgoyal.net/kitty/installer.sh", "kitty-installer.sh")
		if err != nil {
			return err
		}
		if err = i.installKitty(p); err != nil {
			return err
		}
		if err = i.link(filepath.Join(i.u.HomeDir, ".local/kitty.app/bin/kitty"), filepath.Join(i.u.HomeDir, ".local/bin/kitty")); err != nil {
			return err
		}
		if err = i.link(filepath.Join(i.u.HomeDir, ".local/kitty.app/bin/kitten"), filepath.Join(i.u.HomeDir, ".local/bin/kitten")); err != nil {
			return err
		}
		desktop := "[Desktop Entry]\nType=Application\nName=Kitty\nExec=\"" + i.u.HomeDir + "/.local/kitty.app/bin/kitty\"\nIcon=" + i.u.HomeDir + "/.local/kitty.app/share/icons/hicolor/256x256/apps/kitty.png\nCategories=System;TerminalEmulator;\nTerminal=false\n"
		if err = i.write(filepath.Join(i.u.HomeDir, ".local/share/applications/kitty.desktop"), []byte(desktop)); err != nil {
			return err
		}
	}
	p, err := i.fetch("https://zed.dev/install.sh", "zed-installer.sh")
	if err != nil {
		return err
	}
	if err = i.userRun("sh", p); err != nil {
		return err
	}
	return i.userRun(filepath.Join(i.u.HomeDir, ".local/zed.app/libexec/zed-editor"), "--help")
}

func (i *installer) installKitty(script string) error {
	kitty := filepath.Join(i.u.HomeDir, ".local/kitty.app/bin/kitty")
	if b, err := i.commandOutput("runuser", i.userArgs(kitty, "--version")...); err == nil && compatibleKitty(string(b)) {
		fmt.Println("Keeping installed Kitty:", strings.TrimSpace(string(b)))
		return nil
	}
	if b, err := i.commandOutput("getconf", "GNU_LIBC_VERSION"); err == nil && needsCompatibleKitty(string(b)) {
		fmt.Println("Using Kitty 0.42.2 for this older glibc, avoiding an incompatible download.")
		if err = i.userRun("sh", script, "launch=n", "installer=version-0.42.2"); err != nil {
			return err
		}
		return i.userRun(filepath.Join(i.u.HomeDir, ".local/kitty.app/bin/kitty"), "--version")
	}
	if err := i.userRun("sh", script, "launch=n"); err != nil {
		return err
	}
	if err := i.userRun(kitty, "--version"); err == nil {
		return nil
	}
	// Current upstream binaries require newer glibc than Ubuntu 20.04. This
	// official release is verified with the supplied config on glibc 2.31.
	fmt.Println("Current Kitty cannot run on this host; installing compatible Kitty 0.42.2.")
	if err := i.userRun("sh", script, "launch=n", "installer=version-0.42.2"); err != nil {
		return err
	}
	return i.userRun(kitty, "--version")
}

func compatibleKitty(version string) bool {
	var major, minor, patch int
	if _, err := fmt.Sscanf(strings.TrimSpace(version), "kitty %d.%d.%d", &major, &minor, &patch); err != nil {
		return false
	}
	return major > 0 || minor > 35 || (minor == 35 && patch >= 2)
}

func needsCompatibleKitty(version string) bool {
	var major, minor int
	if _, err := fmt.Sscanf(strings.TrimSpace(version), "glibc %d.%d", &major, &minor); err != nil {
		return false
	}
	return major < 2 || (major == 2 && minor < 35)
}

func (i *installer) configure() error {
	for src, dst := range map[string]string{"shell/kitty.conf": ".config/kitty/kitty.conf", "zed/keymap.json": ".config/zed/keymap.json", "shell/tmux.conf": ".tmux.conf", "temp_neovim/.clang-format": ".clang-format"} {
		if err := i.copyConfig(src, dst); err != nil {
			return err
		}
	}
	zed, err := configs.ReadFile("zed/settings.json")
	if err != nil {
		return err
	}
	// Zed accepts JSONC. Insert into the outer object without rewriting comments.
	zed = bytes.Replace(zed, []byte("{"), []byte("{\n  \"auto_install_extensions\": {\"catppuccin\": true, \"catppuccin-icons\": true},"), 1)
	if err = i.write(filepath.Join(i.u.HomeDir, ".config/zed/settings.json"), zed); err != nil {
		return err
	}
	b, err := configs.ReadFile("shell/.zshrc")
	if err != nil {
		return err
	}
	// Ubuntu LTS Clang may use the draft names for the user's requested standards.
	cpp, c := supportedStandard("clang++", []string{"c++26", "c++2c", "c++23", "c++2b", "c++20"}, "c++"), supportedStandard("clang", []string{"c23", "c2x", "c17"}, "c")
	if cpp == "" || c == "" {
		return errors.New("Clang cannot compile C and C++")
	}
	s := strings.ReplaceAll(strings.ReplaceAll(string(b), "-std=c++26", "-std="+cpp), "-std=c23", "-std="+c)
	if cpp != "c++26" || c != "c23" {
		i.warn(fmt.Errorf("this Clang uses %s / %s; the installed .zshrc uses those supported flags. Repository original preserved", cpp, c))
	}
	s = "export PATH=\"$HOME/.local/bin:/usr/local/bin:$PATH\"\nexport JAVA_HOME=\"$HOME/.local/share/myeditorconfig/java\"\nexport CC=clang\nexport CXX=clang++\n" + s
	// Replace Arch-only maintenance aliases in the deployed copy on other distros.
	if i.distro != "arch" {
		re := regexp.MustCompile(`(?m)^alias (cleanup|rip|grubup|fixpacman|big|gitpkg)=.*\n?`)
		s = re.ReplaceAllString(s, "")
		if i.distro == "ubuntu" {
			s += "\nalias cleanup='sudo apt-get autoremove'\n"
		} else {
			s += "\nalias cleanup='sudo dnf autoremove'\n"
		}
	}
	if err = i.write(filepath.Join(i.u.HomeDir, ".zshrc"), []byte(s)); err != nil {
		return err
	}
	nvim := filepath.Join(i.u.HomeDir, ".config/nvim")
	marker := filepath.Join(nvim, ".myeditorconfig-managed")
	if _, err = os.Stat(marker); os.IsNotExist(err) {
		// Move config and plugin state together so an unrelated old install cannot contaminate LazyVim.
		for _, d := range []string{nvim, filepath.Join(i.u.HomeDir, ".local/share/nvim"), filepath.Join(i.u.HomeDir, ".local/state/nvim"), filepath.Join(i.u.HomeDir, ".cache/nvim")} {
			if err = i.save(d); err != nil {
				return err
			}
		}
		if err = i.clone("LazyVim/starter", nvim); err != nil {
			return err
		}
		if err = os.RemoveAll(filepath.Join(nvim, ".git")); err != nil {
			return err
		}
		if err = i.write(marker, []byte("Managed by myeditorconfig\n")); err != nil {
			return err
		}
	}
	for src, dst := range map[string]string{"temp_neovim/conform.lua": "lua/plugins/conform.lua", "temp_neovim/noice.lua": "lua/plugins/noice.lua", "temp_neovim/keymaps.lua": "lua/config/keymaps.lua"} {
		if err = i.copyConfig(src, ".config/nvim/"+dst); err != nil {
			return err
		}
	}
	extras, err := configs.ReadFile("temp_neovim/LazyExtras.txt")
	if err != nil {
		return err
	}
	var imports []string
	for _, x := range strings.Fields(string(extras)) {
		imports = append(imports, "lazyvim.plugins.extras."+x)
	}
	sort.Strings(imports)
	meta := struct {
		Extras  []string `json:"extras"`
		Version int      `json:"version"`
	}{imports, 8}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err = i.write(filepath.Join(nvim, "lazyvim.json"), append(data, '\n')); err != nil {
		return err
	}
	if err = i.userRun("zsh", "-n", filepath.Join(i.u.HomeDir, ".zshrc")); err != nil {
		return err
	}
	if err = i.userRun("nvim", "--headless", "+Lazy! sync", "+lua "+lazyValidationLua, "+qa"); err != nil {
		return err
	}
	return nil
}

// Neovim may exit 0 after a Lua startup error or a failed Lazy task. Fail the
// installation explicitly rather than claiming a half-installed editor works.
const lazyValidationLua = `local failed = {}
if vim.v.errmsg ~= "" then table.insert(failed, vim.v.errmsg) end
local ok, config = pcall(require, "lazy.core.config")
if not ok then table.insert(failed, tostring(config)) else
  for name, plugin in pairs(config.plugins) do
    if plugin.url and not vim.uv.fs_stat(plugin.dir) then table.insert(failed, name .. ": missing plugin directory") end
    for _, task in ipairs(plugin._.tasks or {}) do
      if task:has_errors() then table.insert(failed, name .. ": " .. task:output(vim.log.levels.ERROR)) end
    end
  end
end
if #failed > 0 then io.stderr:write(table.concat(failed, "\n") .. "\n"); vim.cmd("cquit 1") end`

func supportedStandard(binary string, flags []string, lang string) string {
	for _, f := range flags {
		c := exec.Command(binary, "-std="+f, "-x", lang, "-fsyntax-only", "-")
		c.Stdin = strings.NewReader("int main(void) { return 0; }\n")
		if c.Run() == nil {
			return f
		}
	}
	return ""
}

const keyboardRules = `# Managed by myeditorconfig. Physical Escape remains unchanged.
# Built-in AT keyboards
evdev:atkbd:*
 KEYBOARD_KEY_3a=esc

# USB/Bluetooth HID keyboards (Caps Lock HID usage 0x70039)
evdev:input:*
 KEYBOARD_KEY_70039=esc
`

func (i *installer) keyboard() error {
	p := "/etc/udev/hwdb.d/90-myeditorconfig-caps-escape.hwdb"
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	// Keep system backups beside the original so /home may be a separate filesystem.
	if old, err := os.ReadFile(p); err == nil && !bytes.Equal(old, []byte(keyboardRules)) {
		if err = os.WriteFile(p+".bak-"+time.Now().UTC().Format("20060102T150405"), old, 0644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(p, []byte(keyboardRules), 0644); err != nil {
		return err
	}
	if _, err := exec.LookPath("systemd-hwdb"); err == nil {
		if err = i.run("systemd-hwdb", "update"); err != nil {
			return err
		}
	} else {
		if err = i.run("udevadm", "hwdb", "--update"); err != nil {
			return err
		}
	}
	i.warn(i.run("udevadm", "trigger", "--subsystem-match=input", "--action=change"))
	i.warn(i.run("udevadm", "settle", "--timeout=10"))
	return nil
}

func (i *installer) loginShell() error {
	p, err := exec.LookPath("zsh")
	if err != nil {
		return err
	}
	b, err := os.ReadFile("/etc/shells")
	if err != nil {
		return err
	}
	if !strings.Contains("\n"+string(b)+"\n", "\n"+p+"\n") {
		f, err := os.OpenFile("/etc/shells", os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(f, "\n"+p)
		f.Close()
		if err != nil {
			return err
		}
	}
	return i.run("usermod", "--shell", p, i.u.Username)
}
