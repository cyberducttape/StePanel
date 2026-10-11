FROM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/cyberducttape/StePanel.Commit=docker -X github.com/cyberducttape/StePanel.BuildDate=container" -o /out/stepanel ./cmd/stepanel

FROM debian:bookworm-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171

# REPRODUCIBILITY: The base image digest is fixed, but apt-get still resolves
# unpinned runtime packages from the live Debian Bookworm repositories. This
# provides a reviewable package inventory, not byte-for-byte reproducibility.
# Use a dated Debian snapshot or a complete external package lock when
# reproducible image contents are required.
#
# docker/apt-pins.txt records the runtime package inventory; it is not a
# complete package lock.
# Never use apt-get upgrade, which pulls untested/unreviewed versions.
# Track version updates in git with CVE or improvement justification.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        libpcre2-8-0 \
        mariadb-client \
        rclone \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --user-group --home-dir /opt/stepanel --shell /usr/sbin/nologin stepanel \
    && mkdir -p /opt/stepanel/web/static /var/lib/ste-panel/imports /var/www/sites \
    && chown -R stepanel:stepanel /opt/stepanel /var/lib/ste-panel /var/www/sites
COPY --from=build /out/stepanel /opt/stepanel/stepanel
COPY web/index.html /opt/stepanel/web/index.html
COPY web/static/ /opt/stepanel/web/static/
USER 10001:10001
WORKDIR /opt/stepanel
ENV HOME=/opt/stepanel \
    STEPANEL_ENV=production \
    STEPANEL_LISTEN=:8080 \
    STEPANEL_IMPORT_ROOT=/var/lib/ste-panel/imports \
    STEPANEL_BACKUP_ROOT=/var/lib/ste-panel/backups \
    STEPANEL_WEB_ROOT=/var/www \
    STEPANEL_MAIL_ROOT=/var/lib/ste-panel/mail \
    STEPANEL_NVM_DIR=/var/lib/ste-panel/nvm \
    STEPANEL_PROXY_ROOT=/var/lib/ste-panel/proxy \
    STEPANEL_VHOST_ROOT=/var/lib/ste-panel/vhosts \
    STEPANEL_APP_ROOT=/var/lib/ste-panel/apps \
    STEPANEL_MALWARE_ROOT=/var/lib/ste-panel/quarantine \
    STEPANEL_AUDIT_LOG=/var/lib/ste-panel/audit.jsonl \
    STEPANEL_JOB_STATE=/var/lib/ste-panel/jobs.json \
    STEPANEL_SESSION_STATE=/var/lib/ste-panel/sessions.json \
    STEPANEL_CONTROL_PLANE_DB=/var/lib/ste-panel/control-plane.db \
    STEPANEL_RECOVERY_ROOT=/var/www/sites/.stepanel-recovery
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD ["curl", "--fail", "--silent", "http://127.0.0.1:8080/readyz"]
ENTRYPOINT ["/opt/stepanel/stepanel"]
