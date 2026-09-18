# Cron — API reference (v0.3.0)

Base path: `/cron` on the ZimaOS host (port 80/443, proxied by the gateway).
All endpoints except `GET /cron/health` require a ZimaOS session token:

```
Authorization: Bearer <access_token>
```

The web UI takes it from `localStorage.access_token`; for scripts obtain
one with `POST /v1/users/login` (`{"username","password"}` →
`data.token.access_token`). State-changing requests from a browser must be
same-origin (the `Origin` header has to match the host); non-browser
clients without `Origin` are fine.

Errors are JSON:

```json
{ "error": "invalid cron expression: 99 is out of range 0-59 for minute", "code": "cron_invalid" }
```

`code` is stable and translated by the UI: `name_required`,
`command_required`, `type_invalid`, `interval_invalid`, `cron_invalid`,
`cron_never_fires`, `priority_invalid`, `value_negative`,
`notification_invalid`, `dependency_self`, `dependency_unknown`,
`dependency_cycle`, `task_limit`, `cross_origin`, `bad_json`,
`auth_required`, `auth_invalid`, `not_found`, `method_not_allowed`.

## Task object (responses)

```json
{
  "id": "60fe579c2c79d6ad",
  "name": "Nightly backup",
  "command": "bash /DATA/scripts/backup.sh",
  "type": "cron",                 "cron_expr": "0 3 * * 1",
  "interval_min": 30,             "interval_ms": 1800000,     // interval tasks only
  "status": "running",            "executing": false,
  "next_run_at": 1789869600000,   "last_run_at": 1789783200000,   // unix ms
  "last_result": { "success": true, "code": "completed", "message": "…stdout…" },
  "timeout_sec": 300, "retry_count": 2, "retry_delay_sec": 60, "current_retry": 0,
  "env": { "BACKUP_DIR": "/DATA/backups" },
  "notifications": [ … credentials masked as "********" … ],
  "category": "backup", "tags": ["critical"], "priority": 9,
  "depends_on": [], "allow_parallel": false, "max_log_entries": 100
}
```

Result codes: `completed`, `exit_error`, `timeout`, `skipped_dependency`,
`skipped_running` (overlap, `allow_parallel` off), `skipped_queue_full`
(more than 10 commands running for 30 s).

## Task request (create, edit, import entries)

```json
{
  "name": "Nightly backup",
  "command": "bash /DATA/scripts/backup.sh",
  "type": "cron",                 "cron_expr": "0 3 * * 1",
  "interval_min": 30,             // when type is "interval"
  "timeout_sec": 300, "retry_count": 2, "retry_delay_sec": 60,
  "env": { "BACKUP_DIR": "/DATA/backups" },
  "category": "backup", "tags": ["critical"], "priority": 9,
  "depends_on": ["<task id>"], "allow_parallel": false, "max_log_entries": 100,
  "notifications": [
    { "enabled": true, "type": "webhook", "target": "https://n8n.local/webhook/x",
      "webhook_format": "n8n", "on_success": false, "on_failure": true },
    { "enabled": true, "type": "email", "target": "admin@example.com",
      "smtp_host": "smtp.example.com", "smtp_port": 587,
      "smtp_user": "user", "smtp_pass": "…", "on_failure": true }
  ]
}
```

Cron syntax: 5 fields, `*`, lists, ranges, steps (`*/n`, `a-b/n`, `a/n`),
weekday names `sun`–`sat` (0 and 7 are Sunday), month names `jan`–`dec`,
in values and ranges. Day-of-month and weekday combine the POSIX way: if
both are restricted, either matches.

On edit, a notification credential sent as `"********"` keeps the stored
value.

## Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/tasks?category=&tag=` | list, ordered by priority (10 first) then name |
| `POST` | `/tasks` | create → 201 + task |
| `GET` | `/tasks/{id}` | one task |
| `PUT` | `/tasks/{id}` | edit (full request body); reschedules if the schedule changed |
| `DELETE` | `/tasks/{id}` | delete task and history → 204 |
| `POST` | `/tasks/{id}/run` | run now → 202 |
| `POST` | `/tasks/{id}/toggle` | pause / resume → task |
| `GET` | `/tasks/{id}/logs?search=&from=&to=&format=csv` | history, newest first |
| `POST` | `/tasks/{id}/logs/clear` | → 204 |
| `POST` | `/tasks/bulk/run` · `/bulk/toggle` · `/bulk/delete` | body `{"ids":[…]}` → `{"affected":n}` |
| `GET` | `/categories` · `/tags` | values in use |
| `POST` | `/cron/validate` | body `{"expr":"…"}` → `{valid, errors[], next_runs[]}` |
| `GET` | `/export` | `{"version":1,"exported_at":ms,"tasks":[request objects]}` |
| `POST` | `/import` | export file or bare array → `{"imported":n,"skipped":[{name,reason,code}]}`; tasks are created paused |
| `GET` | `/templates` | built-in templates |
| `GET` / `PUT` | `/settings` | global Telegram settings (token masked on read; masked value on write keeps it) |
| `POST` | `/settings/test-telegram` | body `{"bot_token","chat_id"}` → `{success, error?}` |
| `GET` | `/health` | `{status, version, uptime_seconds, tasks_total, tasks_running, tasks_paused, last_execution}` — no token needed |

## Webhook payloads

| `webhook_format` | Body |
|---|---|
| `generic` (default) | `{"event":"task_completed","task":{id,name,command},"result":{success,message,duration_ms},"timestamp"}` |
| `n8n` | flat: `{event, task_id, task_name, command, success, message, duration_ms, timestamp}` |
| `discord` | `{"content": "✅ SUCCESS — Name (1250ms)\n```output```"}` |
| `slack` | `{"text": …same message…}` |
| `home_assistant` | `{message, task_name, success, duration_ms, output, timestamp}` |
| `uptime_kuma` | push URL called with `?status=up|down&msg=…&ping=<ms>` |

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `CRON_DATA_PATH` | `/DATA/AppData/cron` | storage directory |
| `CRON_STATIC_DIR` | `/usr/share/casaos/www/modules/cron` | UI files served at `/modules/cron/` on the daemon's own port |
| `CASAOS_RUNTIME_PATH` | CasaOS default | where `management.url` of the gateway lives |
| `CRON_DISABLE_AUTH` | unset | `1` disables the session check — development only, logged loudly |

Limits: 500 tasks, 10 concurrent commands, 1 MB request body, 4000
characters of output kept per run, 100 history entries per task by default.

## Testing

```sh
go test -race ./...                       # unit + handler tests
python3 tools/check-i18n.py               # every key in every language
ZIMA_USER=admin ZIMA_PASS=… ./test_deployment.sh http://<host>   # 30 checks against a live host
```
