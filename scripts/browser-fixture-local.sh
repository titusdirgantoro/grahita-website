#!/bin/sh
set -eu

server=${GRAHITA_SERVER:-./bin/server}

if [ "${APP_ENV:-local}" != "local" ]; then
  printf '%s\n' 'GALAT: fixture browser hanya boleh dijalankan dengan APP_ENV=local.' >&2
  exit 2
fi
if [ -z "${DATABASE_URL:-}" ]; then
  printf '%s\n' 'GALAT: DATABASE_URL sintetis lokal wajib diisi.' >&2
  exit 2
fi
if [ ! -x "$server" ]; then
  printf 'GALAT: binary server tidak ditemukan/executable: %s\n' "$server" >&2
  printf '%s\n' 'Jalankan make build atau set GRAHITA_SERVER.' >&2
  exit 2
fi

fixture_password=$(openssl rand -hex 24)

APP_ENV=local SEED_PASSWORD="$fixture_password" "$server" --mode=seed

printf '%s\n' \
  'Fixture browser lokal aktif. Buka URL berikut lewat browser:' \
  '  Admin Superadmin: http://localhost:5174/api/v1/test/browser-session/superadmin' \
  '  Admin Manager:    http://localhost:5174/api/v1/test/browser-session/manager' \
  '  Cabang sintetis:  http://localhost:5173/api/v1/test/browser-session/branch' \
  'Password fixture dibuat sementara dan tidak dicetak.'

export APP_ENV=local
export SESSION_SECURE=false
export BROWSER_FIXTURE_ENABLED=true
export BROWSER_FIXTURE_PASSWORD="$fixture_password"
exec "$server" --mode=api
