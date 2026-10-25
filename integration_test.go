package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Opt in: BOOTSTRAP_INTEGRATION=1 go test -v -run TestContainerInstall -timeout 60m
// No host directories or devices are mounted into the disposable containers.
func TestContainerInstall(t *testing.T) {
	if os.Getenv("BOOTSTRAP_INTEGRATION") != "1" {
		t.Skip("set BOOTSTRAP_INTEGRATION=1 to run real distro installations in Podman")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("this integration matrix runs on Linux x86_64")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Fatal("Podman is required for isolated integration tests")
	}
	d := t.TempDir()
	binary := filepath.Join(d, "bootstrap")
	c := exec.Command(filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-o", binary, ".")
	c.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	for _, tc := range []struct{ name, image string }{{"ubuntu20", "docker.io/library/ubuntu:20.04"}, {"ubuntu24", "docker.io/library/ubuntu:24.04"}, {"fedora44", "registry.fedoraproject.org/fedora:44"}, {"arch", "docker.io/library/archlinux:base"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := fmt.Sprintf("myeditorconfig-integration-%s-%d", tc.name, time.Now().UnixNano())
			reuse := os.Getenv("BOOTSTRAP_TEST_REUSE_PREFIX")
			if reuse != "" {
				name = reuse + tc.name
			}
			log, err := os.CreateTemp("", "myeditorconfig-"+tc.name+"-*.log")
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			t.Log("Installation log:", log.Name())
			podman := func(args ...string) {
				t.Helper()
				cmd := exec.Command("podman", args...)
				cmd.Stdout = log
				cmd.Stderr = log
				if err := cmd.Run(); err != nil {
					t.Fatalf("podman %s: %v; see %s", strings.Join(args, " "), err, log.Name())
				}
			}
			if reuse == "" {
				podman("run", "-d", "--name", name, tc.image, "sleep", "infinity")
				t.Cleanup(func() {
					if b, err := exec.Command("podman", "rm", "-f", "--time", "0", name).CombinedOutput(); err != nil {
						t.Logf("container cleanup: %v %s", err, b)
					}
				})
				podman("exec", name, "useradd", "-m", "-s", "/bin/bash", "tester")
			} else {
				t.Log("Rechecking existing disposable fixture:", name)
			}
			podman("cp", binary, name+":/usr/local/bin/bootstrap")
			podman("exec", name, "sh", "-c", `printf 'original-zshrc\n' > /home/tester/.zshrc; chown tester:tester /home/tester/.zshrc`)
			podman("exec", name, "/usr/local/bin/bootstrap", "--user", "tester")
			podman("exec", name, "runuser", "-u", "tester", "--", "env", "-i", "HOME=/home/tester", "PATH=/home/tester/.local/bin:/usr/local/bin:/usr/bin:/bin", "JAVA_HOME=/home/tester/.local/share/myeditorconfig/java", "zsh", "-ic", integrationSmoke)
			// Rerun: existing installation, system links, fonts and backups must survive.
			podman("exec", name, "/usr/local/bin/bootstrap", "--user", "tester")
			podman("exec", name, "sh", "-c", `find /home/tester/.local/state/myeditorconfig/backups -name .zshrc -exec grep -l original-zshrc {} + | grep .`)
		})
	}
}

const integrationSmoke = `set -eu
for bin in zsh clang clang++ clang-format clangd nvim kitty zed eza zoxide fzf fd rg node npm java javac tree-sitter; do command -v "$bin"; done
zsh -n ~/.zshrc
nvim --headless -u NONE -i NONE --cmd 'lua if vim.fn.has("nvim-0.11.2") ~= 1 or jit == nil then vim.cmd("cquit 1") end' +qa
tree-sitter --version
nvim --headless '+lua if vim.v.errmsg ~= "" then io.stderr:write(vim.v.errmsg); vim.cmd("cquit 1") end' +qa
~/.local/zed.app/libexec/zed-editor --help
java -version
node --version
fc-match -f '%{family}\n' 'CaskaydiaMono Nerd Font' | grep 'CaskaydiaMono Nerd Font'
mkdir -p ~/smoke
cd ~/smoke
printf '#include <stdio.h>\nint main(void) { puts("C works"); }\n' > hello.c
printf '#include <iostream>\nint main() { std::cout << "C++ works\\n"; }\n' > hello.cpp
printf 'class Hello { public static void main(String[] args) { System.out.println("Java works"); } }\n' > Hello.java
cout hello.c | grep 'C works'
gpp hello.cpp | grep 'C++ works'
jvc Hello.java | grep 'Java works'
test -f ~/.config/nvim/lua/plugins/conform.lua
test -f ~/.config/nvim/lua/plugins/noice.lua
test -f ~/.config/nvim/lua/config/keymaps.lua
test -f ~/.config/zed/settings.json
test -f ~/.config/kitty/kitty.conf
test -f ~/.clang-format
test -s /etc/udev/hwdb.d/90-myeditorconfig-caps-escape.hwdb
getent passwd tester | grep '/zsh$'
ldd ~/.local/zed.app/libexec/zed-editor > ~/zed-ldd.txt
if grep -q 'not found' ~/zed-ldd.txt; then cat ~/zed-ldd.txt; exit 1; fi
kitty +runpy 'from kitty.config import load_config; bad=[]; load_config("/home/tester/.config/kitty/kitty.conf", accumulate_bad_lines=bad); print(bad); raise SystemExit(bool(bad))'
`
