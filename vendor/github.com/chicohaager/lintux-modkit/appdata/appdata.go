// Package appdata makes a module's directory under /DATA/AppData safe for root.
//
// /DATA/AppData is 0777 without the sticky bit on ZimaOS 1.8 (measured on .184,
// 2026-10-26, ZIMAOS-KNOWLEDGE §79.11), and the share of /DATA is writable over
// SMB: whoever can write there renames root's directory (mode 0700 or not) and
// puts their own in its place — a wg-quick config with PostUp, a pool.env root
// sources, cron jobs, a tunnel's endpoint. With the sticky bit only a directory's
// owner renames it (measured: "Operation not permitted" for uid 999 afterwards),
// and a directory that is not root's is refused.
//
// Secure runs before a module reads anything there: from Go at the start of the
// daemon, from systemd through sysext/appdata-guard.sh (ExecStartPre or a guard
// unit) for programs systemd starts with a file from there.
package appdata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// trusted owners: root, and the process' own user (tests).
var trusted = func(uid int) bool { return uid == 0 || uid == os.Geteuid() }

// parentOK replaces the parent and ancestor checks in tests.
var parentOK func(string) bool

func uidOf(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// safe: nobody but the owner changes the directory's entries — not writable by
// group or others, or sticky (others may add entries but not rename root's).
func safe(fi os.FileInfo) bool {
	return fi.Mode().Perm()&0o022 == 0 || fi.Mode()&os.ModeSticky != 0
}

// Secure checks dir and the path to it and sets what it can:
//   - every ancestor above the parent belongs to root and is safe;
//   - the parent (e.g. /DATA/AppData) is a real directory of root; when others can
//     write it, it gets the sticky bit;
//   - dir is created 0700 when missing; an existing one must be a real directory
//     (no symlink) of root that neither group nor others can write. It is not
//     changed otherwise (a module may keep 0755).
func Secure(dir string) error {
	dir = filepath.Clean(dir)
	parent := filepath.Dir(dir)
	if parentOK == nil || !parentOK(parent) {
		for a := filepath.Dir(parent); ; a = filepath.Dir(a) {
			fi, err := os.Stat(a)
			if err != nil {
				return err
			}
			if !trusted(uidOf(fi)) || !safe(fi) {
				return fmt.Errorf("appdata: %s (mode %v, uid %d) lets others change the path to %s", a, fi.Mode(), uidOf(fi), dir)
			}
			if a == filepath.Dir(a) {
				break
			}
		}
		pfi, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		if pfi.Mode()&os.ModeSymlink != 0 || !pfi.IsDir() {
			return fmt.Errorf("appdata: %s is not a plain directory", parent)
		}
		if !trusted(uidOf(pfi)) {
			return fmt.Errorf("appdata: %s belongs to uid %d", parent, uidOf(pfi))
		}
		if !safe(pfi) {
			if err := os.Chmod(parent, pfi.Mode().Perm()|os.ModeSticky); err != nil {
				return fmt.Errorf("appdata: sticky bit on %s: %w", parent, err)
			}
		}
	}
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
		fi, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("appdata: %s is a symlink", dir)
	case !fi.IsDir():
		return fmt.Errorf("appdata: %s is not a directory", dir)
	case !trusted(uidOf(fi)):
		return fmt.Errorf("appdata: %s belongs to uid %d, not root — refusing to use it", dir, uidOf(fi))
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("appdata: others can write to %s (mode %v)", dir, fi.Mode().Perm())
	}
	return nil
}
