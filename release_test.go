package main

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseArtifacts(t *testing.T) {
	data, err := os.ReadFile("dist/SHA256SUMS")
	if os.IsNotExist(err) {
		t.Skip("build release binaries first")
	}
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			checks[f[1]] = f[0]
		}
	}
	for name, machine := range map[string]elf.Machine{"devboot-linux-amd64": elf.EM_X86_64, "devboot-linux-arm64": elf.EM_AARCH64} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("dist", name)
			f, err := elf.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if f.Machine != machine {
				t.Fatalf("wrong architecture: %v", f.Machine)
			}
			for _, p := range f.Progs {
				if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
					t.Fatal("binary depends on a dynamic loader/runtime")
				}
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm()&0111 == 0 {
				t.Fatal("binary is not executable")
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			h := sha256.Sum256(b)
			if hex.EncodeToString(h[:]) != checks[name] {
				t.Fatal("release checksum mismatch")
			}
		})
	}
}
