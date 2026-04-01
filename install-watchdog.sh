#!/bin/bash
set -euo pipefail

# Install cron watchdog timer to /etc/systemd/system/
# This survives sysext refreshes because /etc is persistent.

echo "Installing cron watchdog timer..."

cat > /etc/systemd/system/cron-watchdog.service << 'EOF'
[Unit]
Description=Restart cron if not running

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'systemctl is-active cron.service || systemctl start cron.service'
EOF

cat > /etc/systemd/system/cron-watchdog.timer << 'EOF'
[Unit]
Description=Ensure cron is running after sysext refresh

[Timer]
OnBootSec=15

[Install]
WantedBy=timers.target
EOF

# Path unit: auto-restart cron when binary changes (e.g. sysext refresh)
cat > /etc/systemd/system/cron-refresh.path << 'EOF'
[Unit]
Description=Watch cron binary for updates

[Path]
PathChanged=/usr/bin/cron

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/cron-refresh.service << 'EOF'
[Unit]
Description=Restart cron after binary update

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'sleep 2 && systemctl restart cron.service'
EOF

systemctl daemon-reload
systemctl enable --now cron-watchdog.timer
systemctl enable --now cron-refresh.path

echo "Done. Watchdog timer and refresh path unit installed."
echo "cron will auto-restart when the binary is updated."
