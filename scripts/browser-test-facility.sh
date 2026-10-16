#!/bin/sh
# Kelola PostgreSQL + object storage sintetis untuk regresi lokal.
# Ini bukan lingkungan aplikasi dan tidak menggantikan E2E production.
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
state_dir=${BROWSER_FACILITY_STATE_DIR:-$HOME/hermes-dev-data/test-facilities/grahita-browser}
env_file=$state_dir/runtime.env
meta_file=$state_dir/runtime.meta
pid_file=$state_dir/s3.pid
log_file=$state_dir/s3.log
s3_bin=$state_dir/s3fixture

fail() {
  printf 'GALAT: %s\n' "$1" >&2
  exit 2
}

port_free() {
  ! ss -ltnH | awk '{print $4}' | grep -Eq "(^|:)$1$"
}

require_safe_port() {
  case "$1" in
    ''|*[!0-9]*) fail "$2 wajib berupa port numerik." ;;
  esac
  [ "$1" -gt 1024 ] || fail "$2 wajib di atas 1024."
  [ "$1" -ne 8080 ] || fail "$2 tidak boleh memakai port 8080."
  port_free "$1" || fail "$2 $1 sedang dipakai."
}

load_meta() {
  [ -r "$meta_file" ] || fail "metadata fixture tidak ditemukan di $meta_file."
  # File dibuat oleh skrip ini, mode 600, dan hanya berisi nilai alfanumerik/jalur lokal.
  . "$meta_file"
}

stop_fixture() {
  [ -e "$meta_file" ] || {
    printf '%s\n' 'Fixture sudah berhenti.'
    return
  }
  load_meta
  if [ -r "$pid_file" ]; then
    s3_pid=$(sed -n '1p' "$pid_file")
    case "$s3_pid" in
      ''|*[!0-9]*) fail 'PID fixture S3 tidak valid.' ;;
    esac
    if kill -0 "$s3_pid" 2>/dev/null; then
      kill "$s3_pid"
      wait "$s3_pid" 2>/dev/null || true
    fi
  fi

  cleanup_sql=$state_dir/cleanup.sql
  umask 077
  cat >"$cleanup_sql" <<EOF
SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$fixture_db' AND pid <> pg_backend_pid();
DROP DATABASE IF EXISTS "$fixture_db";
DROP ROLE IF EXISTS "$fixture_role";
EOF
  psql -h "$pg_socket" -p "$pg_port" -U "$pg_admin_role" -d postgres -X -q -v ON_ERROR_STOP=1 -f "$cleanup_sql" >/dev/null
  rm -f "$cleanup_sql" "$env_file" "$meta_file" "$pid_file" "$log_file" "$s3_bin"
  rmdir "$state_dir" 2>/dev/null || true
  printf '%s\n' 'Fixture regresi lokal dihentikan dan database/storage sintetis dibersihkan.'
}

start_fixture() {
  [ ! -e "$meta_file" ] || fail "fixture sudah memiliki state; jalankan status atau stop terlebih dahulu."
  command -v psql >/dev/null 2>&1 || fail 'psql wajib tersedia.'
  command -v ss >/dev/null 2>&1 || fail 'ss wajib tersedia untuk memeriksa bind loopback.'
  command -v openssl >/dev/null 2>&1 || fail 'openssl wajib tersedia.'
  command -v curl >/dev/null 2>&1 || fail 'curl wajib tersedia untuk smoke storage.'

  pg_socket=${BROWSER_FACILITY_PG_SOCKET:-}
  pg_port=${BROWSER_FACILITY_PG_PORT:-5455}
  pg_admin_role=${BROWSER_FACILITY_PG_ADMIN_ROLE:-grahita}
  [ -n "$pg_socket" ] || fail 'BROWSER_FACILITY_PG_SOCKET wajib menunjuk socket PostgreSQL rootless lokal.'
  case "$pg_socket" in /*) ;; *) fail 'socket PostgreSQL wajib berupa jalur absolut lokal.' ;; esac
  case "$pg_port" in ''|*[!0-9]*) fail 'BROWSER_FACILITY_PG_PORT wajib numerik.' ;; esac

  s3_port=${BROWSER_FACILITY_S3_PORT:-19090}
  api_port=${BROWSER_FIXTURE_API_PORT:-18088}
  customer_port=${BROWSER_FIXTURE_CUSTOMER_PORT:-15173}
  admin_port=${BROWSER_FIXTURE_ADMIN_PORT:-15174}
  require_safe_port "$s3_port" 'Port S3'
  require_safe_port "$api_port" 'Port API'
  require_safe_port "$customer_port" 'Port Vite customer'
  require_safe_port "$admin_port" 'Port Vite admin'

  mkdir -p "$state_dir"
  chmod 700 "$state_dir"
  umask 077

  suffix=$(openssl rand -hex 6)
  fixture_db=grahita_browser_fixture_$suffix
  fixture_role=grahita_fixture_$suffix
  db_password=$(openssl rand -hex 24)
  s3_access=$(openssl rand -hex 16)
  s3_secret=$(openssl rand -hex 32)
  s3_bucket=grahita-browser-fixture-$suffix

  create_sql=$state_dir/create.sql
  cat >"$create_sql" <<EOF
CREATE ROLE "$fixture_role" LOGIN PASSWORD '$db_password';
CREATE DATABASE "$fixture_db" OWNER "$fixture_role";
EOF
  if ! psql -h "$pg_socket" -p "$pg_port" -U "$pg_admin_role" -d postgres -X -q -v ON_ERROR_STOP=1 -f "$create_sql" >/dev/null; then
    rm -f "$create_sql"
    rmdir "$state_dir" 2>/dev/null || true
    fail 'gagal membuat role/database fixture pada PostgreSQL rootless.'
  fi
  rm -f "$create_sql"

  # Guard fixture sengaja menolak query DSN agar parameter host tidak dapat
  # menimpa literal loopback. PostgreSQL rootless lokal menerima fallback TLS
  # standar dari klien tanpa membutuhkan `sslmode` pada URL.
  database_url=postgres://$fixture_role
  database_url=$database_url:$db_password@127.0.0.1:$pg_port/$fixture_db
  cleanup_on_error=true
  trap 'if [ "${cleanup_on_error:-false}" = true ]; then stop_fixture >/dev/null 2>&1 || true; fi' EXIT INT TERM HUP

  cat >"$meta_file" <<EOF
pg_socket='$pg_socket'
pg_port='$pg_port'
pg_admin_role='$pg_admin_role'
fixture_db='$fixture_db'
fixture_role='$fixture_role'
s3_port='$s3_port'
api_port='$api_port'
customer_port='$customer_port'
admin_port='$admin_port'
EOF
  chmod 600 "$meta_file"

  APP_ENV=local DATABASE_URL="$database_url" "$repo_dir/scripts/browser-fixture-local.sh" --prepare-database >/dev/null

  go_bin=$($repo_dir/scripts/go-env.sh --go)
  [ -n "$go_bin" ] || fail 'toolchain Go tidak tersedia.'
  (cd "$repo_dir/api" && "$go_bin" build -o "$s3_bin" ./cmd/s3fixture)
  nohup "$s3_bin" -addr "127.0.0.1:$s3_port" -bucket "$s3_bucket" >"$log_file" 2>&1 &
  s3_pid=$!
  printf '%s\n' "$s3_pid" >"$pid_file"

  ready=false
  i=0
  while [ "$i" -lt 50 ]; do
    if curl -fsSI "http://127.0.0.1:$s3_port/$s3_bucket" >/dev/null 2>&1; then
      ready=true
      break
    fi
    i=$((i + 1))
    sleep 0.1
  done
  [ "$ready" = true ] || fail 'fixture S3 gagal siap pada loopback.'

  cat >"$env_file" <<EOF
export TEST_DATABASE_URL='$database_url'
export DATABASE_URL='$database_url'
export S3_ENDPOINT='http://127.0.0.1:$s3_port'
export S3_REGION='us-east-1'
export S3_BUCKET='$s3_bucket'
export S3_ACCESS_KEY='$s3_access'
export S3_SECRET_KEY='$s3_secret'
export S3_USE_PATH_STYLE='true'
export BROWSER_FIXTURE_API_PORT='$api_port'
export BROWSER_FIXTURE_CUSTOMER_PORT='$customer_port'
export BROWSER_FIXTURE_ADMIN_PORT='$admin_port'
EOF
  chmod 600 "$env_file"

  cleanup_on_error=false
  trap - EXIT INT TERM HUP
  printf 'Fixture regresi lokal siap. Muat environment secara lokal dari:\n  %s\n' "$env_file"
  printf '%s\n' 'Nilai kredensial tidak dicetak. Fixture ini bukan staging/development dan bukan bukti kelulusan E2E production.'
}

status_fixture() {
  load_meta
  [ -r "$env_file" ] || fail 'runtime.env tidak tersedia.'
  . "$env_file"
  s3_pid=$(sed -n '1p' "$pid_file")
  kill -0 "$s3_pid" 2>/dev/null || fail 'proses fixture S3 tidak hidup.'
  psql "$TEST_DATABASE_URL" -X -A -t -v ON_ERROR_STOP=1 -c "SELECT marker FROM public.browser_fixture_guard WHERE id = TRUE" | grep -qx 'grahita-browser-fixture-v1' || fail 'marker database fixture tidak cocok.'
  curl -fsSI "$S3_ENDPOINT/$S3_BUCKET" >/dev/null || fail 'fixture S3 tidak sehat.'
  printf '%s\n' 'Fixture regresi lokal sehat: PostgreSQL bermarker dan storage S3 sintetis hidup pada loopback.'
  printf 'Environment aman: %s\n' "$env_file"
}

reset_fixture() {
  load_meta
  old_socket=$pg_socket
  old_port=$pg_port
  old_admin=$pg_admin_role
  old_s3=$s3_port
  old_api=$api_port
  old_customer=$customer_port
  old_admin_port=$admin_port
  stop_fixture
  BROWSER_FACILITY_PG_SOCKET=$old_socket BROWSER_FACILITY_PG_PORT=$old_port \
    BROWSER_FACILITY_PG_ADMIN_ROLE=$old_admin BROWSER_FACILITY_S3_PORT=$old_s3 \
    BROWSER_FIXTURE_API_PORT=$old_api BROWSER_FIXTURE_CUSTOMER_PORT=$old_customer \
    BROWSER_FIXTURE_ADMIN_PORT=$old_admin_port start_fixture
}

case "${1:-}" in
  start) start_fixture ;;
  status) status_fixture ;;
  stop) stop_fixture ;;
  reset) reset_fixture ;;
  env) [ -r "$env_file" ] || fail 'fixture belum aktif.'; printf '%s\n' "$env_file" ;;
  *) fail 'pemakaian: browser-test-facility.sh start|status|reset|stop|env' ;;
esac
