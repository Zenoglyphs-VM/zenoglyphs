#!/usr/bin/env bash
# doctor.sh — read-only health check for both units. No root required unless
# -telegram is given (it needs to read the 600-mode bot.env).
#
# Exit status is 0 only if every check passed, so this is safe to put on a
# cron and alert on nonzero — it is meant to catch "the unit is active but
# the thing it's supposed to be doing has actually stalled", which
# `systemctl is-active` alone cannot tell you.
#
# usage: doctor.sh [-telegram]

cd "$(dirname "${BASH_SOURCE[0]}")"
. ./_lib.sh
# _lib.sh turns on -e and pipefail for the scripts that want fail-fast
# semantics; this one deliberately does not, since its whole job is to run
# every check and report all of them, not stop at the first failure.
set +e
set +o pipefail
set -u
need_cmd curl
need_cmd systemctl

check_telegram=0
[ "${1:-}" = "-telegram" ] && check_telegram=1

fail=0
ok()   { log "  [ok] $*"; }
bad()  { err "  [FAIL] $*"; fail=1; }
note() { log "  [--] $*"; }

log "== units =="
for u in "$UNIT_SERVE" "$UNIT_BOT"; do
	if ! systemctl list-unit-files "$u" >/dev/null 2>&1; then
		note "$u is not installed (run install.sh)"
		continue
	fi
	if systemctl is-active --quiet "$u"; then
		ok "$u is active"
	else
		bad "$u is not active ($(systemctl is-active "$u" 2>&1 || true))"
	fi
done

log "== indexer / API =="
info="$(curl -fsS -m 5 "$API_URL/info" 2>/dev/null || true)"
if [ -z "$info" ]; then
	bad "$API_URL/info did not answer"
else
	sync_h="$(json_field syncHeight "$info")"
	node_h="$(json_field nodeHeight "$info")"
	confirmations="$(json_field confirmationDepth "$info")"
	retain="$(json_field snapshotRetain "$info")"
	scripts_on="$(json_field scriptsEnabled "$info")"
	content_origin="$(json_field contentOrigin "$info")"

	if [ -n "$sync_h" ] && [ -n "$node_h" ]; then
		lag=$((node_h - sync_h))
		max_lag="${DOCTOR_MAX_LAG:-30}"
		if [ "$lag" -le "$max_lag" ]; then
			ok "synced: $sync_h / $node_h (lag $lag momentums)"
		else
			bad "sync lag is $lag momentums (> $max_lag) — syncHeight=$sync_h nodeHeight=$node_h"
		fi
	else
		bad "could not read syncHeight/nodeHeight from /info"
	fi

	if [ -n "$confirmations" ] && [ -n "$retain" ]; then
		if [ "$confirmations" -le "$retain" ]; then
			ok "confirmations ($confirmations) <= snapshot-retain ($retain), per README §7"
		else
			bad "confirmations ($confirmations) > snapshot-retain ($retain) — a reorg deeper than retain cannot be recovered before it is reported final; see README §7"
		fi
	fi

	note "scriptsEnabled=$scripts_on"

	if [ -n "$content_origin" ]; then
		code="$(curl -s -o /dev/null -m 5 -w '%{http_code}' "$content_origin" 2>/dev/null || echo 000)"
		if [ "$code" != "000" ]; then
			ok "content origin $content_origin reachable (HTTP $code)"
		else
			bad "content origin $content_origin unreachable"
		fi
	fi
fi

log "== bot =="
if systemctl list-unit-files "$UNIT_BOT" >/dev/null 2>&1; then
	if [ ! -f "$ZCRIBE_BOT_ENV" ]; then
		bad "$ZCRIBE_BOT_ENV is missing"
	else
		mode="$(stat -c '%a' "$ZCRIBE_BOT_ENV" 2>/dev/null || echo '?')"
		if [ "$mode" = "600" ]; then
			ok "$ZCRIBE_BOT_ENV is mode 600"
		else
			bad "$ZCRIBE_BOT_ENV is mode $mode, expected 600"
		fi

		if [ "$check_telegram" -eq 1 ]; then
			if [ ! -r "$ZCRIBE_BOT_ENV" ]; then
				note "cannot read $ZCRIBE_BOT_ENV to check the token (run as root, or drop -telegram)"
			else
				token="$(grep -E '^ZCRIBE_TELEGRAM_TOKEN=' "$ZCRIBE_BOT_ENV" | head -n1 | cut -d= -f2-)"
				if [ -z "$token" ] || [ "$token" = "123456:replace-me" ]; then
					bad "ZCRIBE_TELEGRAM_TOKEN in $ZCRIBE_BOT_ENV is unset or still the example value"
				else
					resp="$(curl -fsS -m 10 "https://api.telegram.org/bot${token}/getMe" 2>/dev/null || true)"
					case "$resp" in
						*'"ok":true'*) ok "Telegram getMe succeeded — token is live" ;;
						*) bad "Telegram getMe failed: ${resp:-no response}" ;;
					esac
				fi
			fi
		else
			note "pass -telegram to also verify the bot token against api.telegram.org"
		fi
	fi
else
	note "$UNIT_BOT not installed — skipping"
fi

log ""
if [ "$fail" -eq 0 ]; then
	log "all checks passed."
else
	log "one or more checks failed."
fi
exit "$fail"
