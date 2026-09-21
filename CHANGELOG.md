# Changelog

## 0.3.2 — unreleased

### Added
- A read-only card **Sync & Backup** below the tasks, when that module is
  installed: name, backup or sync, schedule in words, next run and last
  result of every job, fetched from `/v2/zbackup/api/jobs` with the
  shell's session (measured on ZimaOS 1.7.1: 200 with the token, 401
  without, 404 when the module is absent — then the card stays hidden; a
  module that answers an error shows the error in the card). *Open Sync &
  Backup* links to the module; Cron does not start, edit or run those
  jobs — the module keeps its own scheduler, so no job exists twice.
- Spanish as the fifth UI language (all 198 strings).

## 0.3.1 — unreleased

### Changed
- The schedule is picked as words — *Daily at*, *Weekly on*, *Monthly on
  day*, *Every hour*, *Every N minutes* — and the list shows it the same
  way (`0 3 * * *` reads "daily at 03:00"). A cron expression is the last
  entry; expressions the words cannot say (`0 3 * * 1,5`) stay expressions.
- README: the templates table and a five-click walkthrough are back.

### Fixed
- Import refused every interval task of a 0.2.x export file
  (`interval_ms` in nanoseconds, no `interval_min`); such files import
  completely now.
- A timeout killed only `/bin/sh`, not the command it had started: a run
  with `sleep 30` and a 1 s timeout took 30 s. The whole process group is
  ended now.
- A tick that arrived while the task was still running set the task's
  result to "skipped" for the duration of the run; it is a history entry
  only now, the running attempt's result stands.
- A DELETE between the registry check and the write in `persistTask`
  could re-create the deleted task on disk.
- The inline history of 0.2.x task files was migrated again on every
  restart until the first change; it is dropped from `tasks.json` right
  after the migration now.
- Changing the recipient of an e-mail notification lost the stored SMTP
  password when the form echoed the mask.
- The session token is renewed through the shell's refresh endpoint when
  it expires; the "reload ZimaOS" banner only appears if that fails.

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
- An interval above 2^53 minutes (what a mistyped number field can send)
  overflowed to a negative duration and panicked the scheduler with the
  registry lock held; every later request hung until a restart. Intervals,
  timeouts and retry delays are now capped at one year.
- The release asset `cron.raw.sha256` named `cron-amd64.raw`, so
  `sha256sum -c` could not verify the download.

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
