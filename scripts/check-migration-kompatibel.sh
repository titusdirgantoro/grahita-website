#!/usr/bin/env sh
# check-migration-kompatibel.sh — menjaga prinsip "migrasi kompatibel mundur"
# (Architecture.md §9): perubahan destruktif pada skema yang sudah dipakai
# membatalkan rollback, jadi ia harus lewat migrasi maju (expand → migrate →
# contract) dan bukan langsung menimpa migrasi lama.
#
# Aturannya:
#   1. Migrasi 0001_init.sql (skema awal) dikecualikan — ia memang membangun
#      seluruh skema dari nol.
#   2. Bagian "Down" (rollback goose) dikecualikan: di sana DROP memang wajar.
#   3. Sisanya tidak boleh memuat pernyataan destruktif di bagian "Up".
#
# Yang dianggap destruktif:
#   DROP TABLE, DROP COLUMN, RENAME COLUMN, RENAME TO, DROP TYPE,
#   ALTER COLUMN ... TYPE, ALTER COLUMN ... SET NOT NULL tanpa DEFAULT
#   (perketatan yang membuat baris lama gagal).
#
# Jalankan: ./scripts/check-migration-kompatibel.sh
set -eu

cd "$(dirname "$0")/.."

migrasi="api/db/migrations"
[ -d "$migrasi" ] || { echo "direktori $migrasi tidak ditemukan" >&2; exit 1; }

# Pola pernyataan destruktif (case-insensitive) pada bagian Up.
pola_drop='(^|[^a-z_])drop[[:space:]]+(table|column|type|constraint|index)'
pola_rename='(^|[^a-z_])rename[[:space:]]+(to|column)'
pola_type='alter[[:space:]]+column[[:space:]]+[^;]*[[:space:]]type[[:space:]]'
pola_notnull='alter[[:space:]]+column[[:space:]]+[^;]*set[[:space:]]+not[[:space:]]+null'

gagal=0
lapor() {
    echo "PELANGGARAN $1:$2: $3" >&2
    gagal=1
}

for berkas in $(ls "$migrasi"/*.sql | sort); do
    nama=$(basename "$berkas")
    case "$nama" in
        0001_init.sql) continue ;;  # skema awal: pengecualian
        0001_*) continue ;;
    esac

    # Potong bagian Down (rollback) — di sana DROP memang disengaja.
    up=$(sed -n '1,/^--[[:space:]]*+goose[[:space:]]\+Down/p' "$berkas")

    if printf '%s' "$up" | grep -qEi "$pola_drop"; then
        lapor "$nama" "$(printf '%s' "$up" | grep -nEi "$pola_drop" | head -1 | cut -d: -f1)" \
            "pernyataan DROP di bagian Up: $(printf '%s' "$up" | grep -Ei "$pola_drop" | head -1 | sed 's/^[[:space:]]*//')"
    fi

    if printf '%s' "$up" | grep -qEi "$pola_rename"; then
        lapor "$nama" "$(printf '%s' "$up" | grep -nEi "$pola_rename" | head -1 | cut -d: -f1)" \
            "RENAME di bagian Up merusak klien versi lama; pakai kolom/tabel baru + migrasi maju"
    fi

    if printf '%s' "$up" | grep -qEi "$pola_type"; then
        lapor "$nama" "$(printf '%s' "$up" | grep -nEi "$pola_type" | head -1 | cut -d: -f1)" \
            "ALTER COLUMN TYPE di bagian Up memutus kompatibilitas mundur"
    fi

    # SET NOT NULL hanya boleh bila kolom juga diberi DEFAULT di berkas yang sama.
    if printf '%s' "$up" | grep -qEi "$pola_notnull" && ! printf '%s' "$up" | grep -qEi 'set[[:space:]]+default'; then
        lapor "$nama" "?" "SET NOT NULL tanpa DEFAULT: baris lama bisa gagal saat migrasi naik"
    fi
done

if [ "$gagal" -ne 0 ]; then
    echo "" >&2
    echo "Migrasi tidak kompatibel mundur. Ikuti pola expand → migrate → contract:" >&2
    echo "  1) tambah kolom/tabel baru (nullable), 2) isi data & pindahkan pembaca," >&2
    echo "  3) hapus yang lama pada rilis BERIKUTNYA lewat migrasi maju tersendiri." >&2
    exit 1
fi

echo "Migrasi kompatibel mundur: tidak ada pernyataan destruktif di bagian Up."
