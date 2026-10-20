# Operating zcribe

Six scripts for running the two zcribe units — `zcribe.service` (indexer,
API, content origin) and `zcribe-bot.service` (the Telegram front end) — on a
Linux host that already has systemd. They are day-2 tooling, not a
deployment guide: for the reverse proxy, the CDN, and the reasoning behind
the two-hostname split, see [`../README.md`](../README.md). These scripts
assume you've read §1–§2 of it (or are about to, via `install.sh`).

None of them replace systemd — `Restart=always` and the sandboxing
directives already live in [`../systemd/*.service`](../systemd/). What's
here is the ten seconds of typing that wrapping saves: the right unit name,
the right journalctl flags, a binary swap that won't crash a running
process, and a health check that looks past "the unit is active" to "the
indexer is actually caught up."

They all need to run *on* the host — they operate on local systemd units and
`127.0.0.1:8710`, not over SSH to somewhere else.

## The scripts

| Script | Root? | What it does |
|---|---|---|
| `install.sh -bin <path>` | yes | First-time setup: service user/group, `/var/lib/zcribe`, `/etc/zcribe`, installs the binary and the unit files, `daemon-reload`. Idempotent — rerun it after any manual change to re-assert the same state. Never touches an existing `bot.env`. |
| `deploy.sh -bin <path> [serve\|bot\|all]` | yes | Ships a new binary and restarts the affected unit(s), then verifies: `is-active`, and for `serve`, that `/info` actually answers. Keeps one prior binary at `zcribe.bak` for `-rollback`. |
| `deploy.sh -rollback [serve\|bot\|all]` | yes | Restores `zcribe.bak` and restarts. The one-step-back safety net for a deploy that verified fine but misbehaves once it's live. |
| `ctl.sh <start\|stop\|restart\|status\|enable\|disable> [serve\|bot\|all]` | start/stop/restart/enable/disable: yes. status: no | Acts on one or both units by name instead of you having to remember `zcribe.service` vs `zcribe-bot.service`. |
| `logs.sh [serve\|bot\|all] [journalctl args...]` | no | Follows the journal by default (`-f`); pass your own flags (`-n 200`, `--since`, `--no-pager`, …) to do something else instead. |
| `doctor.sh [-telegram]` | no (yes for `-telegram`) | Read-only health check. Exit 0 only if every check passed — safe to cron and alert on. |

Every script that takes a target defaults to `all` (both units) except
`deploy.sh`, which requires you to say which one you mean when rolling out a
change — a default there would make "just restart the bot" too easy to
accidentally turn into "restart the indexer too."

`_lib.sh` is not a script — it's the shared constants (paths, unit names,
`API_URL`) and helpers the other six source. Change a path once there rather
than in six places.

## Typical workflows

**First install on a fresh host**, after copying this repo and a built
`zcribe` binary over:

```bash
sudo ./install.sh -bin ./zcribe
sudoedit /etc/zcribe/bot.env                     # token + channel, if you want the bot
sudoedit /etc/systemd/system/zcribe.service      # check -start and the other ExecStart flags
sudo ./ctl.sh enable all
sudo ./ctl.sh start all
./doctor.sh
```

**Routine restart** (e.g. after an nginx/Caddy config change that doesn't
touch zcribe itself):

```bash
sudo ./ctl.sh restart all
./doctor.sh
```

**Shipping a new build**:

```bash
sudo ./deploy.sh -bin ./zcribe serve   # or: bot, or: all
./doctor.sh
# if doctor.sh (or your users) find something wrong that -bin's own
# verification didn't catch:
sudo ./deploy.sh -rollback serve
```

**Checking on it** (by hand, or on a cron with alerting on nonzero exit):

```bash
./doctor.sh              # unit status, sync lag, the confirmations/retain
                          # invariant from ../README.md §7, content-origin
                          # reachability
./doctor.sh -telegram    # also calls getMe against the live token — a real
                          # network call to Telegram, so it's opt-in rather
                          # than something a routine cron run does by default
./logs.sh                # follow both units
./logs.sh bot -n 200 --no-pager
```

## What `doctor.sh` actually checks, and why

`systemctl is-active` tells you the process didn't crash. It does not tell
you the indexer stopped making progress against a node that's still
reachable, that the confirmation depth was configured deeper than the reorg
window it can recover from, or that the bot's token was revoked. Those are
the failure modes that are silent until a user notices, so `doctor.sh` checks
for them directly:

- **sync lag** — `nodeHeight − syncHeight` from `/info`, failing past
  `DOCTOR_MAX_LAG` momentums (default 30; set the env var to change it).
- **the confirmations/snapshot-retain inequality** — `../README.md` §7:
  `zcribe serve` itself warns at startup if this is inverted, but that warning
  is easy to miss in a log stream. This re-checks it from the live `/info`.
- **content origin reachability** — a proxy or firewall change can take this
  down without touching the indexer process at all.
- **bot.env permissions** — must be mode 600; systemd's `EnvironmentFile=`
  doesn't enforce that, a misconfigured deploy step could.
- **the Telegram token, with `-telegram`** — the only check here that makes an
  outbound call, which is why it's opt-in.

## A note on `deploy.sh`'s binary swap

Overwriting a running executable in place (what `cp` without
`--remove-destination`, or a naive `install`, can do) truncates the file
while the kernel may still have it mapped for the running process — a
recipe for a segfault on the next page fault, not a clean restart. This
script instead copies the new binary to `zcribe.new` beside the target and
`mv`s it over — `rename(2)` is atomic, and the running process keeps reading
its own already-open inode, completely undisturbed, until `systemctl
restart` actually replaces it. This is the same reason package managers and
`ExecStartPre=` binary swaps use rename rather than truncate-and-rewrite.
