#!/usr/bin/env sh
# smoke-staging.sh — uji asap alur kritis pada STAGING/PRODUKSI (dua host).
#
# Alur: login cabang → katalog → draft → submit → login admin → verifikasi
# (cocok → Done) → daftar dokumen → unduh dokumen.
#
# Dua host diuji sekaligus supaya isolasi cookie host-only ikut terbukti:
# sesi yang diterbitkan host cabang tidak boleh berlaku di host admin.
#
# Pemakaian:
#   CUSTOMER_BASE=https://staging.grahita-branch.tiusclaw.com \
#   ADMIN_BASE=https://staging.grahita-admin.tiusclaw.com \
#   SMOKE_EMAIL=... SMOKE_PASSWORD=... \
#   SMOKE_ADMIN_EMAIL=... SMOKE_ADMIN_PASSWORD=... \
#   ./scripts/smoke-staging.sh
#
# Untuk staging ber-basic-auth, isi SMOKE_BASIC_AUTH="user:kata-sandi".
# Kredensial HANYA dari environment (Architecture.md §6) — jangan pernah
# ditulis di repo atau di baris perintah yang terekam.
#
# Kode keluar:
#   0 = seluruh alur (termasuk unduh dokumen) berhasil
#   1 = ada langkah WAJIB yang gagal
#   2 = alur berhenti pada ketergantungan eksternal yang belum tersedia
#       (mis. Gotenberg/S3) — bukan sukses, bukan pula cacat rute

set -u

CUSTOMER_BASE=${CUSTOMER_BASE:?CUSTOMER_BASE wajib diisi}
ADMIN_BASE=${ADMIN_BASE:?ADMIN_BASE wajib diisi}
SMOKE_EMAIL=${SMOKE_EMAIL:?SMOKE_EMAIL wajib diisi}
SMOKE_PASSWORD=${SMOKE_PASSWORD:?SMOKE_PASSWORD wajib diisi}
SMOKE_ADMIN_EMAIL=${SMOKE_ADMIN_EMAIL:?SMOKE_ADMIN_EMAIL wajib diisi}
SMOKE_ADMIN_PASSWORD=${SMOKE_ADMIN_PASSWORD:?SMOKE_ADMIN_PASSWORD wajib diisi}
SMOKE_BASIC_AUTH=${SMOKE_BASIC_AUTH:-}

KERJA=$(mktemp -d)
trap 'rm -rf "$KERJA"' EXIT
JAR_CABANG="$KERJA/cabang.cookies"
JAR_ADMIN="$KERJA/admin.cookies"
TMP1="$KERJA/1.json"
TMP2="$KERJA/2.json"

# ---------- pembantu ----------
ok()   { printf '  \033[32mLULUS\033[0m %s\n' "$1"; }
info() { printf '  \033[36mINFO \033[0m %s\n' "$1"; }
gagal() { printf '  \033[31mGAGAL\033[0m %s\n' "$1"; ADA_GAGAL=1; }
blokir() { printf '  \033[33mTERHALANG\033[0m %s\n' "$1"; ADA_TERHALANG=1; }
ADA_GAGAL=0
ADA_TERHALANG=0

# curl dasar: sertakan basic-auth bila diisi.
#
# SMOKE_CURL_OPTS hanya dipakai untuk memverifikasi REPLIKA staging lokal yang
# dibuat dengan Caddy lokal (mis. "--resolve host:8443:127.0.0.1 --cacert ca.crt");
# pada staging/produksi nyata variabel ini dibiarkan kosong.
# shellcheck disable=SC2086
curlx() {
    if [ -n "$SMOKE_BASIC_AUTH" ]; then
        curl -sS --max-time 30 -u "$SMOKE_BASIC_AUTH" ${SMOKE_CURL_OPTS:-} "$@"
    else
        curl -sS --max-time 30 ${SMOKE_CURL_OPTS:-} "$@"
    fi
}

# Ambil nilai cookie dari cookie jar Netscape (nama di kolom 6, nilai kolom 7).
cookie() { awk -v n="$2" '$6==n {v=$7} END {print v}' "$1"; }

kode() { printf '%s' "$1" | tail -1; }
# sukses2xx mengembalikan 0 bila kode status diawali "2".
sukses2xx() { case "$1" in 2??) return 0 ;; *) return 1 ;; esac; }

json() { # json <berkas> <ekspresi-python-yang-mengembalikan-string>
    python3 "$KERJA/ambil.py" "$1" "$2"
}

cat > "$KERJA/ambil.py" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
expr = sys.argv[2]
val = eval(expr, {"d": d})
if isinstance(val, bool):
    print("true" if val else "false")
elif val is None:
    print("")
else:
    print(val)
PY

# Permintaan ber-JSON dengan token CSRF dari cookie jar.
req_json() { # req_json <metode> <url> <jar> <berkas> <body>
    metode=$1; url=$2; jar=$3; keluar=$4; body=$5
    token=$(cookie "$jar" grahita_csrf)
    curlx -o "$keluar" -w '\n%{http_code}' -X "$metode" "$url" \
        -b "$jar" -c "$jar" \
        -H "Content-Type: application/json" \
        -H "X-CSRF-Token: $token" \
        -H "Accept: application/json" \
        --data "$body"
}

echo "Uji asap staging Grahita"
echo "  cabang: $CUSTOMER_BASE"
echo "  admin : $ADMIN_BASE"
echo

# ---------- 1. UI + header keamanan ----------
echo "1. Kedua host menyajikan UI dan API same-origin"
for pasangan in "cabang:$CUSTOMER_BASE" "admin:$ADMIN_BASE"; do
    nama=${pasangan%%:*}; basis=${pasangan#*:}
    halaman=$(curlx -o "$KERJA/ui.html" -w '\n%{http_code}' -D "$KERJA/ui.h" "$basis/")
    if [ "$(kode "$halaman")" = "200" ] && grep -qi '<div id="root">' "$KERJA/ui.html"; then
        ok "$nama: halaman SPA tersaji (200)"
    else
        gagal "$nama: halaman SPA -> $(kode "$halaman")"
    fi
    for h in content-security-policy strict-transport-security x-content-type-options x-frame-options; do
        if grep -qi "^$h:" "$KERJA/ui.h"; then
            ok "$nama: header $h ada"
        else
            gagal "$nama: header $h tidak ada pada respons UI"
        fi
    done
    meta=$(curlx -o "$TMP1" -w '\n%{http_code}' "$basis/api/v1/meta")
    if [ "$(kode "$meta")" = "200" ] && [ "$(json "$TMP1" "d['data']['env']")" != "" ]; then
        ok "$nama: API same-origin /api/v1/meta 200 (env $(json "$TMP1" "d['data']['env']"))"
    else
        gagal "$nama: /api/v1/meta -> $(kode "$meta")"
    fi
done
echo

# ---------- 2. login cabang ----------
echo "2. Login cabang + atribut cookie sesi"
curlx -o /dev/null -c "$JAR_CABANG" "$CUSTOMER_BASE/api/v1/meta" >/dev/null 2>&1 || true
token=$(cookie "$JAR_CABANG" grahita_csrf)
set_cookie=$(curlx -o "$TMP1" -D "$KERJA/login.h" -w '\n%{http_code}' -X POST \
    "$CUSTOMER_BASE/api/v1/auth/login" -c "$JAR_CABANG" \
    -H "Content-Type: application/json" -H "X-CSRF-Token: $token" \
    --data "{\"email\":\"$SMOKE_EMAIL\",\"password\":\"$SMOKE_PASSWORD\"}")
if [ "$(kode "$set_cookie")" = "200" ]; then
    ok "login cabang 200"
else
    gagal "login cabang -> $(kode "$set_cookie"): $(head -c 300 "$TMP1")"
fi
sesi=$(grep -i '^set-cookie: .*grahita_session=' "$KERJA/login.h" | head -1)
if [ -n "$sesi" ]; then
    for atribut in HttpOnly SameSite=Lax Secure; do
        if printf '%s' "$sesi" | grep -qi "$atribut"; then
            ok "cookie sesi memuat $atribut"
        else
            gagal "cookie sesi TANPA $atribut: $sesi"
        fi
    done
    if printf '%s' "$sesi" | grep -qi 'domain='; then
        gagal "cookie sesi memuat atribut Domain (harus host-only): $sesi"
    else
        ok "cookie sesi host-only (tanpa Domain)"
    fi
else
    gagal "tidak ada Set-Cookie sesi pada login"
fi
echo

# ---------- 3. katalog ----------
echo "3. Katalog cabang (harga dari server)"
katalog=$(curlx -o "$TMP1" -w '\n%{http_code}' -b "$JAR_CABANG" -c "$JAR_CABANG" "$CUSTOMER_BASE/api/v1/catalog")
PRODUK_ID=""
if [ "$(kode "$katalog")" = "200" ]; then
    PRODUK_ID=$(json "$TMP1" "[p['id'] for p in d['data'] if p['jenis']=='reguler' and p['harga']>0][0]")
    ok "katalog 200; produk uji $PRODUK_ID"
else
    gagal "katalog -> $(kode "$katalog")"
fi
echo

# ---------- 4. draft + submit ----------
echo "4. Buat draft lalu submit (C&C tanpa bukti)"
draft=""
if [ -n "$PRODUK_ID" ]; then
    draft=$(req_json POST "$CUSTOMER_BASE/api/v1/orders" "$JAR_CABANG" "$TMP1" \
        "{\"catatan\":\"uji asap F1-22\",\"lines\":[{\"product_id\":\"$PRODUK_ID\",\"qty\":2}]}")
    if sukses2xx "$(kode "$draft")"; then
        idPesanan=$(json "$TMP1" "d['data']['id']")
        ok "draft dibuat $idPesanan (total $(json "$TMP1" "d['data']['total']"), status $(kode "$draft"))"
    else
        gagal "buat draft -> $(kode "$draft"): $(head -c 300 "$TMP1")"
    fi
fi

idPesanan=${idPesanan:-}
if [ -n "$idPesanan" ]; then
    token=$(cookie "$JAR_CABANG" grahita_csrf)
    submit=$(curlx -o "$TMP1" -w '\n%{http_code}' -X POST \
        "$CUSTOMER_BASE/api/v1/orders/$idPesanan/submit" -b "$JAR_CABANG" -c "$JAR_CABANG" \
        -H "X-CSRF-Token: $token" -F "is_cnc=true")
    if [ "$(kode "$submit")" = "200" ]; then
        ok "submit 200; status $(json "$TMP1" "d['data']['status']"), nomor $(json "$TMP1" "d['data']['nomor']")"
    else
        gagal "submit -> $(kode "$submit"): $(head -c 300 "$TMP1")"
    fi
fi
echo

# ---------- 5. isolasi cookie antar host ----------
echo "5. Isolasi sesi antar host (cookie host-only)"
if [ -n "$idPesanan" ]; then
    lintas=$(curlx -o /dev/null -w '\n%{http_code}' -b "$JAR_CABANG" "$ADMIN_BASE/api/v1/auth/me")
    if [ "$(kode "$lintas")" = "401" ]; then
        ok "sesi cabang TIDAK berlaku di host admin (401)"
    else
        gagal "sesi cabang diterima di host admin -> $(kode "$lintas") (kebocoran sesi)"
    fi
fi
echo

# ---------- 6. login admin + verifikasi ----------
echo "6. Login admin lalu verifikasi (cocok → Done)"
curlx -o /dev/null -c "$JAR_ADMIN" "$ADMIN_BASE/api/v1/meta" >/dev/null 2>&1 || true
token=$(cookie "$JAR_ADMIN" grahita_csrf)
masuk=$(curlx -o "$TMP1" -w '\n%{http_code}' -X POST "$ADMIN_BASE/api/v1/auth/login" \
    -c "$JAR_ADMIN" -H "Content-Type: application/json" -H "X-CSRF-Token: $token" \
    --data "{\"email\":\"$SMOKE_ADMIN_EMAIL\",\"password\":\"$SMOKE_ADMIN_PASSWORD\"}")
if [ "$(kode "$masuk")" = "200" ]; then
    ok "login admin 200 (peran $(json "$TMP1" "d['data']['user']['role']"))"
else
    gagal "login admin -> $(kode "$masuk"): $(head -c 300 "$TMP1")"
fi

if [ -n "$idPesanan" ]; then
    verif=$(req_json POST "$ADMIN_BASE/api/v1/admin/orders/$idPesanan/verify" "$JAR_ADMIN" "$TMP1" \
        '{"hasil":"cocok"}')
    if [ "$(kode "$verif")" = "200" ]; then
        ok "verifikasi 200; status pesanan $(json "$TMP1" "d['data']['order']['status']"), hasil $(json "$TMP1" "d['data']['verifikasi']['hasil']")"
    else
        gagal "verifikasi -> $(kode "$verif"): $(head -c 300 "$TMP1")"
    fi
fi
echo

# ---------- 7. dokumen + unduh ----------
echo "7. Daftar dokumen lalu unduh"
if [ -n "$idPesanan" ]; then
    dok=$(curlx -o "$TMP1" -w '\n%{http_code}' -b "$JAR_CABANG" "$CUSTOMER_BASE/api/v1/orders/$idPesanan/documents")
    if [ "$(kode "$dok")" != "200" ]; then
        gagal "daftar dokumen -> $(kode "$dok")"
    else
        jumlah=$(json "$TMP1" "len(d['data'])")
        if [ "$jumlah" = "0" ]; then
            blokir "daftar dokumen kosong: render Invoice/Surat Jalan butuh Gotenberg + object storage (belum tersedia di lingkungan ini)"
        else
            docID=$(json "$TMP1" "d['data'][0]['id']")
            unduh=$(curlx -o "$KERJA/dokumen.bin" -w '\n%{http_code}' \
                -b "$JAR_CABANG" "$CUSTOMER_BASE/api/v1/documents/$docID/download")
            ukuran=$(wc -c < "$KERJA/dokumen.bin" | tr -d ' ')
            if [ "$(kode "$unduh")" = "200" ] && [ "$ukuran" -gt 1000 ]; then
                ok "unduh dokumen 200 ($ukuran byte, $(json "$TMP1" "d['data'][0]['jenis']"))"
            else
                gagal "unduh dokumen -> $(kode "$unduh") ($ukuran byte)"
            fi
        fi
    fi
fi
echo

if [ "$ADA_GAGAL" -ne 0 ]; then
    echo "HASIL: GAGAL — ada langkah wajib yang tidak lulus."
    exit 1
fi
if [ "$ADA_TERHALANG" -ne 0 ]; then
    echo "HASIL: TERHALANG — alur login→submit→verify lulus, unduh dokumen belum bisa"
    echo "        dibuktikan karena ketergantungan eksternal (Gotenberg/S3) belum ada."
    exit 2
fi
echo "HASIL: LULUS — login → submit → verify → unduh dokumen berhasil di kedua host."
