#!/bin/sh
# mulai.sh — merender template Alertmanager dari environment lalu menjalankan
# Alertmanager.
#
# Alertmanager TIDAK mendukung substitusi environment di berkas konfigurasinya
# (tidak seperti Prometheus), jadi templat dirender di sini dengan sed sebelum
# biner dijalankan. Konsekuensinya: tidak ada kata sandi/alamat yang perlu
# ditulis di repo (Architecture.md §6).
#
# Untuk verifikasi konfigurasi tanpa menjalankan server:
#   ALERTMANAGER_RENDER_ONLY=1 ALERT_EMAIL_TO=... sh mulai.sh && amtool check-config <hasil>
set -eu

TEMPLATE=${ALERTMANAGER_TEMPLATE:-/etc/alertmanager/alertmanager.tmpl.yml}
KELUARAN=${ALERTMANAGER_RENDERED:-/alertmanager/alertmanager.yml}

# Variabel yang WAJIB terisi. Gagal keras bila kosong: lebih baik container
# menolak start daripada menjalankan alarm yang tidak bisa mengirim ke mana pun.
wajib="ALERT_SMTP_SMARTHOST ALERT_SMTP_FROM ALERT_SMTP_USER ALERT_SMTP_PASS ALERT_EMAIL_TO"
kosong=""
for v in $wajib; do
    eval "nilai=\${$v:-}"
    if [ -z "$nilai" ]; then
        kosong="$kosong $v"
    fi
done
if [ -n "$kosong" ]; then
    echo "Alertmanager tidak dapat dirender: variabel wajib belum diisi:$kosong" >&2
    echo "Isi lewat environment (mekanisme rahasia operator); jangan tulis di repo." >&2
    exit 1
fi

mkdir -p "$(dirname "$KELUARAN")"
cp "$TEMPLATE" "$KELUARAN"

# Substitusi ${VAR} (tanpa nilai bawaan, seperti os.ExpandEnv).
for v in $wajib; do
    eval "nilai=\${$v}"
    # Kutip miring untuk sed; hindari '/' karena dipakai sebagai pemisah.
    aman=$(printf '%s' "$nilai" | sed 's/[&|]/\\&/g')
    sed -i "s|\${$v}|$aman|g" "$KELUARAN"
done

if grep -q '\${' "$KELUARAN"; then
    echo "Masih ada placeholder yang belum tersubstitusi:" >&2
    grep -n '\${' "$KELUARAN" >&2
    exit 1
fi

if [ "${ALERTMANAGER_RENDER_ONLY:-}" = "1" ]; then
    echo "render selesai: $KELUARAN"
    exit 0
fi

exec /bin/alertmanager \
    --config.file="$KELUARAN" \
    --storage.path=/alertmanager \
    --web.listen-address=:9093
