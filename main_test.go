package main

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDetectDistro(t *testing.T) {
	for _, tc := range []struct{ data, want string }{
		{"ID=ubuntu\nVERSION_ID=20.04", "ubuntu"},
		{"ID=ubuntu\nVERSION_ID=22.04", "ubuntu"},
		{"ID=fedora\nID_LIKE=\"rhel centos\"", "fedora"},
		{"ID=arch", "arch"},
		{"ID=linuxmint\nID_LIKE=\"ubuntu debian\"", "ubuntu"},
		{"ID=manjaro\nID_LIKE=arch", "arch"},
	} {
		got, err := detectDistro(tc.data)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v", tc.data, got, err)
		}
	}
	if _, err := detectDistro("ID=debian"); err == nil {
		t.Fatal("silently treated unsupported distro as Ubuntu")
	}
}

func testInstaller(t *testing.T) *installer {
	t.Helper()
	home := t.TempDir()
	return &installer{u: &user.User{HomeDir: home, Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid())}, uid: os.Getuid(), gid: os.Getgid(), backup: filepath.Join(home, "backups", "run"), output: func(string, ...string) ([]byte, error) { return nil, errors.New("fixture: no existing tool") }}
}

func TestConfigReplacementBacksUpAndReruns(t *testing.T) {
	i := testInstaller(t)
	p := filepath.Join(i.u.HomeDir, ".config/kitty/kitty.conf")
	if err := i.write(p, []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := i.write(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(i.backup, strings.TrimPrefix(p, "/"))
	b, err := os.ReadFile(backup)
	if err != nil || string(b) != "original" {
		t.Fatalf("backup lost: %q, %v", b, err)
	}
	if err = i.write(p, []byte("new")); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(backup)
	if err != nil || string(b) != "original" {
		t.Fatalf("rerun overwrote backup: %q, %v", b, err)
	}
}

func TestSymlinkReplacementPreservesExternalTarget(t *testing.T) {
	i := testInstaller(t)
	external := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(external, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(i.u.HomeDir, ".zshrc")
	if err := os.Symlink(external, p); err != nil {
		t.Fatal(err)
	}
	if err := i.write(p, []byte("managed")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(external)
	if err != nil || string(b) != "untouched" {
		t.Fatalf("modified symlink target: %q %v", b, err)
	}
	backup := filepath.Join(i.backup, strings.TrimPrefix(p, "/"))
	dest, err := os.Readlink(backup)
	if err != nil || dest != external {
		t.Fatalf("did not back up symlink: %q %v", dest, err)
	}
}

func TestEmbeddedHiddenConfigs(t *testing.T) {
	for _, p := range []string{"shell/.zshrc", "shell/kitty.conf", "zed/settings.json", "zed/keymap.json", "temp_neovim/.clang-format", "temp_neovim/conform.lua", "temp_neovim/noice.lua", "temp_neovim/keymaps.lua", "temp_neovim/LazyExtras.txt"} {
		b, err := configs.ReadFile(p)
		if err != nil || len(b) == 0 {
			t.Errorf("missing embedded config %s: %v", p, err)
		}
	}
}

func TestKeyboardPreservesEscape(t *testing.T) {
	if !strings.Contains(keyboardRules, "KEYBOARD_KEY_3a=esc") || !strings.Contains(keyboardRules, "KEYBOARD_KEY_70039=esc") {
		t.Fatal("missing Caps Lock mapping")
	}
	for _, line := range strings.Split(keyboardRules, "\n") {
		if strings.Contains(line, "KEYBOARD_KEY_01=") || strings.Contains(line, "KEYBOARD_KEY_70029=") {
			t.Fatal("physical Escape remapped")
		}
	}
}
