#!/usr/bin/env bash
# logs.sh — tail or search zcribe's journald output.
#
# Both units log to stdout/stderr (systemd's default), so journalctl is
# already the log store; this just saves typing -u twice and picking the
# right unit name.
#
# usage: logs.sh [serve|bot|all] [journalctl args...]
#   logs.sh                        # follow both units
#   logs.sh bot                    # follow just the bot
#   logs.sh serve -n 200 --no-pager  # last 200 lines from serve, no pager

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
. ./_lib.sh
need_cmd journalctl

target="all"
case "${1:-}" in
	serve|bot|all) target="$1"; shift ;;
esac

units="$(resolve_target "$target")"
args=()
for u in $units; do
	args+=(-u "$u")
done

# Default to following; a caller who passed their own journalctl flags is
# asking for something other than a live tail, so -f is only added when
# nothing else was given.
if [ $# -eq 0 ]; then
	exec journalctl "${args[@]}" -f
fi
exec journalctl "${args[@]}" "$@"
