#!/bin/sh
# dokploy-sentinel installer — POSIX sh.
#
# Quick install (interactive — prompts for the webhook + settings on a terminal):
#   curl -fsSL https://raw.githubusercontent.com/akaike-byob/dokploy-sentinel/main/packaging/install.sh | sh
#
# Automated / non-interactive install (settings via flags or env — no prompts):
#   curl -fsSL .../install.sh | sh -s -- --slack-url 'https://hooks.slack.com/services/...'
#   SLACK_URL='https://hooks.slack.com/...' HOST_LABEL=web1 sh install.sh --non-interactive
#
# Settings — each has a --flag and an ENV var; flags win. On a fresh install with a
# terminal and none of these set, the installer prompts for them (reads /dev/tty, so
# it works under `curl | sh`). Pass --non-interactive (or -y) to never prompt.
#   --slack-url URL        / SLACK_URL         one webhook for all tiers (the common case)
#   --slack-warn-url URL   / SLACK_WARN_URL    per-tier webhooks (each falls back to --slack-url)
#   --slack-alert-url URL  / SLACK_ALERT_URL
#   --slack-page-url URL   / SLACK_PAGE_URL
#   --host-label LABEL     / HOST_LABEL        alert label (empty -> the node hostname)
#   --mention M            / MENTION           e.g. <@U123>; added only on PAGE
#   --heartbeat-url URL    / HEARTBEAT_URL     dead-man's-switch ping; empty = off
#   --interval 30s         / INTERVAL          timer cadence (drop-in; survives upgrades)
#   --version v1.2.3       / VERSION           pin a release (default: latest)
#   --repo owner/name      / REPO              source repo (default: akaike-byob/dokploy-sentinel)
#   --non-interactive, -y  / NON_INTERACTIVE=1 never prompt
#
# On a fresh install with no webhook supplied, the example config (placeholder URLs)
# is written and the timer is left DISABLED until you set a real webhook. An existing
# config is never overwritten (upgrades keep it).
#
# Upgrade (same command — it is idempotent):
#   curl -fsSL .../install.sh | sh                 # to the latest release
#   VERSION=v1.2.3 curl -fsSL .../install.sh | sh  # to a specific release
#   Re-running replaces the binary + units and KEEPS your config + state; the
#   timer picks up the new binary on its next tick. Your config is re-validated
#   before the timer is re-armed, so a config the new version rejects is caught.
#
# Uninstall:
#   curl -fsSL .../install.sh | sh -s -- --uninstall
#
# ---------------------------------------------------------------------------
# MANUAL / AIR-GAPPED INSTALL (no curl | sh) — the first-class alternative:
#
#   1. From https://github.com/akaike-byob/dokploy-sentinel/releases download, for your
#      arch, the archive dokploy-sentinel_<version>_linux_<amd64|arm64>.tar.gz plus
#      SHA256SUMS (and, if you verify signatures, SHA256SUMS.minisig).
#   2. Verify the checksums file signature (optional but recommended):
#        minisign -Vm SHA256SUMS -P <published-public-key>
#   3. Verify the archive against the checksums:
#        sha256sum -c SHA256SUMS 2>/dev/null | grep -F "$(uname -m)"   # or: sha256sum -c SHA256SUMS
#   4. Extract and install:
#        tar -xzf dokploy-sentinel_*_linux_*.tar.gz
#        sudo install -m 0755 dokploy-sentinel /usr/local/bin/dokploy-sentinel
#        sudo install -m 0644 packaging/systemd/dokploy-sentinel*.service \
#                             packaging/systemd/dokploy-sentinel.timer /etc/systemd/system/
#        sudo install -D -m 0600 packaging/config.example.toml /etc/dokploy-sentinel/config.toml
#        sudo mkdir -p /var/lib/dokploy-sentinel
#   5. Edit /etc/dokploy-sentinel/config.toml, then:
#        sudo dokploy-sentinel check --config /etc/dokploy-sentinel/config.toml
#        sudo systemctl daemon-reload
#        sudo systemctl enable --now dokploy-sentinel.timer
# ---------------------------------------------------------------------------

set -e
set -o noglob

# ---- configuration ----------------------------------------------------------
REPO="${REPO:-akaike-byob/dokploy-sentinel}"
VERSION="${VERSION:-latest}"
INTERVAL="${INTERVAL:-}"

# Delivery settings (may be set via env or the matching --flags; prompted
# interactively on a fresh install when a terminal is available).
SLACK_URL="${SLACK_URL:-}"                 # one webhook for all tiers
SLACK_WARN_URL="${SLACK_WARN_URL:-}"       # per-tier overrides (optional)
SLACK_ALERT_URL="${SLACK_ALERT_URL:-}"
SLACK_PAGE_URL="${SLACK_PAGE_URL:-}"
HOST_LABEL="${HOST_LABEL:-}"               # empty -> the node hostname
HEARTBEAT_URL="${HEARTBEAT_URL:-}"         # dead-man's-switch; empty = off
MENTION="${MENTION:-}"                      # e.g. <@U123>; added only on PAGE
NON_INTERACTIVE="${NON_INTERACTIVE:-}"      # 1 = never prompt

BIN_NAME="dokploy-sentinel"
BIN_DEST="/usr/local/bin/${BIN_NAME}"
CONFIG_DIR="/etc/dokploy-sentinel"
CONFIG_FILE="${CONFIG_DIR}/config.toml"
STATE_DIR="/var/lib/dokploy-sentinel"
UNIT_DIR="/etc/systemd/system"
TIMER_DROPIN_DIR="${UNIT_DIR}/dokploy-sentinel.timer.d"

log()  { printf '==> %s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		die "must run as root (re-run with sudo)"
	fi
}

detect_arch() {
	arch="$(uname -m)"
	case "$arch" in
		x86_64 | amd64) echo "amd64" ;;
		aarch64 | arm64) echo "arm64" ;;
		*) die "unsupported architecture: ${arch}" ;;
	esac
}

detect_os() {
	# OS is informational; the build targets linux only.
	if [ -r /etc/os-release ]; then
		# shellcheck disable=SC1091
		. /etc/os-release
		log "detected OS: ${PRETTY_NAME:-${NAME:-unknown}}"
	fi
	[ "$(uname -s)" = "Linux" ] || die "dokploy-sentinel supports Linux only"
}

# Resolve VERSION=latest to a concrete tag via the GitHub releases API.
resolve_version() {
	if [ "$VERSION" != "latest" ]; then
		echo "$VERSION"
		return
	fi
	api="https://api.github.com/repos/${REPO}/releases/latest"
	tag="$(curl -fsSL "$api" | grep -m1 '"tag_name"' | cut -d'"' -f4)"
	[ -n "$tag" ] || die "could not resolve latest release tag from ${api}"
	echo "$tag"
}

download() {
	# download URL DEST
	curl -fsSL -o "$2" "$1" || die "download failed: $1"
}

# Verify the downloaded binary/archive against the published SHA256SUMS. When minisign
# and a published pubkey are available, verify the checksums file signature first.
verify_checksums() {
	# verify_checksums FILE FILENAME_IN_SUMS SUMS_FILE
	file="$1"
	name="$2"
	sums="$3"

	# Extract the expected hash for this filename from SHA256SUMS.
	expected="$(grep -F "  ${name}" "$sums" | head -n1 | cut -d' ' -f1)"
	[ -n "$expected" ] || die "no checksum for ${name} in SHA256SUMS"

	if command -v sha256sum >/dev/null 2>&1; then
		actual="$(sha256sum "$file" | cut -d' ' -f1)"
	elif command -v shasum >/dev/null 2>&1; then
		actual="$(shasum -a 256 "$file" | cut -d' ' -f1)"
	else
		die "no sha256 tool found (sha256sum or shasum)"
	fi

	[ "$expected" = "$actual" ] || die "checksum mismatch for ${name}: expected ${expected}, got ${actual}"
	log "sha256 verified: ${name}"
}

verify_signature() {
	# verify_signature SUMS_FILE SIG_FILE
	sums="$1"
	sig="$2"
	pubkey="${CONFIG_DIR}/minisign.pub"

	if ! command -v minisign >/dev/null 2>&1; then
		warn "minisign not installed — skipping signature check (SHA256 still enforced)"
		return
	fi
	if [ ! -f "$sig" ]; then
		warn "no SHA256SUMS.minisig published — skipping signature check"
		return
	fi
	if [ ! -f "$pubkey" ]; then
		warn "no pubkey at ${pubkey} — skipping signature check (place one to enforce)"
		return
	fi
	minisign -Vm "$sums" -p "$pubkey" -x "$sig" >/dev/null 2>&1 ||
		die "minisign signature verification failed for SHA256SUMS"
	log "minisign signature verified"
}

install_binary() {
	arch="$1"
	version="$2"
	tmpdir="$3"

	# The release download URL path uses the git tag (e.g. v1.2.3), but goreleaser strips
	# the leading 'v' from asset names (dokploy-sentinel_1.2.3_linux_amd64.tar.gz).
	ver_num="${version#v}"
	base="https://github.com/${REPO}/releases/download/${version}"
	archive="${BIN_NAME}_${ver_num}_linux_${arch}.tar.gz"

	log "downloading ${archive} (${version}, ${arch})"
	download "${base}/${archive}"        "${tmpdir}/${archive}"
	download "${base}/SHA256SUMS"        "${tmpdir}/SHA256SUMS"
	# Signature is optional; ignore download failure.
	curl -fsSL -o "${tmpdir}/SHA256SUMS.minisig" "${base}/SHA256SUMS.minisig" 2>/dev/null || true

	verify_signature "${tmpdir}/SHA256SUMS" "${tmpdir}/SHA256SUMS.minisig"
	verify_checksums "${tmpdir}/${archive}" "${archive}" "${tmpdir}/SHA256SUMS"

	# Only after verification do we unpack and touch the filesystem.
	tar -xzf "${tmpdir}/${archive}" -C "${tmpdir}"
	[ -f "${tmpdir}/${BIN_NAME}" ] || die "binary ${BIN_NAME} missing from archive"

	install -m 0755 "${tmpdir}/${BIN_NAME}" "$BIN_DEST"
	log "installed ${BIN_DEST}"

	# Stage the packaged units + example config from the extracted archive.
	STAGED_PKG="${tmpdir}/packaging"
}

have_delivery_settings() {
	[ -n "${SLACK_URL}${SLACK_WARN_URL}${SLACK_ALERT_URL}${SLACK_PAGE_URL}" ]
}

# should_prompt: only on a fresh install, with a terminal, when nothing was given
# via flags/env and --non-interactive was not passed. Reads from /dev/tty so it
# works even under `curl … | sh` (where stdin is the script, not the terminal).
should_prompt() {
	[ "$NON_INTERACTIVE" = "1" ] && return 1
	have_delivery_settings && return 1
	[ -f "$CONFIG_FILE" ] && return 1
	[ -r /dev/tty ] || return 1
	return 0
}

prompt_settings() {
	log "Interactive setup — press Enter to accept a default; leave the webhook empty to configure later."
	printf 'Slack incoming webhook URL: ' >/dev/tty
	IFS= read -r SLACK_URL </dev/tty || SLACK_URL=""
	printf 'Host label (Enter = this host'"'"'s hostname): ' >/dev/tty
	IFS= read -r HOST_LABEL </dev/tty || HOST_LABEL=""
	printf 'PAGE mention, e.g. <@U123> (optional): ' >/dev/tty
	IFS= read -r MENTION </dev/tty || MENTION=""
	printf 'Heartbeat URL, dead-man'"'"'s-switch (optional): ' >/dev/tty
	IFS= read -r HEARTBEAT_URL </dev/tty || HEARTBEAT_URL=""
	printf 'Timer interval [%s]: ' "${INTERVAL:-60s}" >/dev/tty
	IFS= read -r _iv </dev/tty || _iv=""
	if [ -n "$_iv" ]; then INTERVAL="$_iv"; fi
}

# emit_target NAME URL [MENTION] — append a [targets.NAME] block to the config.
emit_target() {
	printf '\n[targets.%s]\nurl = "%s"\n' "$1" "$2" >>"$CONFIG_FILE"
	if [ -n "$3" ]; then
		printf 'mention = "%s"\n' "$3" >>"$CONFIG_FILE"
	fi
}

# gen_tier NAME URL [MENTION] — emit a target if URL is non-empty and echo the
# routing token (["NAME"] or []) for that tier.
gen_tier() {
	if [ -n "$2" ]; then
		emit_target "$1" "$2" "$3"
		printf '["%s"]' "$1"
	else
		printf '[]'
	fi
}

# generate_config writes a minimal config.toml from the delivery settings. All
# check blocks are omitted, so the binary applies its built-in defaults.
generate_config() {
	umask 077
	{
		printf '# dokploy-sentinel config — generated by install.sh.\n'
		printf '# Full reference (every check + option): %s.example\n' "$CONFIG_FILE"
		printf '# Re-run the installer to change delivery, or edit + validate:\n'
		printf '#   dokploy-sentinel check --config %s\n' "$CONFIG_FILE"
		printf '# Every check uses its built-in default unless you add a [checks.*] block.\n\n'
		printf 'host_label    = "%s"\n' "$HOST_LABEL"
		printf 'heartbeat_url = "%s"\n' "$HEARTBEAT_URL"
	} >"$CONFIG_FILE"

	if [ -n "${SLACK_WARN_URL}${SLACK_ALERT_URL}${SLACK_PAGE_URL}" ]; then
		# Per-tier delivery (each falls back to the shared --slack-url).
		uw="${SLACK_WARN_URL:-$SLACK_URL}"
		ua="${SLACK_ALERT_URL:-$SLACK_URL}"
		up="${SLACK_PAGE_URL:-$SLACK_URL}"
		rw="$(gen_tier warn "$uw" "")"
		ra="$(gen_tier alert "$ua" "")"
		rp="$(gen_tier page "$up" "$MENTION")"
		{
			printf '\n[routing]\n'
			printf 'WARN  = %s\n' "$rw"
			printf 'ALERT = %s\n' "$ra"
			printf 'PAGE  = %s\n' "$rp"
		} >>"$CONFIG_FILE"
	else
		# One webhook for all tiers (the common case).
		emit_target slack "$SLACK_URL" "$MENTION"
		{
			printf '\n[routing]\n'
			printf 'WARN  = ["slack"]\n'
			printf 'ALERT = ["slack"]\n'
			printf 'PAGE  = ["slack"]\n'
		} >>"$CONFIG_FILE"
	fi
	chmod 0600 "$CONFIG_FILE"
}

install_config() {
	mkdir -p "$CONFIG_DIR"
	chmod 0700 "$CONFIG_DIR"
	mkdir -p "$STATE_DIR"
	staged_example="${STAGED_PKG}/config.example.toml"
	DELIVERY_READY=0

	# Always drop the full annotated reference next to the config.
	if [ -f "$staged_example" ]; then
		install -m 0600 "$staged_example" "${CONFIG_FILE}.example"
	fi

	if [ -f "$CONFIG_FILE" ]; then
		# Upgrade: never clobber an existing config; refresh the reference.
		if [ -f "$staged_example" ]; then
			install -m 0600 "$staged_example" "${CONFIG_FILE}.new"
		fi
		log "existing config kept at ${CONFIG_FILE} (latest reference: ${CONFIG_FILE}.new)"
		DELIVERY_READY=1
		return
	fi

	if have_delivery_settings; then
		generate_config
		log "wrote ${CONFIG_FILE} (0600) with your delivery settings; full reference at ${CONFIG_FILE}.example"
		DELIVERY_READY=1
	elif [ -f "$staged_example" ]; then
		install -m 0600 "$staged_example" "$CONFIG_FILE"
		warn "no webhook provided — installed the example config with PLACEHOLDER URLs (timer NOT armed)"
		warn "set a real webhook in ${CONFIG_FILE}, then:"
		warn "  dokploy-sentinel check --config ${CONFIG_FILE} && systemctl enable --now dokploy-sentinel.timer"
	else
		warn "no config source available — skipping config bootstrap"
	fi
}

install_units() {
	src="${STAGED_PKG}/systemd"
	[ -d "$src" ] || die "systemd units not found in archive at ${src}"
	install -m 0644 "${src}/dokploy-sentinel.service"         "${UNIT_DIR}/dokploy-sentinel.service"
	install -m 0644 "${src}/dokploy-sentinel-failure.service" "${UNIT_DIR}/dokploy-sentinel-failure.service"
	install -m 0644 "${src}/dokploy-sentinel.timer"          "${UNIT_DIR}/dokploy-sentinel.timer"
	log "installed systemd units into ${UNIT_DIR}"
}

install_interval_dropin() {
	[ -n "$INTERVAL" ] || return 0
	mkdir -p "$TIMER_DROPIN_DIR"
	# OnUnitActiveSec is reset (empty line) then set so re-runs don't stack values.
	cat >"${TIMER_DROPIN_DIR}/interval.conf" <<EOF
[Timer]
OnUnitActiveSec=
OnUnitActiveSec=${INTERVAL}
EOF
	log "timer interval override set to ${INTERVAL} (drop-in survives upgrades)"
}

arm() {
	# Don't arm until delivery is configured (placeholder config -> user must edit).
	if [ "${DELIVERY_READY:-0}" != "1" ]; then
		systemctl daemon-reload || true
		warn "units installed but NOT enabled — configure a webhook first (see messages above)"
		return 0
	fi
	# Refuse to enable on an invalid config.
	if "$BIN_DEST" check --config "$CONFIG_FILE"; then
		log "config validated"
	else
		warn "config at ${CONFIG_FILE} is invalid — units installed but NOT enabled"
		warn "fix it, then: dokploy-sentinel check --config ${CONFIG_FILE} && systemctl enable --now dokploy-sentinel.timer"
		systemctl daemon-reload || true
		return 0
	fi

	systemctl daemon-reload
	systemctl enable --now dokploy-sentinel.timer
	log "timer enabled and started"

	# Optional immediate run so the operator sees a first report + validates their webhook
	# without waiting for the first tick.
	log "running once now to produce a first report (rate checks show 'no baseline yet')"
	systemctl start dokploy-sentinel.service || warn "immediate run reported an error — check: journalctl -u dokploy-sentinel.service"
}

parse_install_flags() {
	while [ $# -gt 0 ]; do
		case "$1" in
			--slack-url)         SLACK_URL="$2"; shift 2 ;;
			--slack-url=*)       SLACK_URL="${1#*=}"; shift ;;
			--slack-warn-url)    SLACK_WARN_URL="$2"; shift 2 ;;
			--slack-warn-url=*)  SLACK_WARN_URL="${1#*=}"; shift ;;
			--slack-alert-url)   SLACK_ALERT_URL="$2"; shift 2 ;;
			--slack-alert-url=*) SLACK_ALERT_URL="${1#*=}"; shift ;;
			--slack-page-url)    SLACK_PAGE_URL="$2"; shift 2 ;;
			--slack-page-url=*)  SLACK_PAGE_URL="${1#*=}"; shift ;;
			--host-label)        HOST_LABEL="$2"; shift 2 ;;
			--host-label=*)      HOST_LABEL="${1#*=}"; shift ;;
			--heartbeat-url)     HEARTBEAT_URL="$2"; shift 2 ;;
			--heartbeat-url=*)   HEARTBEAT_URL="${1#*=}"; shift ;;
			--mention)           MENTION="$2"; shift 2 ;;
			--mention=*)         MENTION="${1#*=}"; shift ;;
			--interval)          INTERVAL="$2"; shift 2 ;;
			--interval=*)        INTERVAL="${1#*=}"; shift ;;
			--version)           VERSION="$2"; shift 2 ;;
			--version=*)         VERSION="${1#*=}"; shift ;;
			--repo)              REPO="$2"; shift 2 ;;
			--repo=*)            REPO="${1#*=}"; shift ;;
			--non-interactive|--yes|-y) NON_INTERACTIVE=1; shift ;;
			*) die "unknown install option: $1 (see --help)" ;;
		esac
	done
}

do_install() {
	parse_install_flags "$@"
	require_root
	need_cmd curl
	need_cmd tar
	need_cmd systemctl
	detect_os

	arch="$(detect_arch)"
	version="$(resolve_version)"

	# Detect an existing install so we can report upgrade vs fresh install.
	prev=""
	if [ -x "$BIN_DEST" ]; then
		prev="$("$BIN_DEST" version 2>/dev/null | awk '{print $2}')"
	fi
	if [ -n "$prev" ]; then
		log "upgrading dokploy-sentinel ${prev} -> ${version} (linux/${arch}); config + state are kept"
	else
		log "installing dokploy-sentinel ${version} for linux/${arch} from ${REPO}"
	fi

	# Fresh install on a terminal with nothing supplied -> ask for the essentials.
	if should_prompt; then
		prompt_settings
	fi

	tmpdir="$(mktemp -d)"
	trap 'rm -rf "$tmpdir"' EXIT INT TERM

	install_binary "$arch" "$version" "$tmpdir"
	install_config
	install_units
	install_interval_dropin
	arm

	if [ -n "$prev" ]; then
		log "upgraded ${prev} -> ${version}. status: systemctl status dokploy-sentinel.timer"
	else
		log "done. status: systemctl status dokploy-sentinel.timer"
	fi
}

do_uninstall() {
	require_root
	# Prefer the packaged uninstaller if it is present alongside the binary's source tree;
	# otherwise perform the removal inline so a piped one-liner still works.
	here=""
	if here="$(cd "$(dirname "$0")" 2>/dev/null && pwd)"; then
		:
	fi
	if [ -n "$here" ] && [ -f "${here}/uninstall.sh" ]; then
		exec sh "${here}/uninstall.sh" "$@"
	fi

	log "uninstalling dokploy-sentinel"
	systemctl disable --now dokploy-sentinel.timer 2>/dev/null || true
	systemctl stop dokploy-sentinel.service 2>/dev/null || true
	rm -f "${UNIT_DIR}/dokploy-sentinel.service" \
		"${UNIT_DIR}/dokploy-sentinel-failure.service" \
		"${UNIT_DIR}/dokploy-sentinel.timer"
	rm -rf "$TIMER_DROPIN_DIR"
	rm -f "$BIN_DEST"
	systemctl daemon-reload 2>/dev/null || true
	log "removed units + binary. Config (${CONFIG_DIR}) and state (${STATE_DIR}) were kept."
	log "run packaging/uninstall.sh --purge to also delete config + state."
}

main() {
	case "${1:-}" in
		--uninstall)
			shift
			do_uninstall "$@"
			;;
		-h | --help)
			grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//'
			;;
		*)
			do_install "$@"
			;;
	esac
}

main "$@"
