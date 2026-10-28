package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chicohaager/lintux-modkit/auth"
	"github.com/chicohaager/lintux-modkit/httpx"
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

// A storage the guard refuses after the wait: 0.3.4 exited, systemd restarted it
// 1038 times (nas-main, 2026-10-28, /DATA of uid 999), no job ran and nothing showed
// why. Now the server stays up, every API route and /health answer 503
// storage_refused with the reason, and a fixed folder is picked up without a restart.
func TestARefusedStorageKeepsTheServerUpAndSaysWhy(t *testing.T) {
	newTestStore(t)
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "planted")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "cron")
	if err := os.Symlink(real, path); err != nil {
		t.Fatal(err)
	}
	fs, err := openStorage(path, 2, time.Millisecond)
	if fs != nil || err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("openStorage on a symlink: %v %v", fs, err)
	}
	refuseStorage(path, err)
	t.Cleanup(func() { storageErr.Store(nil) })
	srv := httptest.NewServer(httpx.CSRF(storageGate(newMux(auth.Disabled().Middleware))))
	t.Cleanup(srv.Close)

	for _, p := range []string{"/cron/tasks", "/cron/settings", "/cron/health"} {
		resp, raw := call(t, srv, http.MethodGet, p, nil)
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "storage_refused") || !strings.Contains(string(raw), "symlink") {
			t.Fatalf("GET %s while refused: %d %s", p, resp.StatusCode, raw)
		}
	}

	if err := os.Remove(path); err != nil { // the fix: the planted link is gone
		t.Fatal(err)
	}
	go recheckStorage(path, 10*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for storageErr.Load() != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	for _, p := range []string{"/cron/tasks", "/cron/health"} {
		if resp, raw := call(t, srv, http.MethodGet, p, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s after the fix: %d %s", p, resp.StatusCode, raw)
		}
	}
}
