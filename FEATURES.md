# cron v0.2.0 — Feature List & How-To

A lightweight task scheduler for ZimaOS/CasaOS with professional features.

## Table of Contents

- [Features](#features)
- [Installation](#installation)
- [UI Guide](#ui-guide)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [Testing](#testing)

---

## Features

### 1. Persistent Task Storage

Tasks are saved to `/DATA/AppData/cron/tasks.json` and survive service restarts. All writes use atomic file operations (write to `.tmp`, then rename) for crash safety.

- Auto-save on every create, update, delete, toggle, and run
- Auto-load on startup with schedule restoration

### 2. Configurable Timeouts & Retry Logic

Each task can define its own execution timeout, retry behavior, and environment variables.

| Field | Default | Description |
|-------|---------|-------------|
| `timeout_sec` | 120 | Kill command after N seconds |
| `retry_count` | 0 | Max retry attempts on failure |
| `retry_delay_sec` | 10 | Seconds between retries |
| `env` | — | Key-value map injected into command environment |

Retries execute automatically after failure. Notifications only fire after the final attempt.

### 3. Notifications (Webhook, Email, Telegram)

Get notified when tasks succeed or fail. Three notification channels available:

**Per-task Webhook & Email:**
```json
{
  "notifications": [{
    "enabled": true,
    "type": "webhook",
    "target": "https://your-webhook.example.com/hook",
    "on_success": false,
    "on_failure": true
  }, {
    "enabled": true,
    "type": "email",
    "target": "admin@example.com",
    "smtp_host": "smtp.gmail.com",
    "smtp_port": 587,
    "smtp_user": "user@gmail.com",
    "smtp_pass": "app-password",
    "on_success": false,
    "on_failure": true
  }]
}
```

**Global Telegram Notifications:**

Configure once in Settings (gear icon), applies to all tasks:
- `PUT /cron/settings` — set bot token, chat ID, trigger conditions
- `POST /cron/settings/test-telegram` — send a test message

**Webhook payload:**
```json
{
  "event": "task_completed",
  "task": { "id": "...", "name": "Backup", "command": "..." },
  "result": { "success": true, "message": "...", "duration_ms": 1250 },
  "timestamp": 1710000000
}
```

Works with n8n, Home Assistant, Discord webhooks, Slack, Uptime Kuma, or any HTTP endpoint.

### 4. Categories, Tags & Priority

Organize tasks with metadata:

| Field | Type | Example |
|-------|------|---------|
| `category` | string | `"backup"`, `"monitoring"` |
| `tags` | string[] | `["critical", "daily"]` |
| `priority` | int (1-10) | `8` |

Filter tasks by category or tag in the API and UI. Categories and tags auto-populate from existing tasks.

### 5. Task Dependencies

Tasks can depend on other tasks. A dependent task will skip execution (with a "dependency not met" result) if any of its dependencies has not succeeded.

```json
{
  "depends_on": ["task-id-1", "task-id-2"],
  "allow_parallel": false
}
```

Missing dependencies are silently ignored (graceful degradation).

### 6. Log Management

Each task keeps an execution log with automatic rotation.

| Feature | Description |
|---------|-------------|
| Max entries | Configurable per task (`max_log_entries`, default: 100) |
| Search | Full-text search via `?search=keyword` |
| Time range | Filter via `?from=timestamp&to=timestamp` |
| CSV export | Download logs as CSV via `?format=csv` |
| Clear | Delete all logs for a task |

CSV export sanitizes cell content against formula injection.

### 7. Cron Expression Validation

Full validation of 5-field cron expressions with field-level error messages.

**Supported syntax:**
- Wildcards: `*`
- Steps: `*/5`, `1-10/2`
- Ranges: `1-5`
- Lists: `1,3,5,10-20`
- Weekday names: `mon`, `tue`, ..., `sat`, `sun`
- Month names: `jan`, `feb`, ..., `dec`

**Frontend:** Real-time validation as you type, showing the next 5 execution times for valid expressions.

### 8. Live Execution Indicator

When a task is running, the UI shows a **pulsing blue dot** with a glow animation and "Executing..." status text. The frontend auto-polls every 2 seconds while any task is active, then stops polling when all tasks are idle.

The `executing` boolean field is included in the task JSON response.

### 9. Task Templates

Pre-built templates for common tasks, accessible via the "Start from Template" dropdown:

| Template | Description |
|----------|-------------|
| AppData Backup | Archive `/DATA/AppData` to `/DATA/backups/` |
| Cleanup Temp Files | Remove files older than 7 days from `/tmp` |
| System Health Check | Disk, memory, and load average |
| Docker Cleanup | Prune unused images, containers, volumes |
| System Update Check | Show OS and kernel info |
| SSL Certificate Check | Check certificate expiry |
| Docker Container Status | List containers with resource usage |

Templates pre-fill the task creation form. Customize before creating.

### 10. Global Settings

Configure global notification settings via the Settings panel (gear icon in header):

- Telegram Bot Token and Chat ID
- Global on-success / on-failure triggers
- Test button to verify Telegram connectivity

Settings are persisted to `/DATA/AppData/cron/settings.json`.

### 11. Sysext Watchdog Timer

ZimaOS uses systemd-sysext which stops services during refresh. A watchdog timer is auto-installed to `/etc/systemd/system/` on first start:

- One-shot check 45 seconds after boot (gateway needs ~45s to register routes)
- Starts cron automatically if not running after boot
- Survives sysext refresh because `/etc/` is persistent
- No periodic polling — runs only once per boot to save resources

### 12. Async Gateway Registration

The HTTP server starts immediately without waiting for the CasaOS gateway. Gateway route registration happens in the background with 60 retry attempts (2s interval). This prevents startup deadlocks when running inside a sysext package.

### 13. Clean Output Handling

On successful execution (exit code 0), only stdout is captured as the log message. stderr noise (e.g., `tar: Removing leading '/'`) is discarded. On failure, both stdout and stderr are combined for full diagnostic context.

### 14. API Improvements

**Bulk operations** — act on multiple tasks at once:
- `POST /tasks/bulk/run` — trigger execution
- `POST /tasks/bulk/toggle` — pause/resume
- `POST /tasks/bulk/delete` — delete

**Import/Export** — backup and migrate tasks:
- `GET /export` — download all tasks as JSON
- `POST /import` — import tasks from JSON (created as paused)

**Health endpoint** — for monitoring tools:
- `GET /health` — returns status, version, uptime, task counts

**Templates:**
- `GET /templates` — list available task templates

**Settings:**
- `GET /settings` — get global settings (Telegram config)
- `PUT /settings` — update global settings
- `POST /settings/test-telegram` — send test Telegram message

---

## Installation

### From Release (ZimaOS)

1. Download `cron.raw` from the releases page
2. Install via zpkg:
   ```bash
   scp cron.raw root@zimaos:/tmp/
   ssh root@zimaos 'zpkg remove cron; zpkg install /tmp/cron.raw && systemctl start cron'
   ```
3. The watchdog timer auto-installs on first start and ensures the service starts after boot.

### Build from Source

```bash
git clone https://github.com/chicohaager/cron.git
cd cron

# Build .raw package (requires Go 1.21+ and squashfs-tools)
./build.sh amd64
```

Or manually:
```bash
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o raw/usr/bin/cron ./cmd/cron
mksquashfs raw/ cron.raw -noappend -comp gzip
```

### Local Development

```bash
# Set a local data path (avoids writing to /DATA/)
export CRON_DATA_PATH=/tmp/cron_data

# Run (will fail gateway registration gracefully)
go run ./cmd/cron
```

---

## UI Guide

### Creating a Task

1. Click **New Task**
2. Fill in name and command
3. Choose schedule type:
   - **Interval** — runs every N minutes
   - **Cron** — standard 5-field cron expression (with live validation)
4. Optionally set category, tags, priority
5. Expand **Advanced Options** for:
   - Timeout, retry count, retry delay
   - Environment variables (key-value editor)
   - Max log entries
   - Task dependencies (select from existing tasks)
   - Webhook notification URL + triggers
6. Click **Create**

### Task List

- **Filter** by category or tag using the dropdowns above the table
- **Run Once** — trigger immediate execution
- **Pause/Resume** — toggle the schedule
- **Show Logs** — expand inline log viewer with search, CSV/JSON export
- **Delete** — remove the task

### Log Viewer

- Click **Show Logs** on any task
- Use the search box to filter by keyword
- **Export CSV** / **Export JSON** — download log data
- **Clear Logs** — delete all entries

---

## API Reference

Base path: `/cron`

### Tasks

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/tasks` | List all tasks |
| `GET` | `/tasks?category=X` | Filter by category |
| `GET` | `/tasks?tag=X` | Filter by tag |
| `POST` | `/tasks` | Create a task |
| `GET` | `/tasks/{id}` | Get single task |
| `DELETE` | `/tasks/{id}` | Delete a task |
| `POST` | `/tasks/{id}/run` | Run task once |
| `POST` | `/tasks/{id}/toggle` | Pause/resume task |

#### Create Task

```bash
curl -X POST http://zimaos/cron/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Daily Backup",
    "command": "/usr/bin/backup.sh",
    "type": "cron",
    "cron_expr": "0 3 * * *",
    "timeout_sec": 300,
    "retry_count": 2,
    "retry_delay_sec": 60,
    "env": {"BACKUP_DIR": "/data/backups"},
    "category": "backup",
    "tags": ["critical", "daily"],
    "priority": 9,
    "max_log_entries": 200,
    "notifications": [{
      "enabled": true,
      "type": "webhook",
      "target": "https://hooks.example.com/backup",
      "on_success": false,
      "on_failure": true
    }]
  }'
```

### Logs

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/tasks/{id}/logs` | Get execution logs (JSON) |
| `GET` | `/tasks/{id}/logs?format=csv` | Download as CSV |
| `GET` | `/tasks/{id}/logs?search=error` | Search log messages |
| `GET` | `/tasks/{id}/logs?from=T&to=T` | Filter by timestamp range |
| `POST` | `/tasks/{id}/logs/clear` | Delete all logs |

### Bulk Operations

```bash
# Run multiple tasks
curl -X POST http://zimaos/cron/tasks/bulk/run \
  -H "Content-Type: application/json" \
  -d '{"ids": ["id1", "id2", "id3"]}'

# Toggle (pause/resume) multiple tasks
curl -X POST http://zimaos/cron/tasks/bulk/toggle \
  -H "Content-Type: application/json" \
  -d '{"ids": ["id1", "id2"]}'

# Delete multiple tasks
curl -X POST http://zimaos/cron/tasks/bulk/delete \
  -H "Content-Type: application/json" \
  -d '{"ids": ["id1", "id2"]}'
```

### Cron Validation

```bash
curl -X POST http://zimaos/cron/cron/validate \
  -H "Content-Type: application/json" \
  -d '{"expr": "*/5 * * * *"}'
```

Response:
```json
{
  "valid": true,
  "errors": [],
  "next_runs": [1710000300000, 1710000600000, 1710000900000, 1710001200000, 1710001500000]
}
```

### Import / Export

```bash
# Export all tasks
curl http://zimaos/cron/export -o tasks_backup.json

# Import tasks (created as paused)
curl -X POST http://zimaos/cron/import \
  -H "Content-Type: application/json" \
  -d @tasks_backup.json
```

### Metadata

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/categories` | List all categories in use |
| `GET` | `/tags` | List all tags in use |

### Health

```bash
curl http://zimaos/cron/health
```

```json
{
  "status": "healthy",
  "version": "0.2.0",
  "uptime_seconds": 86400,
  "tasks_total": 15,
  "tasks_running": 12,
  "tasks_paused": 3,
  "last_execution": 1710000000
}
```

Use this with Uptime Kuma, Zabbix, or any HTTP health checker.

### Templates

```bash
curl http://zimaos/cron/templates
```

Returns a list of pre-built task templates that can be used to pre-fill the creation form.

### Settings

```bash
# Get current settings
curl http://zimaos/cron/settings

# Update Telegram config
curl -X PUT http://zimaos/cron/settings \
  -H "Content-Type: application/json" \
  -d '{"telegram_bot_token": "123:ABC", "telegram_chat_id": "-100123", "telegram_on_failure": true}'

# Test Telegram
curl -X POST http://zimaos/cron/settings/test-telegram \
  -H "Content-Type: application/json" \
  -d '{"bot_token": "123:ABC", "chat_id": "-100123"}'
```

---

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CRON_DATA_PATH` | `/DATA/AppData/cron` | Storage directory for tasks.json and settings.json |
| `CASAOS_RUNTIME_PATH` | (system default) | CasaOS gateway runtime path |

### Data Files

```
/DATA/AppData/cron/
  tasks.json          # All task definitions and state
  settings.json       # Global settings (Telegram config)
```

---

## Testing

Run the included deployment test script against a live instance:

```bash
./test_deployment.sh http://your-zimaos-ip
```

This runs 29 automated tests covering all features:
- Health endpoint
- Task CRUD with all fields
- Environment variable injection
- Notification config persistence
- Category/tag filtering
- Dependencies
- Log search, CSV export, clear
- Cron validation (valid + invalid)
- Bulk operations
- Import/Export
- Cleanup

All test tasks are automatically deleted after the run.
