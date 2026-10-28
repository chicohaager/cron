// Package appdata makes a module's directory under /DATA/AppData safe for root.
//
// /DATA/AppData is 0777 without the sticky bit on ZimaOS 1.8 (measured on the second test NAS,
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
	"log"
	"os"
	"path/filepath"
	"syscall"
)

// trusted owners: root, and the process' own user (tests).
var trusted = func(uid int) bool { return uid == 0 || uid == os.Geteuid() }

// parentOK replaces the parent and ancestor checks in tests.
var parentOK func(string) bool

// dataRoot is the one ancestor Secure repairs: on a ZimaOS 1.8 box /DATA belonged to
// the login user (uid 999, also its home; measured nas-main, 2026-10-28), on two others to
// root (nas-test, remote-cube). That owner can rename /DATA/AppData, so the check refuses rightly,
// and every module with this guard stopped. Secure gives /DATA back to root (the mode
// stays) and says so in the log; a symlinked /DATA or an unsafe mode is still refused.
var dataRoot = "/DATA"

// chown replaces os.Chown in tests (they do not run as root).
var chown = os.Chown

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

// repairOwner gives a (dataRoot) to root and returns its new state, which the caller
// checks as before.
func repairOwner(a string, fi os.FileInfo, dir string) (os.FileInfo, error) {
	lfi, err := os.Lstat(a)
	if err != nil {
		return nil, err
	}
	if lfi.Mode()&os.ModeSymlink != 0 || !lfi.IsDir() {
		return fi, nil // refused below
	}
	old, group := uidOf(fi), -1
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		group = int(st.Gid)
	}
	if err := chown(a, 0, 0); err != nil {
		return nil, fmt.Errorf("appdata: %s belongs to uid %d, which can change the path to %s; owner root not set: %w", a, old, dir, err)
	}
	log.Printf("appdata: %s belonged to uid %d, which could change the path to %s; owner set to root (back: chown %d:%d %s)", a, old, dir, old, group, a)
	return os.Stat(a)
}

// ancestor checks a, an ancestor of dir: root's (after the repair of dataRoot) and safe.
func ancestor(a, dir string) error {
	fi, err := os.Stat(a)
	if err != nil {
		return err
	}
	if !trusted(uidOf(fi)) && a == dataRoot {
		if fi, err = repairOwner(a, fi, dir); err != nil {
			return err
		}
	}
	if !trusted(uidOf(fi)) || !safe(fi) {
		return fmt.Errorf("appdata: %s (mode %v, uid %d) lets others change the path to %s", a, fi.Mode(), uidOf(fi), dir)
	}
	return nil
}

// chain checks p (a path without symlinks) and all its ancestors up to /.
func chain(p, dir string) error {
	for a := p; ; a = filepath.Dir(a) {
		if err := ancestor(a, dir); err != nil {
			return err
		}
		if a == filepath.Dir(a) {
			return nil
		}
	}
}

// target is where the symlink l points, as an absolute path. Secure follows one
// step to an existing path without further symlinks (or . and ..), and nothing else
// — the same rule as sysext/appdata-guard.sh.
func target(l string) (string, error) {
	t, err := os.Readlink(l)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(t) {
		t = filepath.Dir(l) + "/" + t
	}
	r, err := filepath.EvalSymlinks(t)
	if err != nil {
		return "", fmt.Errorf("appdata: %s points to %s, which is missing: %w", l, t, err)
	}
	if r != t {
		return "", fmt.Errorf("appdata: %s points to %s, a path with further symlinks or . and ..; only a symlink to a real path is followed", l, t)
	}
	return r, nil
}

// Secure checks dir and the path to it and sets what it can:
//   - every ancestor above the parent belongs to root and is safe; /DATA owned by
//     someone else is given to root (see dataRoot);
//   - the parent (e.g. /DATA/AppData) is a directory of root; when others can
//     write it, it gets the sticky bit;
//   - a symlink on the way (/DATA/AppData moved to another disk) is followed one
//     step to a real path, and that path is checked up to / as well. Before, a
//     symlinked parent was refused, and the path behind a symlinked ancestor was
//     not checked at all;
//   - dir is created 0700 when missing; an existing one must be a real directory
//     (no symlink) of root that neither group nor others can write. It is not
//     changed otherwise (a module may keep 0755).
func Secure(dir string) error {
	dir = filepath.Clean(dir)
	parent := filepath.Dir(dir)
	if parentOK == nil || !parentOK(parent) {
		for a := filepath.Dir(parent); ; a = filepath.Dir(a) {
			if err := ancestor(a, dir); err != nil {
				return err
			}
			if lfi, err := os.Lstat(a); err == nil && lfi.Mode()&os.ModeSymlink != 0 {
				r, err := target(a)
				if err != nil {
					return err
				}
				if err := chain(r, dir); err != nil {
					return err
				}
			}
			if a == filepath.Dir(a) {
				break
			}
		}
		if lfi, err := os.Lstat(parent); err == nil && lfi.Mode()&os.ModeSymlink != 0 {
			r, err := target(parent)
			if err != nil {
				return err
			}
			if err := chain(filepath.Dir(r), dir); err != nil {
				return err
			}
			log.Printf("appdata: %s is a symlink to %s; checking and using %s", parent, r, r)
			parent, dir = r, filepath.Join(r, filepath.Base(dir))
		}
		pfi, err := os.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("appdata: %s is missing", parent)
		}
		if err != nil {
			return err
		}
		if !pfi.IsDir() {
			return fmt.Errorf("appdata: %s is not a directory", parent)
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
