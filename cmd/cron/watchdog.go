package main

import (
	"log"
	"os"
	"os/exec"
)

// installWatchdog writes the boot-time watchdog and the binary-change path
// unit to /etc/systemd/system, which on ZimaOS is persistent and — unlike
// the sysext that ships cron.service — already merged when multi-user.target
// is resolved. Without the timer the service is never scheduled at boot
// (see README, "systemd integration"). Units are rewritten only when their
// content differs, so an upgrade with changed units takes effect and an
// unchanged one is a no-op.
func installWatchdog() {
	if os.Geteuid() != 0 {
		log.Printf("[cron] not root, skipping watchdog install")
		return
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		log.Printf("[cron] Systemd not detected, skipping watchdog install")
		return
	}

	units := map[string]string{
		"/etc/systemd/system/cron-watchdog.service": `[Unit]
Description=Start cron if it is not running

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'systemctl is-active cron.service || systemctl start cron.service'
`,
		"/etc/systemd/system/cron-watchdog.timer": `[Unit]
Description=Start cron if the sysext unit was missed at boot

[Timer]
OnBootSec=15

[Install]
WantedBy=timers.target
`,
		"/etc/systemd/system/cron-refresh.path": `[Unit]
Description=Watch cron binary for updates

[Path]
PathChanged=/usr/bin/cron

[Install]
WantedBy=multi-user.target
`,
		"/etc/systemd/system/cron-refresh.service": `[Unit]
Description=Restart cron after binary update

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'sleep 2 && systemctl restart cron.service'
`,
	}

	changed := false
	for path, content := range units {
		if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			log.Printf("[cron] Could not write %s: %v", path, err)
			return
		}
		changed = true
	}
	if !changed {
		return
	}

	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		log.Printf("[cron] systemctl daemon-reload: %v", err)
	}
	for _, unit := range []string{"cron-watchdog.timer", "cron-refresh.path"} {
		if err := exec.Command("systemctl", "enable", "--now", unit).Run(); err != nil {
			log.Printf("[cron] systemctl enable %s: %v", unit, err)
		}
	}
	log.Printf("[cron] Watchdog timer and refresh path unit installed")
}
