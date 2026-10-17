// Package watchdog installs the two unit pairs a systemd-sysext module needs
// on ZimaOS to survive boot and upgrades.
//
// Units inside a sysext are not scheduled at boot: multi-user.target is
// resolved before the extension is merged into /usr, so a WantedBy= symlink
// points at a unit that does not exist yet. /etc/systemd/system is persistent
// and already there, hence two small units live there:
//
//	<name>-watchdog.timer  (OnBootSec)  → <name>-watchdog.service: start <name>.service if inactive
//	<name>-refresh.path    (PathChanged=<binary>) → <name>-refresh.service: restart after an upgrade
//
// Measured with a real reboot on ZimaOS 1.7.0 (tailscale sysext) and in
// production for cron since April 2026. Still required on 1.8.0-beta1: a
// sysext carrying EXTENSION_RELOAD_MANAGER=1 plus a shipped
// multi-user.target.wants/ link was NOT started at boot either (measured
// 2026-10-02, ZimaBoard2 — systemd reloaded after the merge, the boot
// transaction did not pick the new Wants= up). See ZIMAOS-KNOWLEDGE §70.
package watchdog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Options names the service to guard.
type Options struct {
	// Service is the unit name without ".service", e.g. "cron".
	Service string
	// Binary is the executable the refresh path unit watches, e.g. "/usr/bin/cron".
	Binary string
	// BootDelaySec is the OnBootSec of the watchdog timer (default 15).
	BootDelaySec int
	// UnitDir defaults to /etc/systemd/system; tests point it elsewhere.
	UnitDir string
	// Systemctl lets tests stub the systemctl calls.
	Systemctl func(args ...string) error
}

// systemctlTimeout bounds one systemctl call. `enable --now` waits for the
// start job; a job that never finishes must not hold the daemon that calls
// Install at its own startup. Killing systemctl does not cancel the job.
const systemctlTimeout = 60 * time.Second

// systemctl runs the systemctl found in PATH.
func systemctl(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", args...).Run()
}

// Install writes the units when they are missing or differ, then reloads
// systemd and enables them. It is a no-op outside systemd and when not root.
func Install(o Options) error {
	if o.Service == "" || o.Binary == "" {
		return fmt.Errorf("watchdog: Service and Binary are required")
	}
	if o.UnitDir == "" {
		o.UnitDir = "/etc/systemd/system"
		if os.Geteuid() != 0 {
			log.Printf("[%s] not root, skipping watchdog install", o.Service)
			return nil
		}
		// Only a missing /run/systemd/system means "not systemd". Any other
		// stat error is no answer to that question, so the install goes on
		// and fails loudly at the write or the systemctl call if it must.
		if _, err := os.Stat("/run/systemd/system"); errors.Is(err, fs.ErrNotExist) {
			log.Printf("[%s] systemd not detected, skipping watchdog install", o.Service)
			return nil
		}
	}
	if o.BootDelaySec <= 0 {
		o.BootDelaySec = 15
	}
	if o.Systemctl == nil {
		o.Systemctl = systemctl
	}

	changed := false
	for name, content := range Units(o) {
		path := filepath.Join(o.UnitDir, name)
		if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("watchdog: write %s: %w", path, err)
		}
		changed = true
	}
	if !changed {
		return nil
	}
	if err := o.Systemctl("daemon-reload"); err != nil {
		log.Printf("[%s] systemctl daemon-reload: %v", o.Service, err)
	}
	for _, unit := range []string{o.Service + "-watchdog.timer", o.Service + "-refresh.path"} {
		if err := o.Systemctl("enable", "--now", unit); err != nil {
			log.Printf("[%s] systemctl enable %s: %v", o.Service, unit, err)
		}
	}
	log.Printf("[%s] watchdog timer and refresh path unit installed", o.Service)
	return nil
}

// Uninstall stops and disables the watchdog timer and refresh path unit and
// removes the four unit files Install wrote. Before it existed, `zpkg remove`
// left them behind in /etc (cron, zbackup — found 2026-10-02): a watchdog that
// keeps trying to start a service whose sysext is gone.
//
// Only the four files with the exact names Install uses are touched; a missing
// file is not an error (Uninstall is idempotent). Unlike Install it does not
// skip silently when not root — an uninstall that pretends to have worked is
// the failure it exists to prevent.
func Uninstall(o Options) error {
	if o.Service == "" {
		return fmt.Errorf("watchdog: Service is required")
	}
	if o.UnitDir == "" {
		o.UnitDir = "/etc/systemd/system"
		if os.Geteuid() != 0 {
			return fmt.Errorf("watchdog: uninstall needs root to remove units in %s", o.UnitDir)
		}
	}
	if o.Systemctl == nil {
		o.Systemctl = systemctl
	}
	for _, unit := range []string{o.Service + "-watchdog.timer", o.Service + "-refresh.path"} {
		if _, err := os.Stat(filepath.Join(o.UnitDir, unit)); err != nil {
			continue
		}
		if err := o.Systemctl("disable", "--now", unit); err != nil {
			log.Printf("[%s] systemctl disable %s: %v", o.Service, unit, err)
		}
	}
	removed := 0
	for name := range Units(Options{Service: o.Service}) {
		err := os.Remove(filepath.Join(o.UnitDir, name))
		switch {
		case err == nil:
			removed++
		case os.IsNotExist(err):
		default:
			return fmt.Errorf("watchdog: remove %s: %w", name, err)
		}
	}
	if removed > 0 {
		if err := o.Systemctl("daemon-reload"); err != nil {
			log.Printf("[%s] systemctl daemon-reload: %v", o.Service, err)
		}
		log.Printf("[%s] watchdog units removed (%d)", o.Service, removed)
	}
	return nil
}

// Units renders the four unit files keyed by file name.
func Units(o Options) map[string]string {
	s := o.Service
	return map[string]string{
		s + "-watchdog.service": fmt.Sprintf(`[Unit]
Description=Start %s if it is not running

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'systemctl is-active --quiet %s.service || systemctl start %s.service'
`, s, s, s),
		s + "-watchdog.timer": fmt.Sprintf(`[Unit]
Description=Start %s if the sysext unit was missed at boot

[Timer]
OnBootSec=%d

[Install]
WantedBy=timers.target
`, s, o.BootDelaySec),
		s + "-refresh.path": fmt.Sprintf(`[Unit]
Description=Watch the %s binary for updates

[Path]
PathChanged=%s

[Install]
WantedBy=multi-user.target
`, s, o.Binary),
		s + "-refresh.service": fmt.Sprintf(`[Unit]
Description=Restart %s after a binary update

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'sleep 2 && systemctl restart %s.service'
`, s, s),
	}
}
