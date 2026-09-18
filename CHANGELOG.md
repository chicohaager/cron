# Changelog

## 0.3.0 — 2026-09-18

### Fixed
- Cron day fields were ignored: any task bound to a weekday or a day of the
  month ran **every day** (#2, #4). Day matching now follows the POSIX
  rule; `*/n` in a day field is a restriction; month names and name
  ranges (`mon-fri`, `jan-mar`) work in the scheduler, not only in the
  validator; `a/n` runs from `a` to the field maximum.
- Data race between the interval ticker and pause/resume that could crash
  the whole service with a nil dereference.
- A retry of a deleted task could run its command once more and write the
  task back to disk, where it reappeared after a restart.
- "Clear history" was not persisted and came back after a restart.
- Export → import lost every interval task (`interval_ms` vs `interval_min`).
- `interval_ms` in the API carried nanoseconds.
- `next_run_at` in `tasks.json` was always the run that had just fired.
- `cron.service` waited on `casaos-*` units that do not exist on
  ZimaOS 1.7 (#1, by @ApertureDevelopment).
- The Docker cleanup template pruned volumes — the data of every stopped app.

### Added
- Session authentication: the API requires a ZimaOS access token; CORS
  reflection is gone and state-changing requests must be same-origin.
- Edit tasks in place (`PUT /tasks/{id}`) and choose a webhook format:
  generic, n8n, Discord, Slack, Home Assistant, Uptime Kuma (by @kennytat, #2).
- UI in English, German, French and Chinese, following the ZimaOS
  language; result and error messages are translated codes.
- New design in the ZFW design language with light and dark theme; stats
  row; confirm dialogs; import result names skipped tasks; larger app
  icon (#3).
- `allow_parallel` is real: overlapping runs are skipped and recorded.
- `priority` orders the task list.
- Dependency validation: unknown, self and cyclic dependencies are rejected.
- Run history stored per task in `logs/<id>.json`; pre-0.3 files are migrated.
- Non-execution outcomes (dependency not met, still running, queue full)
  appear as results, not only in the journal.
- arm64 build; `ARCHITECTURE` in the sysext release file; sha256 files;
  CI with `-race` tests and an i18n completeness check.
- `RequiresMountsFor=/DATA/AppData` and journal output in the unit.

### Changed
- API errors are JSON `{error, code}`.
- Export file is a versioned envelope in the request format.
- Webhooks may target private addresses again (n8n, Home Assistant and
  Uptime Kuma live on the LAN).
- `install-watchdog.sh` removed; the daemon installs the units itself.

## 0.2.0 — 2026-04-01

Persistence, retries, notifications, templates, categories/tags, bulk
operations, import/export, health endpoint, sysext watchdog.
