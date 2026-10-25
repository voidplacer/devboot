package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPackagesAcrossDistros(t *testing.T) {
	for _, distro := range []string{"ubuntu", "fedora", "arch"} {
		t.Run(distro, func(t *testing.T) {
			i := testInstaller(t)
			i.distro = distro
			var commands []string
			i.command = func(name string, args ...string) error {
				commands = append(commands, name+" "+strings.Join(args, " "))
				return nil
			}
			if err := i.packages(); err != nil {
				t.Fatal(err)
			}
			all := strings.Join(commands, "\n")
			for _, pkg := range []string{"clang", "fontconfig", "util-linux", "zsh", "git", "curl"} {
				if !strings.Contains(all, pkg) {
					t.Errorf("missing %s", pkg)
				}
			}
			switch distro {
			case "ubuntu":
				if strings.Contains(all, " kitty ") {
					t.Fatal("Ubuntu should not install Kitty twice")
				}
				if !strings.Contains(all, "--no-install-recommends") {
					t.Fatal("Ubuntu would download optional recommendations")
				}
				if strings.Contains(all, " neovim") {
					t.Error("Ubuntu must not install old repository Neovim")
				}
				if !strings.Contains(all, "add-apt-repository -y universe") || !strings.Contains(all, "DEBIAN_FRONTEND=noninteractive") {
					t.Fatal("Ubuntu is not unattended or Universe is missing")
				}
			case "arch":
				if !strings.Contains(all, "alsa-lib") {
					t.Fatal("Zed's ALSA runtime dependency is missing")
				}
				if !strings.Contains(all, "pacman -Syu --needed --noconfirm") {
					t.Fatal("Arch must avoid partial upgrades and prompts")
				}
			case "fedora":
				if !strings.Contains(all, "clang-tools-extra") {
					t.Fatal("Fedora formatter/clangd missing")
				}
			}
		})
	}
}

func TestUbuntuDoesNotReconfigureExistingUniverse(t *testing.T) {
	i := testInstaller(t)
	i.distro = "ubuntu"
	i.output = func(string, ...string) ([]byte, error) { return []byte("Package: fd-find\n"), nil }
	updates := 0
	i.command = func(name string, args ...string) error {
		all := name + " " + strings.Join(args, " ")
		if strings.Contains(all, "software-properties-common") || name == "add-apt-repository" {
			t.Fatal("unnecessary repository setup")
		}
		if all == "apt-get update" {
			updates++
		}
		return nil
	}
	if err := i.packages(); err != nil {
		t.Fatal(err)
	}
	if updates != 1 {
		t.Fatal("apt was refreshed repeatedly")
	}
}

func TestParallelSetupJoinsAndWaitsForAllJobs(t *testing.T) {
	ready := make(chan struct{}, 3)
	release := make(chan struct{})
	done := make(chan error, 1)
	var finished atomic.Int32
	steps := []setupStep{}
	for n := 0; n < 3; n++ {
		steps = append(steps, setupStep{fmt.Sprint(n), func() error { ready <- struct{}{}; <-release; finished.Add(1); return errors.New("fixture failure") }})
	}
	go func() { done <- parallelSetup(steps) }()
	for n := 0; n < 3; n++ {
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatal("jobs did not run concurrently")
		}
	}
	close(release)
	if err := <-done; err == nil || finished.Load() != 3 {
		t.Fatal("returned before collecting all setup jobs", err)
	}
}

func TestRuntimeReuseAvoidsLargeDownloads(t *testing.T) {
	i := testInstaller(t)
	i.tmp = t.TempDir()
	base := filepath.Join(i.u.HomeDir, ".local/share/myeditorconfig")
	for _, name := range []string{"node/bin/node", "node/bin/npm", "node/bin/npx", "java/bin/java", "java/bin/javac"} {
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fixture"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	i.output = func(name string, args ...string) ([]byte, error) {
		all := strings.Join(args, " ")
		switch {
		case strings.Contains(all, "node/bin/node"):
			return []byte("v24.1.0\n"), nil
		case strings.Contains(all, "java/bin/javac"):
			return []byte("javac 21.0.8\n"), nil
		case strings.Contains(all, "tree-sitter --version"):
			return []byte("tree-sitter 0.27.1\n"), nil
		}
		return nil, errors.New("unexpected version probe")
	}
	i.command = func(name string, args ...string) error {
		if name != "curl" || args[len(args)-1] != "https://nodejs.org/dist/index.json" {
			t.Fatalf("unexpected download/command %s %v", name, args)
		}
		return os.WriteFile(filepath.Join(i.tmp, "node-index.json"), []byte(`[{"version":"v24.1.0","lts":"Krypton"}]`), 0644)
	}
	if err := i.runtimes(); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityVersions(t *testing.T) {
	for v, want := range map[string]bool{"glibc 2.31\n": true, "glibc 2.35": false, "glibc 2.39": false, "unknown": false} {
		if got := needsCompatibleKitty(v); got != want {
			t.Errorf("glibc %q: %v", v, got)
		}
	}
	for v, want := range map[string]bool{"kitty 0.42.2 created by Kovid Goyal": true, "kitty 0.35.2": true, "kitty 0.15.0": false, "bad": false} {
		if got := compatibleKitty(v); got != want {
			t.Errorf("Kitty %q: %v", v, got)
		}
	}
	for v, want := range map[string]bool{"javac 21.0.13": true, "javac 21": true, "javac 17.0.9": false, "javac 1.8.0": false, "javac 210.1": false} {
		if got := java21Version(v); got != want {
			t.Errorf("Java %q: %v", v, got)
		}
	}
}

func TestPackageFailureStopsFurtherCommands(t *testing.T) {
	i := testInstaller(t)
	i.distro = "ubuntu"
	calls := 0
	failure := errors.New("repository unavailable")
	i.command = func(string, ...string) error { calls++; return failure }
	if err := i.packages(); !errors.Is(err, failure) {
		t.Fatal("package failure was hidden:", err)
	}
	if calls != 1 {
		t.Fatal("continued after package failure")
	}
}

func TestDownloadPermissionsAndChecksum(t *testing.T) {
	i := testInstaller(t)
	i.tmp = t.TempDir()
	payload := []byte("downloaded binary")
	i.command = func(name string, args ...string) error {
		if name != "curl" {
			t.Fatalf("unexpected command %s", name)
		}
		for n, a := range args {
			if a == "--output" {
				return os.WriteFile(args[n+1], payload, 0600)
			}
		}
		return errors.New("no output destination")
	}
	h := sha256.Sum256(payload)
	r := release{Tag: "v1"}
	r.Assets = append(r.Assets, struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	}{"tool.tar.gz", "https://example.com/tool.tar.gz", "sha256:" + hex.EncodeToString(h[:])})
	p, err := i.asset(r, func(s string) bool { return s == "tool.tar.gz" })
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("user cannot read staged download: %v %v", info, err)
	}
	r.Assets[0].Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err = i.asset(r, func(string) bool { return true }); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatal("corrupt download accepted:", err)
	}
	if _, err = i.asset(r, func(string) bool { return false }); err == nil {
		t.Fatal("missing asset accepted")
	}
	if _, err = i.fetch("https://example.com", "../escape"); err == nil {
		t.Fatal("unsafe download path accepted")
	}
}

func TestReleasePagesKeepAssetsAndDigestsTogether(t *testing.T) {
	digest := strings.Repeat("a", 64)
	body := `<ul><li><a href="/owner/tool/releases/download/v1/tool.tar.gz">tool</a><span>sha256:` + digest + `</span></li><li><a href="/other/tool/releases/download/v1/wrong.tar.gz">other repo</a></li><li><a href="https://example.com/owner/tool/releases/download/v1/evil.tar.gz">external</a></li><li><a href="/owner/tool/releases/download/v1/arm.tar.gz">arm</a><span>sha256:` + strings.Repeat("b", 64) + `</span></li></ul>`
	r, err := parseReleaseAssets("owner/tool", "v1", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Assets) != 2 || r.Assets[0].Name != "tool.tar.gz" || r.Assets[0].Digest != "sha256:"+digest || r.Assets[1].Digest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("incorrect asset/digest associations: %+v", r)
	}
	if _, err = parseReleaseAssets("owner/tool", "v1", "<html>not a release</html>"); err == nil {
		t.Fatal("empty release page accepted")
	}
	i := testInstaller(t)
	calls := 0
	i.metadata = func(address string) ([]byte, string, error) {
		calls++
		if calls == 1 {
			return nil, "https://github.com/owner/tool/releases/tag/v1", nil
		}
		return []byte(body), address, nil
	}
	if _, err = i.latest("owner/tool"); err != nil || calls != 2 {
		t.Fatalf("release discovery failed: %v", err)
	}
	i.metadata = func(string) ([]byte, string, error) {
		return nil, "https://example.com/owner/tool/releases/tag/v1", nil
	}
	if _, err = i.latest("owner/tool"); err == nil {
		t.Fatal("foreign release redirect accepted")
	}
}

func TestLiveReleaseMetadata(t *testing.T) {
	if os.Getenv("BOOTSTRAP_LIVE_METADATA") != "1" {
		t.Skip("set BOOTSTRAP_LIVE_METADATA=1 to verify current upstream release pages")
	}
	for _, tc := range []struct{ repo, marker string }{{"eza-community/eza", "eza_x86_64-unknown-linux-musl.tar.gz"}, {"ajeetdsouza/zoxide", "x86_64-unknown-linux-musl.tar.gz"}, {"junegunn/fzf", "linux_amd64.tar.gz"}, {"neovim/neovim", "nvim-linux-x86_64.tar.gz"}, {"ryanoasis/nerd-fonts", "CascadiaMono.tar.xz"}} {
		t.Run(tc.repo, func(t *testing.T) {
			i := testInstaller(t)
			r, err := i.latest(tc.repo)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range r.Assets {
				if strings.Contains(a.Name, tc.marker) {
					if !strings.HasPrefix(a.Digest, "sha256:") {
						t.Fatal("upstream asset lacks digest")
					}
					t.Log(r.Tag, a.Name, a.Digest)
					return
				}
			}
			t.Fatalf("asset %s missing from release %s", tc.marker, r.Tag)
		})
	}
}

func TestRepeatedChangesPreserveEveryBackup(t *testing.T) {
	i := testInstaller(t)
	p := filepath.Join(i.u.HomeDir, ".zshrc")
	for _, s := range []string{"first", "second", "third"} {
		if err := i.write(p, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(i.backup, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			seen[string(b)] = true
		}
		return nil
	})
	if err != nil || !seen["first"] || !seen["second"] {
		t.Fatalf("an earlier backup was overwritten: %v %v", seen, err)
	}
}

func TestUserCommandsDiscardRootEnvironment(t *testing.T) {
	i := testInstaller(t)
	i.u.Username = "tester"
	i.command = func(name string, args ...string) error {
		if name != "runuser" {
			t.Fatal("command would run as root")
		}
		all := strings.Join(args, "\n")
		for _, want := range []string{"-u\ntester\n--\nenv\n-i\n", "--chdir=" + i.u.HomeDir, "HOME=" + i.u.HomeDir, "PATH=" + i.u.HomeDir + "/.local/bin", "JAVA_HOME=" + i.u.HomeDir} {
			if !strings.Contains(all, want) {
				t.Errorf("missing safe environment %q", want)
			}
		}
		return nil
	}
	if err := i.userRun("git", "clone", "https://example.com/repo"); err != nil {
		t.Fatal(err)
	}
}

func TestTreeSitterFallsBackWhenPrebuiltCannotRun(t *testing.T) {
	i := testInstaller(t)
	i.tmp = t.TempDir()
	var calls []string
	checks := 0
	i.command = func(name string, args ...string) error {
		all := name + " " + strings.Join(args, " ")
		calls = append(calls, all)
		if name == "curl" {
			for n, a := range args {
				if a == "--output" {
					return os.WriteFile(args[n+1], []byte("fixture"), 0600)
				}
			}
		}
		if strings.Contains(all, "tree-sitter --version") {
			checks++
			if checks == 1 {
				return errors.New("GLIBC_2.39 not found")
			}
		}
		return nil
	}
	if err := i.treeSitter(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "cargo install --locked --force") {
		t.Fatal("unusable tree-sitter binary was left installed")
	}
	if checks != 2 {
		t.Fatal("source-built CLI was not validated")
	}
}

func TestCompatibleTreeSitterIsNotReplacedOnRerun(t *testing.T) {
	for input, want := range map[string]bool{"tree-sitter 0.27.1": true, "tree-sitter 0.26.1 (abc)": true, "tree-sitter 0.26.0": false, "tree-sitter 0.25.10": false, "tree-sitter 1.0.0": true, "broken": false} {
		if got := compatibleTreeSitter(input); got != want {
			t.Errorf("%s: got %v want %v", input, got, want)
		}
	}
	i := testInstaller(t)
	i.output = func(string, ...string) ([]byte, error) { return []byte("tree-sitter 0.27.1\n"), nil }
	i.command = func(string, ...string) error { t.Fatal("working source-built CLI would be replaced"); return nil }
	if err := i.treeSitter(); err != nil {
		t.Fatal(err)
	}
}

func TestKittyFallbackAndValidation(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			i := testInstaller(t)
			probes := 0
			fallbacks := 0
			i.command = func(name string, args ...string) error {
				all := strings.Join(args, " ")
				if strings.Contains(all, "installer=version-0.42.2") {
					fallbacks++
				}
				if strings.HasSuffix(all, "kitty --version") {
					probes++
					if bad && probes == 1 {
						return errors.New("GLIBC_2.35 not found")
					}
				}
				return nil
			}
			if err := i.installKitty("/tmp/kitty-installer.sh"); err != nil {
				t.Fatal(err)
			}
			if bad && (fallbacks != 1 || probes != 2) {
				t.Fatal("incompatible Kitty was not replaced and validated")
			}
			if !bad && (fallbacks != 0 || probes != 1) {
				t.Fatal("working current Kitty was unnecessarily downgraded")
			}
		})
	}
}

func TestSupportedStandardFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux compiler fixture")
	}
	p := filepath.Join(t.TempDir(), "compiler")
	if err := os.WriteFile(p, []byte("#!/bin/sh\ncase \"$1\" in -std=c++2b) exit 0;; *) exit 1;; esac\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := supportedStandard(p, []string{"c++26", "c++2c", "c++23", "c++2b", "c++20"}, "c++"); got != "c++2b" {
		t.Fatal("unsupported flag selected:", got)
	}
	if got := supportedStandard(p, []string{"c23", "c2x", "c17"}, "c"); got != "" {
		t.Fatal("unusable compiler accepted:", got)
	}
}

func TestBinaryCLI(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux binary")
	}
	if os.Geteuid() == 0 {
		t.Skip("implicit user selection and sudo forwarding require a non-root caller")
	}
	d := t.TempDir()
	binary := filepath.Join(d, "bootstrap")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	fakeSudo := filepath.Join(d, "sudo")
	if err := os.WriteFile(fakeSudo, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nexit 42\n"), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--dry-run")
	cmd.Dir = d
	cmd.Env = append(os.Environ(), "PATH="+d+":"+os.Getenv("PATH"))
	if b, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(b), "No changes made") {
		t.Fatalf("dry run requested sudo or failed: %v\n%s", err, b)
	}
	cmd = exec.Command(binary, "--user", "example-account")
	cmd.Dir = d
	cmd.Env = append(os.Environ(), "PATH="+d+":"+os.Getenv("PATH"))
	b, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 || !strings.Contains(string(b), "--\n"+binary+"\n--user\nexample-account") {
		t.Fatalf("sudo arguments/exit status lost: %v\n%s", err, b)
	}
}

func TestKeyboardDatabaseParses(t *testing.T) {
	tool, err := exec.LookPath("systemd-hwdb")
	if err != nil {
		t.Skip("systemd-hwdb is not installed")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "etc/udev/hwdb.d")
	if err = os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "90-caps-escape.hwdb"), []byte(keyboardRules), 0644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(tool, "--strict", "--root="+root, "update").CombinedOutput(); err != nil {
		t.Fatalf("invalid keyboard database: %v\n%s", err, b)
	}
	for modalias, want := range map[string]string{"evdev:atkbd:dmi:bvnTest:svnTest:pnKeyboard:": "KEYBOARD_KEY_3a=esc", "evdev:input:b0003v0001p0001e0001": "KEYBOARD_KEY_70039=esc", "evdev:input:b0005v0001p0001e0001": "KEYBOARD_KEY_70039=esc"} {
		b, err := exec.Command(tool, "--root="+root, "query", modalias).CombinedOutput()
		if err != nil || !strings.Contains(string(b), want) {
			t.Fatalf("keyboard %s did not get Caps mapping: %v\n%s", modalias, err, b)
		}
	}
}

func TestConcurrentDirectoryCreation(t *testing.T) {
	i := testInstaller(t)
	var steps []setupStep
	for n := 0; n < 20; n++ {
		path := filepath.Join(i.u.HomeDir, ".local/state/shared-backups", fmt.Sprint(n))
		steps = append(steps, setupStep{"directory", func() error { return i.mkdir(path) }})
	}
	if err := parallelSetup(steps); err != nil {
		t.Fatal(err)
	}
}
