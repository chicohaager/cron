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
// production for cron since April 2026.
package watchdog

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
		if _, err := os.Stat("/run/systemd/system"); err != nil {
			log.Printf("[%s] systemd not detected, skipping watchdog install", o.Service)
			return nil
		}
	}
	if o.BootDelaySec <= 0 {
		o.BootDelaySec = 15
	}
	if o.Systemctl == nil {
		o.Systemctl = func(args ...string) error { return exec.Command("systemctl", args...).Run() }
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
