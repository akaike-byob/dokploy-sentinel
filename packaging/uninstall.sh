#!/bin/sh
# dokploy-sentinel uninstaller — POSIX sh.
#
#   sudo sh uninstall.sh            # remove units + binary, ASK before deleting config/state
#   sudo sh uninstall.sh --purge    # also delete config + state, no prompt
#   sudo sh uninstall.sh --yes      # same as --purge (assume yes to the prompt)

set -e
set -o noglob

BIN_DEST="/usr/local/bin/dokploy-sentinel"
CONFIG_DIR="/etc/dokploy-sentinel"
STATE_DIR="/var/lib/dokploy-sentinel"
UNIT_DIR="/etc/systemd/system"
TIMER_DROPIN_DIR="${UNIT_DIR}/dokploy-sentinel.timer.d"

PURGE=0

log()  { printf '==> %s\n' "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		die "must run as root (re-run with sudo)"
	fi
}

parse_args() {
	for arg in "$@"; do
		case "$arg" in
			--purge | --yes | -y) PURGE=1 ;;
			-h | --help)
				grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//'
				exit 0
				;;
			*) die "unknown argument: ${arg}" ;;
		esac
	done
}

remove_units() {
	log "disabling and stopping the timer"
	systemctl disable --now dokploy-sentinel.timer 2>/dev/null || true
	systemctl stop dokploy-sentinel.service 2>/dev/null || true

	log "removing systemd units + binary"
	rm -f "${UNIT_DIR}/dokploy-sentinel.service" \
		"${UNIT_DIR}/dokploy-sentinel-failure.service" \
		"${UNIT_DIR}/dokploy-sentinel.timer"
	rm -rf "$TIMER_DROPIN_DIR"
	rm -f "$BIN_DEST"

	systemctl daemon-reload 2>/dev/null || true
}

remove_data() {
	if [ "$PURGE" -eq 1 ]; then
		answer="y"
	else
		printf 'Delete config (%s) and state (%s)? [y/N] ' "$CONFIG_DIR" "$STATE_DIR"
		read -r answer || answer=""
	fi

	case "$answer" in
		y | Y | yes | YES)
			rm -rf "$CONFIG_DIR" "$STATE_DIR"
			log "removed config + state"
			;;
		*)
			log "kept config (${CONFIG_DIR}) and state (${STATE_DIR})"
			;;
	esac
}

main() {
	require_root
	parse_args "$@"
	remove_units
	remove_data
	log "uninstall complete"
}

main "$@"
