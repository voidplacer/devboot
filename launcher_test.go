package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReleaseDownloader(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux downloader")
	}
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			d := t.TempDir()
			result := filepath.Join(d, "executed")
			cwdResult := filepath.Join(d, "cwd")
			payload := []byte("#!/bin/sh\nprintf '%s' \"$*\" > \"$DEVBOOT_TEST_RESULT\"\npwd > \"$DEVBOOT_TEST_CWD\"\n")
			bin := filepath.Join(d, "payload")
			if err := os.WriteFile(bin, payload, 0600); err != nil {
				t.Fatal(err)
			}
			h := sha256.Sum256(payload)
			if corrupt {
				h = sha256.Sum256([]byte("wrong"))
			}
			arch := runtime.GOARCH
			if arch != "amd64" && arch != "arm64" {
				t.Skip("supported release architectures only")
			}
			sums := filepath.Join(d, "sums")
			if err := os.WriteFile(sums, []byte(fmt.Sprintf("%x  devboot-linux-%s\n", h, arch)), 0600); err != nil {
				t.Fatal(err)
			}
			curl := `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in --output) out=$2; shift 2;; *) address=$1; shift;; esac
done
case "$address" in */SHA256SUMS) cp "$DEVBOOT_SUM_FIXTURE" "$out";; */devboot-linux-*) cp "$DEVBOOT_BIN_FIXTURE" "$out";; *) exit 9;; esac
`
			if err := os.WriteFile(filepath.Join(d, "curl"), []byte(curl), 0755); err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(d, "cache")
			cmd := exec.Command("sh", "get.sh", "--dry-run")
			cmd.Env = append(os.Environ(), "PATH="+d+":"+os.Getenv("PATH"), "XDG_CACHE_HOME="+cache, "DEVBOOT_TEST_RESULT="+result, "DEVBOOT_TEST_CWD="+cwdResult, "DEVBOOT_SUM_FIXTURE="+sums, "DEVBOOT_BIN_FIXTURE="+bin)
			out, err := cmd.CombinedOutput()
			if corrupt {
				if err == nil {
					t.Fatal("corrupt binary was executed")
				}
				if _, err = os.Stat(result); !os.IsNotExist(err) {
					t.Fatal("executed an unverified payload")
				}
			} else {
				if err != nil {
					t.Fatalf("download failed: %v\n%s", err, out)
				}
				b, err := os.ReadFile(result)
				if err != nil || string(b) != "--dry-run" {
					t.Fatalf("arguments not forwarded: %q %v", b, err)
				}
				want, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				b, err = os.ReadFile(cwdResult)
				if err != nil || string(b) != want+"\n" {
					t.Fatalf("launcher changed the working directory: %q %v", b, err)
				}
			}
			left, err := filepath.Glob(filepath.Join(cache, "devboot/download.*"))
			if err != nil || len(left) != 0 {
				t.Fatal("download directory was not cleaned up")
			}
		})
	}
}
