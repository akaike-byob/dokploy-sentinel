#!/bin/sh
# dokploy-sentinel — multi-node deploy over SSH (the recommended Swarm path).
#
# Runs the host-level installer (install.sh) on every node of your fleet, so each
# node gets its own systemd timer. This keeps the design guarantee that the
# watchdog is NOT a container and cannot be OOM-killed by the very condition it
# detects. In Swarm every node sees only its own containers, so it must run on
# every node — manager and workers alike.
#
# The config is host-agnostic (thresholds are %/ratios), so the SAME config works
# on every node; leave host_label empty and each node reports its own hostname.
#
# Usage (run from a manager, or any box with SSH to all nodes):
#   ./deploy-swarm.sh [--config FILE] [--nodes-file FILE] [--uninstall] [NODE ...]
#
# Node list resolution (first non-empty wins):
#   1. NODE arguments on the command line   (e.g. ./deploy-swarm.sh web1 web2 db1)
#   2. --nodes-file FILE                     (one SSH target per line, # comments ok)
#   3. `docker node ls` hostnames           (must be SSH-resolvable)
#
# A NODE is an SSH target: "host", "user@host", or an alias from ~/.ssh/config.
#
# Options / environment:
#   --config FILE     push this config.toml to every node (0600) before installing
#   --nodes-file FILE read SSH targets from FILE
#   --uninstall       run the installer's --uninstall on every node instead
#   SSH_USER=deploy   default user for bare "host" targets (else your SSH default)
#   SSH_OPTS="..."    extra ssh/scp options (e.g. "-i key -o StrictHostKeyChecking=no")
#   SUDO=sudo         privilege-escalation prefix run on the node (default: none = ssh as root)
#   VERSION=v1.2.3    pin the release installed on each node (default: latest)
#   INTERVAL=30s      timer cadence on each node
#   REPO=owner/name   source repo (default: akaike-byob/dokploy-sentinel)
#
# Examples:
#   SUDO=sudo ./deploy-swarm.sh --config ./config.toml web1 web2 db1
#   ./deploy-swarm.sh --nodes-file nodes.txt --config ./config.toml
#   ./deploy-swarm.sh --uninstall web1 web2 db1

set -e

REPO="${REPO:-akaike-byob/dokploy-sentinel}"
INSTALL_URL="https://raw.githubusercontent.com/${REPO}/main/packaging/install.sh"
SSH_USER="${SSH_USER:-}"
SSH_OPTS="${SSH_OPTS:-}"
SUDO="${SUDO:-}"

CONFIG=""
NODES_FILE=""
ACTION="install"
NODES=""

log()  { printf '==> %s\n' "$*"; }
warn() { printf 'WARN: %s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# ---- parse args -------------------------------------------------------------
while [ $# -gt 0 ]; do
	case "$1" in
		--config)       CONFIG="$2"; shift 2 ;;
		--config=*)     CONFIG="${1#*=}"; shift ;;
		--nodes-file)   NODES_FILE="$2"; shift 2 ;;
		--nodes-file=*) NODES_FILE="${1#*=}"; shift ;;
		--uninstall)    ACTION="uninstall"; shift ;;
		-h|--help)      grep -E '^#( |$)' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
		--)             shift; while [ $# -gt 0 ]; do NODES="${NODES} $1"; shift; done ;;
		-*)             die "unknown option: $1" ;;
		*)              NODES="${NODES} $1"; shift ;;
	esac
done

# ---- resolve node list ------------------------------------------------------
if [ -z "$NODES" ] && [ -n "$NODES_FILE" ]; then
	[ -f "$NODES_FILE" ] || die "nodes file not found: $NODES_FILE"
	NODES="$(sed 's/#.*//' "$NODES_FILE" | tr '\n' ' ')"
fi
if [ -z "$NODES" ]; then
	command -v docker >/dev/null 2>&1 || die "no nodes given and docker not available to discover them"
	log "no nodes given — discovering from 'docker node ls' (hostnames must be SSH-reachable)"
	NODES="$(docker node ls --format '{{.Hostname}}' 2>/dev/null | tr '\n' ' ')" ||
		die "docker node ls failed (are you on a Swarm manager?)"
fi
NODES="$(printf '%s' "$NODES" | tr -s ' ' | sed 's/^ //; s/ $//')"
[ -n "$NODES" ] || die "no nodes to deploy to"

if [ -n "$CONFIG" ] && [ ! -f "$CONFIG" ]; then
	die "config file not found: $CONFIG"
fi

# ssh target: prefix SSH_USER only for bare "host" targets (no existing user@).
target_for() {
	case "$1" in
		*@*) printf '%s' "$1" ;;
		*)
			if [ -n "$SSH_USER" ]; then
				printf '%s@%s' "$SSH_USER" "$1"
			else
				printf '%s' "$1"
			fi
			;;
	esac
}

push_config() {
	target="$1"
	# shellcheck disable=SC2086,SC2029 # SSH_OPTS word-split + client-side var expansion are intentional
	scp $SSH_OPTS "$CONFIG" "${target}:/tmp/ds-config.$$.toml" >/dev/null
	# shellcheck disable=SC2086,SC2029 # SSH_OPTS word-split + client-side var expansion are intentional
	ssh $SSH_OPTS "$target" \
		"${SUDO} sh -c 'mkdir -p /etc/dokploy-sentinel && chmod 0700 /etc/dokploy-sentinel && install -m 0600 /tmp/ds-config.$$.toml /etc/dokploy-sentinel/config.toml && rm -f /tmp/ds-config.$$.toml'"
}

do_node() {
	target="$1"
	if [ "$ACTION" = "uninstall" ]; then
		# shellcheck disable=SC2086,SC2029 # SSH_OPTS word-split + client-side var expansion are intentional
		ssh $SSH_OPTS "$target" "curl -fsSL '$INSTALL_URL' | ${SUDO} sh -s -- --uninstall"
		return
	fi
	if [ -n "$CONFIG" ]; then
		push_config "$target"
	fi
	# env vars are set AFTER sudo so they survive into the piped installer.
	# NON_INTERACTIVE=1 guarantees the remote installer never blocks on a prompt.
	# shellcheck disable=SC2086,SC2029 # SSH_OPTS word-split + client-side var expansion are intentional
	ssh $SSH_OPTS "$target" \
		"curl -fsSL '$INSTALL_URL' | ${SUDO} env NON_INTERACTIVE=1 VERSION='${VERSION:-}' INTERVAL='${INTERVAL:-}' REPO='${REPO}' sh"
}

# ---- run --------------------------------------------------------------------
log "action=${ACTION} nodes:${NODES}"
if [ -n "$CONFIG" ]; then
	log "pushing shared config to each node: $CONFIG"
fi

ok_list=""
fail_list=""
for node in $NODES; do
	target="$(target_for "$node")"
	log "----- ${target} -----"
	if do_node "$target"; then
		ok_list="${ok_list} ${node}"
	else
		warn "node ${node} failed"
		fail_list="${fail_list} ${node}"
	fi
done

echo
log "done. ok:${ok_list:- none}"
if [ -n "$fail_list" ]; then
	warn "failed:${fail_list}"
	exit 1
fi
exit 0
