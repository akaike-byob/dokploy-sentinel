# Image for the OPTIONAL containerized (Swarm global-service) deployment.
# Built by goreleaser from the prebuilt static binary (see .goreleaser.yaml).
#
# distroless/static ships ca-certificates (needed for HTTPS delivery to Slack) and
# nothing else — no shell, no package manager. It runs as root because the
# watchdog must read the host's /proc, cgroup v2 tree, and docker.sock, which are
# bind-mounted read-only into the container (see packaging/docker-stack.yml).
#
# NOTE: running as a container softens the project's "cannot be OOM-killed by the
# condition it detects" guarantee. The systemd install (install.sh /
# deploy-swarm.sh) remains the recommended path; this image is the zero-SSH
# alternative for teams that accept the trade-off.
FROM gcr.io/distroless/static:latest

COPY dokploy-sentinel /usr/local/bin/dokploy-sentinel

ENTRYPOINT ["/usr/local/bin/dokploy-sentinel"]
CMD ["run", "--config", "/etc/dokploy-sentinel/config.toml"]
