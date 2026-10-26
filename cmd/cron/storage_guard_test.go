package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /DATA/AppData is 0777 without the sticky bit on ZimaOS 1.8 (KB §79.11): the jobs
// in /DATA/AppData/cron run as root (/bin/sh -lc), so a swapped folder would run
// anyone's commands. The storage folder is checked before it is opened.
func TestAStorageFolderRootCannotTrustIsRefused(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openAt(filepath.Join(base, "cron")); err != nil {
		t.Fatalf("a fresh folder: %v", err)
	}
	real := filepath.Join(base, "planted")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "cron-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openAt(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked folder: err %v", err)
	}
}
