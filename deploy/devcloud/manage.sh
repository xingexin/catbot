#!/usr/bin/env bash
# Manage this DevCloud installation only; all application data stays on /data.
set -euo pipefail
umask 077

CATBOT_ROOT=/data/catbot
CATBOT_HOME=/data/catbot-home
CATBOT_OPS=/data/catbot-ops
CATBOT_USER=catbot
CATBOT_RUNTIME_DIR="$CATBOT_HOME/.docker/run"
CATBOT_CONFIG="$CATBOT_OPS/supervisord.conf"
CATBOT_SUPERVISOR="$CATBOT_OPS/venv/bin/supervisord"
CATBOT_CTL="$CATBOT_OPS/venv/bin/supervisorctl"
CATBOT_PATH="$CATBOT_HOME/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

usage() {
  printf '%s\n' \
    'Usage: sudo /data/catbot/deploy/devcloud/manage.sh {start|status|stop|logs|up}' \
    '  start   Start Supervisor, Rootless Docker and existing catbot images.' \
    '  status  Show the dedicated supervisor, Docker and Compose status.' \
    '  stop    Stop catbot containers, Docker and Supervisor; preserve all data.' \
    '  logs    Show recent service and Docker logs; do not follow.' \
    '  up      Start Docker, then run make to build and update catbot.'
}

die() { printf 'catbot: %s\n' "$*" >&2; exit 1; }

[[ $# -eq 1 ]] || { usage >&2; exit 2; }
case "$1" in
  start|status|stop|logs|up) CATBOT_ACTION=$1 ;;
  *) usage >&2; exit 2 ;;
esac
[[ $(id -u) -eq 0 ]] || die 'Run this management command as root.'
[[ -d "$CATBOT_ROOT" ]] || die "Repository is missing: $CATBOT_ROOT"
[[ -x "$CATBOT_CTL" ]] || die "Supervisor is missing: $CATBOT_CTL"
command -v runuser >/dev/null || die 'runuser is required.'
command -v timeout >/dev/null || die 'timeout is required.'

as_catbot() {
  runuser -u "$CATBOT_USER" -- env -i \
    HOME="$CATBOT_HOME" USER="$CATBOT_USER" LOGNAME="$CATBOT_USER" \
    PATH="$CATBOT_PATH" XDG_RUNTIME_DIR="$CATBOT_RUNTIME_DIR" \
    DOCKER_HOST="unix://$CATBOT_RUNTIME_DIR/docker.sock" \
    /bin/sh -c 'cd "$1"; shift; exec "$@"' sh "$CATBOT_ROOT" "$@"
}

supervisor_running() {
  [[ -f "$CATBOT_CONFIG" ]] &&
    "$CATBOT_CTL" -c "$CATBOT_CONFIG" pid >/dev/null 2>&1
}

docker_ready() {
  as_catbot timeout 5 docker info --format '{{json .SecurityOptions}}' 2>/dev/null |
    grep -q 'name=rootless'
}

prepare_supervisor() {
  [[ -x "$CATBOT_SUPERVISOR" ]] || die "Missing $CATBOT_SUPERVISOR"
  [[ -x "$CATBOT_HOME/bin/dockerd-rootless.sh" ]] ||
    die 'Rootless Docker has not been installed for catbot.'
  [[ -f "$CATBOT_ROOT/deploy/.env" ]] ||
    die 'Create the private deploy/.env before starting this installation.'
  id "$CATBOT_USER" >/dev/null 2>&1 || die 'The catbot service user is missing.'
  install -d -m 0700 "$CATBOT_OPS" "$CATBOT_OPS/logs"
  if [[ ! -f "$CATBOT_CONFIG" ]]; then
    install -m 0600 "$CATBOT_ROOT/deploy/devcloud/supervisor.conf.example" "$CATBOT_CONFIG"
  fi
  as_catbot mkdir -p "$CATBOT_RUNTIME_DIR"
  as_catbot chmod 0700 "$CATBOT_RUNTIME_DIR"
}

start_docker() {
  prepare_supervisor
  if ! supervisor_running; then
    # Do not delete another process's socket or PID file to force startup.
    "$CATBOT_SUPERVISOR" -c "$CATBOT_CONFIG"
  fi
  local supervisor_deadline=$((SECONDS + 15))
  until supervisor_running; do
    (( SECONDS < supervisor_deadline )) || die 'Supervisor did not become ready; inspect ops/logs.'
    sleep 1
  done
  if ! "$CATBOT_CTL" -c "$CATBOT_CONFIG" status rootless-docker |
    grep -q 'RUNNING\|STARTING'; then
    "$CATBOT_CTL" -c "$CATBOT_CONFIG" start rootless-docker
  fi
  local docker_deadline=$((SECONDS + 120))
  until docker_ready; do
    (( SECONDS < docker_deadline )) ||
      die 'Rootless Docker is not ready; inspect /data/catbot-ops/logs/rootless-docker.log.'
    sleep 2
  done
  as_catbot docker compose version
}

lock_changes() {
  command -v flock >/dev/null || die 'flock is required.'
  install -d -m 0700 "$CATBOT_OPS"
  exec 9>"$CATBOT_OPS/manage.lock"
  flock -n 9 || die 'Another catbot start, stop or update is running.'
}

case "$CATBOT_ACTION" in
  start)
    lock_changes
    start_docker
    as_catbot ./deploy/scripts/compose up --no-build --wait --wait-timeout 300
    as_catbot ./deploy/scripts/compose ps
    ;;
  up)
    lock_changes
    start_docker
    as_catbot make
    ;;
  status)
    if supervisor_running; then
      "$CATBOT_CTL" -c "$CATBOT_CONFIG" status || true
    else
      printf '%s\n' 'Supervisor: stopped'
    fi
    if docker_ready; then
      as_catbot docker info --format 'Docker {{.ServerVersion}}; driver={{.Driver}}; data={{.DockerRootDir}}'
      as_catbot ./deploy/scripts/compose ps
    else
      printf '%s\n' 'Rootless Docker: unavailable'
    fi
    ;;
  stop)
    lock_changes
    if docker_ready; then
      # The wrapper includes the optional NapCat profile, even if now disabled.
      # Stop failure intentionally aborts before shutting down Docker.
      as_catbot ./deploy/scripts/compose stop --timeout 60
    else
      printf '%s\n' 'Rootless Docker is unavailable; Compose stop could not be verified.'
    fi
    if supervisor_running; then
      if "$CATBOT_CTL" -c "$CATBOT_CONFIG" status rootless-docker |
        grep -q 'RUNNING\|STARTING\|BACKOFF'; then
        "$CATBOT_CTL" -c "$CATBOT_CONFIG" stop rootless-docker
      fi
      "$CATBOT_CTL" -c "$CATBOT_CONFIG" shutdown
    fi
    printf '%s\n' 'Stop requested. Containers, volumes, configuration and login data were not deleted.'
    ;;
  logs)
    if docker_ready; then
      as_catbot ./deploy/scripts/compose logs --tail=100 --no-color
    else
      printf '%s\n' 'Rootless Docker unavailable; showing existing daemon logs only.'
    fi
    for logfile in "$CATBOT_OPS/logs/rootless-docker.log" "$CATBOT_OPS/logs/supervisord.log"; do
      if [[ -f "$logfile" ]]; then
        printf '\n%s\n' "$logfile"
        tail -n 80 "$logfile"
      fi
    done
    ;;
esac
