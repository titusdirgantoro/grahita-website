#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
server=${GRAHITA_SERVER:-$repo_dir/bin/server}
marker='grahita-browser-fixture-v1'
api_port=${BROWSER_FIXTURE_API_PORT:-8088}
customer_port=${BROWSER_FIXTURE_CUSTOMER_PORT:-5173}
admin_port=${BROWSER_FIXTURE_ADMIN_PORT:-5174}

fail() {
  printf 'GALAT: %s\n' "$1" >&2
  exit 2
}

[ "${APP_ENV:-local}" = "local" ] || fail 'fixture browser hanya boleh dijalankan dengan APP_ENV=local.'
[ -n "${DATABASE_URL:-}" ] || fail 'DATABASE_URL sintetis lokal wajib diisi.'
command -v psql >/dev/null 2>&1 || fail 'psql wajib tersedia untuk memverifikasi identitas database fixture.'
for port in "$api_port" "$customer_port" "$admin_port"; do
  case "$port" in
    ''|*[!0-9]*) fail 'port fixture wajib numerik.' ;;
  esac
  [ "$port" -gt 1024 ] || fail 'port fixture wajib di atas 1024.'
  [ "$port" -ne 8080 ] || fail 'fixture tidak boleh memakai port 8080.'
done

identity=$(psql "$DATABASE_URL" -X -A -t -v ON_ERROR_STOP=1 -c "SELECT CASE WHEN COALESCE(inet_server_addr() <<= inet '127.0.0.0/8' OR inet_server_addr() = inet '::1', FALSE) THEN 1 ELSE 0 END || '|' || CASE WHEN current_database() LIKE 'grahita_browser_fixture_%' THEN 1 ELSE 0 END")
[ "$identity" = '1|1' ] || fail 'database fixture wajib memakai server loopback dan nama grahita_browser_fixture_*.'

if [ "${1:-}" = '--prepare-database' ]; then
  relation_count=$(psql "$DATABASE_URL" -X -A -t -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND c.relkind IN ('r','p','v','m','f')")
  [ "$relation_count" = '0' ] || fail 'prepare hanya boleh menandai database yang benar-benar kosong.'
  psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 -c "CREATE TABLE public.browser_fixture_guard (id boolean PRIMARY KEY DEFAULT TRUE CHECK (id), marker text NOT NULL); INSERT INTO public.browser_fixture_guard (id, marker) VALUES (TRUE, '$marker');" >/dev/null
  printf '%s\n' 'Database kosong telah ditandai sebagai fixture browser lokal. Lanjutkan dengan menjalankan skrip tanpa argumen.'
  exit 0
fi

[ "${1:-}" = '' ] || fail 'argumen tidak dikenal.'
[ -x "$server" ] || fail "binary server tidak ditemukan/executable: $server (jalankan make build atau set GRAHITA_SERVER)."
command -v npm >/dev/null 2>&1 || fail 'npm wajib tersedia untuk menjalankan dua proxy Vite loopback.'

stored_marker=$(psql "$DATABASE_URL" -X -A -t -v ON_ERROR_STOP=1 -c "SELECT marker FROM public.browser_fixture_guard WHERE id = TRUE")
[ "$stored_marker" = "$marker" ] || fail 'marker database fixture tidak ada atau tidak cocok; jalankan --prepare-database pada database kosong.'

fixture_password=$(openssl rand -hex 24)
proxy_key=$(openssl rand -hex 32)
runtime_dir=$(mktemp -d "${TMPDIR:-/tmp}/grahita-browser-fixture.XXXXXX")
customer_pid=''
admin_pid=''
api_pid=''

cleanup() {
  trap - EXIT INT TERM HUP
  for pid in "$api_pid" "$admin_pid" "$customer_pid"; do
    if [ -n "$pid" ]; then
      kill "$pid" 2>/dev/null || true
    fi
  done
  wait 2>/dev/null || true
  rm -f "$runtime_dir/customer.log" "$runtime_dir/admin.log"
  rmdir "$runtime_dir" 2>/dev/null || true
}
trap cleanup EXIT INT TERM HUP

psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 -f "$repo_dir/scripts/pg-dev-shim.sql" >/dev/null
APP_ENV=local "$server" --mode=migrate
APP_ENV=local SEED_PASSWORD="$fixture_password" "$server" --mode=seed

VITE_HOST=127.0.0.1 VITE_PORT="$customer_port" VITE_API_TARGET="http://127.0.0.1:$api_port" \
  VITE_BROWSER_FIXTURE_PROXY_KEY="$proxy_key" npm --prefix "$repo_dir/web" run dev:customer >"$runtime_dir/customer.log" 2>&1 &
customer_pid=$!
VITE_HOST=127.0.0.1 VITE_PORT="$admin_port" VITE_API_TARGET="http://127.0.0.1:$api_port" \
  VITE_BROWSER_FIXTURE_PROXY_KEY="$proxy_key" npm --prefix "$repo_dir/web" run dev:admin >"$runtime_dir/admin.log" 2>&1 &
admin_pid=$!

printf '%s\n' 'Fixture browser lokal aktif pada loopback. Buka URL berikut lewat browser:'
printf '  Admin Superadmin: http://127.0.0.1:%s/api/v1/test/browser-session/superadmin\n' "$admin_port"
printf '  Admin Manager:    http://127.0.0.1:%s/api/v1/test/browser-session/manager\n' "$admin_port"
printf '  Cabang sintetis:  http://127.0.0.1:%s/api/v1/test/browser-session/branch\n' "$customer_port"
printf '%s\n' 'Password dan kunci proxy dibuat sementara dan tidak dicetak.'

export APP_ENV=local
export HTTP_ADDR=127.0.0.1:$api_port
export SESSION_SECURE=false
export BROWSER_FIXTURE_ENABLED=true
export BROWSER_FIXTURE_PASSWORD="$fixture_password"
export BROWSER_FIXTURE_PROXY_KEY="$proxy_key"
export BROWSER_FIXTURE_ORIGINS=http://127.0.0.1:$customer_port,http://127.0.0.1:$admin_port
"$server" --mode=api &
api_pid=$!
wait "$api_pid"
