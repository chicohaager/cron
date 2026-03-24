#!/bin/bash
set -euo pipefail

# Install zima-cron watchdog timer to /etc/systemd/system/
# This survives sysext refreshes because /etc is persistent.

echo "Installing zima-cron watchdog timer..."

cat > /etc/systemd/system/zima-cron-watchdog.service << 'EOF'
[Unit]
Description=Restart zima-cron if not running

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'systemctl is-active zima-cron.service || systemctl start zima-cron.service'
EOF

cat > /etc/systemd/system/zima-cron-watchdog.timer << 'EOF'
[Unit]
Description=Ensure zima-cron is running after sysext refresh

[Timer]
OnBootSec=45
OnUnitActiveSec=30

[Install]
WantedBy=timers.target
EOF

systemctl daemon-reload
systemctl enable --now zima-cron-watchdog.timer

echo "Done. Watchdog timer installed and started."
echo "zima-cron will be checked 45s after boot, then every 30s."
