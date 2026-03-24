# zima_cron

A lightweight, professional task scheduler for [ZimaOS](https://www.zimaos.com/) / [CasaOS](https://casaos.io/). Schedule commands with interval or cron expressions, manage execution with timeouts and retries, organize with categories and tags, and monitor with webhook notifications.

## Features

- **Interval & Cron Scheduling** — run commands every N minutes or via 5-field cron expressions
- **Persistent Storage** — tasks survive reboots (JSON file with atomic writes)
- **Configurable Timeouts & Retries** — per-task timeout, automatic retry on failure
- **Environment Variables** — inject custom env vars into task commands
- **Webhook Notifications** — get notified on success/failure via any HTTP endpoint
- **Categories, Tags & Priority** — organize and filter tasks
- **Task Dependencies** — skip execution when upstream tasks haven't succeeded
- **Log Management** — per-task logs with rotation, search, CSV/JSON export
- **Cron Validation** — real-time validation with next 5 execution times preview
- **Bulk Operations** — run, pause, or delete multiple tasks at once
- **Import/Export** — backup and restore all tasks as JSON
- **Health Endpoint** — `GET /zima_cron/health` for monitoring tools (Uptime Kuma, Zabbix)
- **Bilingual UI** — English and Chinese, switchable in the header

## Quick Start

### Install on ZimaOS

1. Download `zima_cron.raw` from [Releases](https://github.com/chicohaager/zima_cron/releases)
2. Copy to your ZimaOS device and merge:
   ```bash
   verschaffe dir einen codebase Überblick/to/extensions/
   ssh user@zimaos "sudo systemd-sysext merge && sudo systemctl restart zima-cron"
   ```
3. Open the Scheduler module in the ZimaOS dashboard

### Build from Source

```bash
git clone https://github.com/chicohaager/zima_cron.git
cd zima_cron

# Build binary
GOOS=linux GOARCH=amd64 go build -o raw/usr/bin/zima-cron ./cmd/zima-cron

# Build zpkg (requires squashfs-tools)
mksquashfs raw/ zima_cron.raw -noappend -comp gzip
```

### Local Development

```bash
export ZIMA_CRON_DATA_PATH=/tmp/zima_cron_data
go run ./cmd/zima-cron
```

## API Overview

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/zima_cron/tasks` | GET | List tasks (supports `?category=X&tag=Y`) |
| `/zima_cron/tasks` | POST | Create task |
| `/zima_cron/tasks/{id}` | GET | Get task details |
| `/zima_cron/tasks/{id}` | DELETE | Delete task |
| `/zima_cron/tasks/{id}/run` | POST | Run task once |
| `/zima_cron/tasks/{id}/toggle` | POST | Pause/resume task |
| `/zima_cron/tasks/{id}/logs` | GET | Get logs (`?format=csv&search=X`) |
| `/zima_cron/tasks/bulk/run` | POST | Bulk run tasks |
| `/zima_cron/tasks/bulk/toggle` | POST | Bulk pause/resume |
| `/zima_cron/tasks/bulk/delete` | POST | Bulk delete |
| `/zima_cron/cron/validate` | POST | Validate cron expression |
| `/zima_cron/export` | GET | Export all tasks as JSON |
| `/zima_cron/import` | POST | Import tasks from JSON |
| `/zima_cron/categories` | GET | List categories |
| `/zima_cron/tags` | GET | List tags |
| `/zima_cron/health` | GET | Health check |

See [FEATURES.md](FEATURES.md) for the complete API reference with curl examples.

## Task Model

```json
{
  "name": "Daily Backup",
  "command": "/usr/bin/backup.sh",
  "type": "cron",
  "cron_expr": "0 3 * * *",
  "timeout_sec": 300,
  "retry_count": 2,
  "retry_delay_sec": 60,
  "env": { "BACKUP_DIR": "/data/backups" },
  "category": "backup",
  "tags": ["critical", "daily"],
  "priority": 9,
  "depends_on": ["other-task-id"],
  "max_log_entries": 200,
  "notifications": [{
    "enabled": true,
    "type": "webhook",
    "target": "https://hooks.example.com/notify",
    "on_success": false,
    "on_failure": true
  }]
}
```

## Project Structure

```
zima_cron/
  cmd/zima-cron/main.go       # Entry point, HTTP handlers, scheduler
  internal/
    config/config.go           # CasaOS configuration
    service/service.go         # Gateway integration
    storage/storage.go         # JSON file persistence (atomic writes)
    notify/notify.go           # Webhook notification dispatcher
    cron/validate.go           # Cron expression validator
  raw/                         # ZimaOS system extension structure
    usr/bin/zima-cron           # Compiled binary
    usr/lib/systemd/system/     # Service file
    usr/share/casaos/           # Module config + web UI
  test_deployment.sh           # 29-test deployment verification script
  FEATURES.md                  # Detailed feature docs & API reference
```

## Testing

Run the deployment test suite against a live instance:

```bash
./test_deployment.sh http://your-zimaos-ip
```

Runs 29 automated tests covering all features with automatic cleanup.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `ZIMA_CRON_DATA_PATH` | `/DATA/AppData/zima_cron` | Persistent storage directory |
| `CASAOS_RUNTIME_PATH` | System default | CasaOS gateway path |

## Tech Stack

- **Backend:** Go 1.20+ (net/http, no frameworks)
- **Frontend:** Vanilla JavaScript, HTML, CSS (no frameworks)
- **Storage:** JSON file with atomic writes
- **Packaging:** systemd-sysext (squashfs `.raw`)

## License

Based on [LinkLeong/zima_cron](https://github.com/LinkLeong/zima_cron). Extended with professional features.

## Author:

Holger Kuehn
