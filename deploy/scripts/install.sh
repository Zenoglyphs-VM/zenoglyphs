#!/usr/bin/env bash
# install.sh — first-time host setup, scripting README.md §4.
#
# Creates the service user, data and config directories, installs the
# binary and the systemd units, and reloads systemd. Idempotent: safe to run
# again after a manual change, it will just re-assert the same state.
#
# It does not start anything and does not touch /etc/zcribe/bot.env if one
# already exists — those are deploy-time and secret-bearing decisions
# respectively, not install-time ones.
#
# usage: install.sh -bin <path to a built zcribe binary>

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
. ./_lib.sh
need_root "$@"
need_cmd systemctl
need_cmd install

bin=""
while [ $# -gt 0 ]; do
	case "$1" in
		-bin) bin="$2"; shift 2 ;;
		*) die "unknown argument '$1' (usage: install.sh -bin <path>)" ;;
	esac
done
[ -n "$bin" ] || die "usage: install.sh -bin <path to a built zcribe binary>"
[ -x "$bin" ] || die "'$bin' does not exist or is not executable"

if ! getent group "$ZCRIBE_GROUP" >/dev/null; then
	log "==> creating group $ZCRIBE_GROUP"
	groupadd --system "$ZCRIBE_GROUP"
fi
if ! id "$ZCRIBE_USER" >/dev/null 2>&1; then
	log "==> creating user $ZCRIBE_USER"
	useradd --system --gid "$ZCRIBE_GROUP" --home "$ZCRIBE_HOME" \
		--shell /usr/sbin/nologin "$ZCRIBE_USER"
fi

log "==> creating $ZCRIBE_HOME"
install -d -o "$ZCRIBE_USER" -g "$ZCRIBE_GROUP" "$ZCRIBE_HOME"

log "==> creating $ZCRIBE_CONF_DIR"
install -d -m750 -o "$ZCRIBE_USER" -g "$ZCRIBE_GROUP" "$ZCRIBE_CONF_DIR"

if [ ! -f "$ZCRIBE_BOT_ENV" ]; then
	log "==> no $ZCRIBE_BOT_ENV yet — installing the example as a starting point"
	install -m600 -o "$ZCRIBE_USER" -g "$ZCRIBE_GROUP" ../bot.env.example "$ZCRIBE_BOT_ENV"
	log "    edit it before enabling $UNIT_BOT: ZCRIBE_TELEGRAM_TOKEN and ZCRIBE_TELEGRAM_CHANNEL are required."
else
	log "==> $ZCRIBE_BOT_ENV already exists, leaving it alone"
fi

log "==> installing binary to $ZCRIBE_BIN"
# cp-to-temp-then-rename, not a direct install/cp over the target: on a
# rerun where zcribe is already running, an in-place overwrite would
# truncate the file the running process still has mapped. See deploy.sh's
# header for the full reasoning — this is the same swap, done here too so
# rerunning this script is actually as safe as the "idempotent" claim above
# says it is. Use deploy.sh instead when the point is to ship a new build;
# it also restarts and verifies.
cp "$bin" "${ZCRIBE_BIN}.new"
chmod 755 "${ZCRIBE_BIN}.new"
mv -f "${ZCRIBE_BIN}.new" "$ZCRIBE_BIN"

log "==> installing systemd units"
install -m644 ../systemd/*.service "$UNIT_DIR"/

log "==> systemctl daemon-reload"
systemctl daemon-reload

log ""
log "done. next steps:"
log "  1. edit $ZCRIBE_BOT_ENV if you want the Telegram bot"
log "  2. review the ExecStart flags in $UNIT_DIR/$UNIT_SERVE (especially -start)"
log "  3. ./ctl.sh enable all && ./ctl.sh start all"
log "  4. ./doctor.sh"
