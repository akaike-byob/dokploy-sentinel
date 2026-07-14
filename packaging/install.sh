#!/bin/sh
# dokploy-sentinel installer — POSIX sh.
#
# Quick install:
#   curl -fsSL https://raw.githubusercontent.com/akaike-byob/dokploy-sentinel/main/packaging/install.sh | sh
#
# Options (environment variables):
#   VERSION=v1.2.3   pin a release (default: latest)
#   REPO=owner/name  override the source repo (default: akaike-byob/dokploy-sentinel)
#   INTERVAL=30s     override the timer cadence (baked into a drop-in, survives upgrades)
#
#   INTERVAL=30s curl -fsSL .../install.sh | sh
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

install_config() {
	staged_example="${STAGED_PKG}/config.example.toml"
	mkdir -p "$CONFIG_DIR"
	chmod 0700 "$CONFIG_DIR"
	mkdir -p "$STATE_DIR"

	if [ ! -f "$staged_example" ]; then
		warn "config.example.toml not found in archive — skipping config bootstrap"
		return
	fi

	if [ -f "$CONFIG_FILE" ]; then
		install -m 0600 "$staged_example" "${CONFIG_FILE}.new"
		log "existing config kept; reference written to ${CONFIG_FILE}.new"
	else
		install -m 0600 "$staged_example" "$CONFIG_FILE"
		log "wrote default config to ${CONFIG_FILE} (edit before it can deliver alerts)"
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
	# Refuse to enable on an invalid config.
	if [ -f "$CONFIG_FILE" ]; then
		if "$BIN_DEST" check --config "$CONFIG_FILE"; then
			log "config validated"
		else
			warn "config at ${CONFIG_FILE} is invalid — units installed but NOT enabled"
			warn "fix it, then: dokploy-sentinel check --config ${CONFIG_FILE} && systemctl enable --now dokploy-sentinel.timer"
			return 0
		fi
	else
		warn "no config at ${CONFIG_FILE} — units installed but NOT enabled"
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

do_install() {
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
			do_install
			;;
	esac
}

main "$@"
