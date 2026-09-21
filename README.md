# Cron — task scheduler for ZimaOS

[![ZimaOS](https://img.shields.io/badge/ZimaOS-Module-blue?style=flat-square)](https://github.com/IceWhaleTech/ZimaOS)
[![License](https://img.shields.io/badge/License-Apache%202.0-green?style=flat-square)](LICENSE)

Run commands on an interval or a cron expression, keep a history of every
run, and get notified when something fails — from a small dashboard inside
ZimaOS. Ships as a systemd-sysext module (`cron.raw`) and is listed in the
ZimaOS Module Store as `zima_cron`.

![Dashboard](images/dashboard.png)

## Features

- **Schedules in words** — daily at, weekly on, monthly on day, every
  hour, every N minutes; the next run times appear while you pick. A
  5-field cron expression (`0 5 1 jan,jul *`, `mon-fri`, …) is there for
  everything else, with live validation.
- **Reliable day handling** — weekday and day-of-month fields follow the
  POSIX rule; `*/2` in a day field is a restriction. (Versions before 0.3.0
  ran every weekday-bound task daily — see the changelog.)
- **Persistent** — tasks and their history survive restarts and reboots.
- **History per task** — output, duration and result of every run, with
  search, CSV/JSON export and a small success/failure sparkline.
- **Edit in place** — change command, schedule or notifications without
  losing the history.
- **Templates** — AppData backup, temp cleanup, health check, Docker
  cleanup (volumes are kept), version report, TLS expiry, container status.
- **Retries and timeouts** per task; **environment variables**; **overlap
  protection** (a run that starts while the previous one is still going is
  skipped unless you allow parallel runs); **dependencies** (run only if
  the tasks it depends on succeeded last time — cycles are rejected);
  **priority** (orders the list); categories and tags with filters.
- **Notifications** — global Telegram; per task webhook (generic JSON,
  n8n, Discord, Slack, Home Assistant, Uptime Kuma push) and e-mail (SMTP).
  Success and failure can be selected separately.
- **Import / export** — a versioned JSON file that round-trips without
  loss; rejected entries are named in the result.
- **Sync & Backup jobs in the same list** — if the
  [Sync & Backup](https://github.com/chicohaager/zima-backup) module is
  installed, its jobs appear in a second card below the tasks: name,
  backup or sync, schedule in words, next run and last result, read from
  the module with the same session (`GET /v2/zbackup/api/jobs`). Cron
  only shows them — starting, editing and running stays in Sync & Backup,
  which keeps its own scheduler, so nothing exists twice. Without the
  module (404 at the gateway) the card is not there.
- **Four languages** — English, German, French, Chinese. The UI follows the
  language of the ZimaOS shell and can be switched in the header.
- **Light and dark theme** in the same design language as ZFW.
- **Authenticated** — every API call needs a valid ZimaOS session token;
  the API is not reachable from the LAN without logging in to ZimaOS.

## Installation

Requires ZimaOS 1.7.x on amd64 or arm64.

**Module Store:** open *Settings → Module Store* in ZimaOS and install
*zima_cron*, or on the host:

```sh
zpkg install zima_cron
```

**Manual:** download `cron-amd64.raw` (or `cron-arm64.raw`) from the
[releases](https://github.com/chicohaager/cron/releases), copy it to the
host **as `cron.raw`** and run

```sh
sudo zpkg install /tmp/cron.raw
```

`zpkg` accepts the image only under the name that matches
`extension-release.cron` inside it; any other file name is refused with
"module not pass validate" (measured on ZimaOS 1.7.1).

The service starts by itself and appears as **Cron** on the ZimaOS
dashboard.

### Upgrading from 0.2.x

```sh
sudo zpkg remove cron && sudo zpkg install /tmp/cron.raw
```

Tasks, settings and history are kept (`/DATA/AppData/cron`). On the first
start 0.3.0 moves the run history out of `tasks.json` into
`logs/<id>.json`; the old file is left untouched until the next save.
`next_run_at` of every cron task is recomputed with the corrected day
rule, so a weekly task that used to fire daily fires weekly from now on.

## Usage

### Your first task in five clicks

1. Open **Cron** from the dashboard and click **New task**.
2. Pick a template — *System Health Check* is a good first one — or type
   a name and a command. Commands run as root via `/bin/sh -c`, with
   `/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin` on the path and the
   host's timezone.
3. Choose **when** from the list: *Daily at 03:00*, *Weekly on Sunday*,
   *Monthly on day 1*, *Every hour*, *Every N minutes*. The next three run
   times appear under the field. A cron expression is the last entry,
   for those who want one.
4. **Create.** The task is active; **Run** tries it right now.

Per row: **Run** (now), **Edit**, **Pause/Resume**, **History**, **Delete**.
*Run all now* triggers every active task. The gear opens global settings
(Telegram). *Export* downloads all tasks as JSON; *Import* reads such a
file — from 0.3.x or from 0.2.x — and creates the tasks paused.

### Templates

| Template | What it does |
|---|---|
| AppData Backup | Archives `/DATA/AppData` to `/DATA/backups/appdata_<date>.tar.gz` |
| Cleanup Temp Files | Removes files older than 7 days from `/tmp` |
| System Health Check | Disk space, memory and load average |
| Docker Cleanup | Removes unused images, containers and networks — volumes are kept |
| System Update Check | Prints the ZimaOS release and kernel |
| SSL Certificate Expiry Check | Certificate expiry of a domain (edit the host) |
| Docker Container Status | Every container with state and resource usage |

### Schedule examples

| You want | Pick |
|---|---|
| a nightly backup | *Daily at* 03:00 |
| a weekly report on Monday morning | *Weekly on* Monday, 07:00 |
| a check every quarter hour | *Every N minutes* 15 |
| the 1st of the month | *Monthly on day* 1, 04:00 |
| weekdays only, or two times a day | *Cron expression*: `0 6 * * mon-fri`, `0 6,18 * * *` |

## systemd integration

`cron.raw` is a systemd-sysext image. It contributes:

| Path | Purpose |
|---|---|
| `/usr/bin/cron` | the daemon |
| `/usr/lib/systemd/system/cron.service` | ordered after `zimaos-gateway`, `zimaos-message-bus`, `zimaos-user`; `RequiresMountsFor=/DATA/AppData`; `Restart=always` |
| `/usr/share/casaos/modules/cron.json` | dashboard tile |
| `/usr/share/casaos/www/modules/cron/` | the UI, served by the ZimaOS gateway |

On ZimaOS, units inside a sysext are not scheduled at boot: `multi-user.target`
is resolved before the extension is merged into `/usr`. The daemon therefore
writes two small units to the persistent `/etc/systemd/system/` on first
start and enables them:

- `cron-watchdog.timer` (`OnBootSec=15`) → `cron-watchdog.service`: starts
  `cron.service` if it is not active after boot.
- `cron-refresh.path` (`PathChanged=/usr/bin/cron`) → `cron-refresh.service`:
  restarts the daemon two seconds after the binary changed, so a module
  upgrade takes effect without a reboot.

The units are rewritten only when their content changes. The gateway
route `/cron` is registered in the background and retried, so the daemon
never waits for the gateway.

ZimaOS also ships a stock `dcron` daemon; it only runs `run-parts` over
the empty `/etc/cron.*` directories and has no user interface. This module
does not touch it.

## Data

```
/DATA/AppData/cron/
  tasks.json       task definitions and last result
  logs/<id>.json   run history of one task
  settings.json    global settings (Telegram)
```

## Building

```sh
./build.sh amd64      # or arm64 — needs Go 1.22+ and squashfs-tools
```

produces `cron-<arch>.raw` plus a `.sha256`. `go test -race ./...` and
`python3 tools/check-i18n.py` are what CI runs.

For local development without a ZimaOS gateway:

```sh
CRON_DISABLE_AUTH=1 CRON_DATA_PATH=/tmp/cron-dev \
CRON_STATIC_DIR=$PWD/raw/usr/share/casaos/www/modules/cron go run ./cmd/cron
```

The UI is then at `http://127.0.0.1:<port>/modules/cron/` (port in the log).

## API

See [FEATURES.md](FEATURES.md). Every call except `/cron/health` needs
`Authorization: Bearer <ZimaOS access token>`.

## Contributing

Issues and pull requests are welcome. The French translation was drafted
by a non-native speaker — corrections are especially welcome.

## Credits

- [IceWhale Technology](https://github.com/IceWhaleTech) — ZimaOS
- [@ApertureDevelopment](https://github.com/ApertureDevelopment) — unit
  names for ZimaOS 1.7 (#1)
- [@kennytat](https://github.com/kennytat) — task editing and webhook
  formats (#2)
- everyone who reported the weekday bug (#2, #4)

Apache License 2.0 — see [LICENSE](LICENSE).
