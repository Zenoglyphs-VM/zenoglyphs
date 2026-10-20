#!/usr/bin/env bash
# _lib.sh — shared constants and helpers for the scripts in this directory.
# Not meant to be run directly; every other script sources it.
#
# The paths here are not a config surface — they are the paths the systemd
# units in ../systemd/*.service and the install steps in ../README.md already
# hard-code. Keeping one copy here means a renamed path only has to change in
# one place instead of silently drifting between a script and a unit file.

set -euo pipefail

ZCRIBE_BIN="/usr/local/bin/zcribe"
ZCRIBE_USER="zcribe"
ZCRIBE_GROUP="zcribe"
ZCRIBE_HOME="/var/lib/zcribe"
ZCRIBE_CONF_DIR="/etc/zcribe"
ZCRIBE_BOT_ENV="${ZCRIBE_CONF_DIR}/bot.env"
UNIT_DIR="/etc/systemd/system"

UNIT_SERVE="zcribe.service"
UNIT_BOT="zcribe-bot.service"

API_URL="${ZCRIBE_API_URL:-http://127.0.0.1:8710}"

log()  { printf '%s\n' "$*"; }
err()  { printf 'error: %s\n' "$*" >&2; }
die()  { err "$*"; exit 1; }

need_root() {
	[ "$(id -u)" -eq 0 ] || die "run as root (or with sudo): $0${*:+ $*}"
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "'$1' is required but not on PATH"
}

# resolve_target expands the service-selector argument every script takes:
# "serve", "bot", or "all" (the default). Echoes the matching unit names,
# space-separated.
resolve_target() {
	case "${1:-all}" in
		serve) echo "$UNIT_SERVE" ;;
		bot)   echo "$UNIT_BOT" ;;
		all)   echo "$UNIT_SERVE $UNIT_BOT" ;;
		*) die "unknown target '$1' — expected serve, bot, or all" ;;
	esac
}

# json_field extracts one top-level scalar field from a small flat JSON
# object without depending on jq being installed on the box. /info's fields
# are all top-level scalars (no nested objects share a field name with the
# ones this repo's scripts look at), so a bounded regex is enough — this is
# not a general JSON parser and must not be asked to be one.
json_field() {
	local field="$1" json="$2"
	printf '%s' "$json" | grep -o "\"${field}\"[[:space:]]*:[[:space:]]*\"\{0,1\}[^,}\"]*\"\{0,1\}" \
		| head -n1 | sed -E 's/^"[^"]+"[[:space:]]*:[[:space:]]*"?([^"]*)"?$/\1/'
}
