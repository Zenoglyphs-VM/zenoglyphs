#!/usr/bin/env bash
# deploy.sh — ship a new zcribe binary and restart the affected service(s).
#
# The replace is done with cp-to-temp-then-rename rather than an in-place
# overwrite: rename(2) is atomic, so a process that already has the old
# binary's inode open (the one systemctl is about to restart) keeps running
# the old bytes right up until the restart, instead of risking a segfault
# from having its executable truncated out from under it mid-read.
#
# Every deploy keeps exactly one prior binary at $ZCRIBE_BIN.bak, which is
# what -rollback restores. It is overwritten by the next deploy, so this is
# a one-step-back safety net, not a release history — keep real artifacts
# wherever you already keep build outputs if you need more than that.
#
# usage:
#   deploy.sh -bin <path to a built zcribe binary> [serve|bot|all]
#   deploy.sh -rollback [serve|bot|all]

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
. ./_lib.sh
need_root "$@"
need_cmd systemctl
need_cmd curl

mode="deploy"
bin=""
target="all"
while [ $# -gt 0 ]; do
	case "$1" in
		-bin) bin="$2"; shift 2 ;;
		-rollback) mode="rollback"; shift ;;
		serve|bot|all) target="$1"; shift ;;
		*) die "unknown argument '$1'" ;;
	esac
done
units="$(resolve_target "$target")"

verify() {
	local u="$1"
	sleep 2
	if ! systemctl is-active --quiet "$u"; then
		err "$u is not active after restart — 'journalctl -u $u -n 50' or ./logs.sh"
		return 1
	fi
	if [ "$u" = "$UNIT_SERVE" ]; then
		if ! curl -fsS -m 5 "$API_URL/info" >/dev/null; then
			err "$UNIT_SERVE is active but $API_URL/info did not answer"
			return 1
		fi
	fi
	log "    $u: ok"
	return 0
}

if [ "$mode" = "rollback" ]; then
	[ -f "${ZCRIBE_BIN}.bak" ] || die "no ${ZCRIBE_BIN}.bak to roll back to"
	log "==> rolling back to ${ZCRIBE_BIN}.bak"
	cp "${ZCRIBE_BIN}.bak" "${ZCRIBE_BIN}.new"
	chmod 755 "${ZCRIBE_BIN}.new"
	mv -f "${ZCRIBE_BIN}.new" "$ZCRIBE_BIN"
else
	[ -n "$bin" ] || die "usage: deploy.sh -bin <path to a built zcribe binary> [serve|bot|all]"
	[ -x "$bin" ] || die "'$bin' does not exist or is not executable"

	if [ -f "$ZCRIBE_BIN" ]; then
		log "==> backing up current binary to ${ZCRIBE_BIN}.bak"
		cp "$ZCRIBE_BIN" "${ZCRIBE_BIN}.bak"
	fi

	log "==> installing $bin -> $ZCRIBE_BIN"
	cp "$bin" "${ZCRIBE_BIN}.new"
	chmod 755 "${ZCRIBE_BIN}.new"
	mv -f "${ZCRIBE_BIN}.new" "$ZCRIBE_BIN"
fi

failed=0
for u in $units; do
	log "==> restarting $u"
	systemctl restart "$u"
	verify "$u" || failed=1
done

if [ "$failed" -ne 0 ]; then
	err "one or more units failed to come back healthy."
	err "the previous binary is still at ${ZCRIBE_BIN}.bak — recover with: $0 -rollback ${target}"
	exit 1
fi
log "deploy ok."
