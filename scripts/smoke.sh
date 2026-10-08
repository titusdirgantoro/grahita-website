#!/usr/bin/env sh
# Pemeriksaan cepat: memastikan semua service merespons.
set -eu

API=${API:-http://localhost:8088}
CUSTOMER=${CUSTOMER:-http://localhost:8080}
ADMIN=${ADMIN:-http://localhost:8081}

ok()   { printf '  \033[32mOK\033[0m   %s\n' "$1"; }
fail() { printf '  \033[31mGAGAL\033[0m %s\n' "$1"; exit 1; }

check() {
  name=$1; url=$2
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$url" || echo 000)
  [ "$code" = "200" ] && ok "$name ($url)" || fail "$name ($url) -> HTTP $code"
}

echo "Memeriksa service..."
check "API healthz"          "$API/healthz"
check "API readyz"           "$API/readyz"
check "API meta"             "$API/api/v1/meta"
check "Customer Portal"      "$CUSTOMER/"
check "Admin Portal"         "$ADMIN/"
check "Proxy /api customer"  "$CUSTOMER/api/v1/meta"
check "Proxy /api admin"     "$ADMIN/api/v1/meta"
check "Mailpit"              "http://localhost:8025/"

echo ""
echo "Readiness:"
curl -s "$API/readyz"; echo
echo ""
echo "Semua pemeriksaan lolos."
