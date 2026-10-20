#!/usr/bin/env bash
# ctl.sh — start, stop, restart, or check zcribe's systemd units.
#
# A thin wrapper, deliberately. systemd already is the process manager
# (Restart=always, the sandboxing directives in ../systemd/*.service) — this
# script exists so an operator does not have to remember two unit names and
# can act on "serve", "bot", or "all" instead.
#
# usage: ctl.sh <start|stop|restart|status|enable|disable> [serve|bot|all]

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
. ./_lib.sh

usage() {
	cat <<EOF
usage: $(basename "$0") <start|stop|restart|status|enable|disable> [serve|bot|all]

  start|stop|restart   act on the unit(s); requires root
  status                print systemctl status (read-only, no root needed)
  enable|disable        toggle start-on-boot; requires root

target defaults to "all" (both zcribe.service and zcribe-bot.service).
EOF
	exit 2
}

[ $# -ge 1 ] || usage
action="$1"; shift || true
units="$(resolve_target "${1:-all}")"

case "$action" in
	start|stop|restart|enable|disable)
		need_root "$action" "${1:-all}"
		for u in $units; do
			log "==> systemctl $action $u"
			systemctl "$action" "$u"
		done
		;;
	status)
		for u in $units; do
			log "==> $u"
			systemctl --no-pager status "$u" || true
			log ""
		done
		;;
	*)
		usage
		;;
esac
