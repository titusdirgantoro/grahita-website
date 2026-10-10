#!/usr/bin/env sh
# check-secrets.sh — memastikan tidak ada rahasia yang ikut masuk repo
# (Architecture.md §6).
#
# Sengaja memakai daftar pola berkeyakinan tinggi agar tidak menghasilkan
# alarm palsu, dan hanya memeriksa berkas yang dilacak git.
#
# Gerbang ini FAIL CLOSED: bila grep sendiri gagal (mis. berkas tak terbaca),
# skrip MENOLAK — tidak pernah melaporkan "bersih" karena alatnya tidak jalan.
#
# Perbaikan QA F1-22 putaran 1 (PEMBLOKIR): pola yang dimulai dengan
# `-----BEGIN` sebelumnya diteruskan ke grep tanpa `-e`, sehingga GNU grep
# memparsenya sebagai OPTION (`grep: unrecognized option '-----BEGIN ...'`,
# exit 2); stderr-nya dibuang (`2>/dev/null`) dan statusnya ditelan (`|| true`).
# Akibatnya pemeriksaan 1 SELALU melaporkan "tidak ada rahasia" walau kunci
# privat/token jelas terlacak. Sekarang: `grep -e "$pola"`, status grep tidak
# pernah ditelan, dan ada kendali negatif tetap (`--uji-diri`) yang dijalankan
# juga oleh CI.
#
# Catatan implementasi: skrip ini memakai `set -eu`; status perintah yang
# memang perlu diperiksa SELALU ditulis sebagai daftar `&& ... || _st=$?`
# (bentuk yang dikecualikan dari errexit) — bukan dengan mematikan/menyalakan
# ulang `set -e`, karena `set -e` yang dinyalakan di dalam fungsi membuat
# `return 1` yang sah justru mematikan skrip.
#
# Pemakaian:
#   scripts/check-secrets.sh              # pindai berkas yang dilacak git
#   scripts/check-secrets.sh --uji-diri   # kendali diri (negatif + positif)
#
# Keluar: 0 bersih, 1 rahasia terdeteksi, 2 alat pemindai gagal (fail closed).
set -eu

cd "$(dirname "$0")/.."

POLA_KUNCI='-----BEGIN (RSA|EC|OPENSSH|PGP) PRIVATE KEY-----|AKIA[0-9A-Z]{16}|aws_secret_access_key[[:space:]]*=[[:space:]]*[A-Za-z0-9/+=]{20,}|sk-ant-[A-Za-z0-9_-]{20,}|ghp_[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|SERVICE_ACCOUNT_JSON[[:space:]]*=[[:space:]]*\{'
POLA_DSN='postgres(ql)?://[^:@/[:space:]]+:[^@/[:space:]]+@'
POLA_BERKAS_TERLARANG='(^|/)(\.env|id_rsa|id_ed25519)$|\.(pem|p12|pfx|key)$'
PESAN_BERSIH='Tidak ada rahasia terdeteksi pada berkas yang dilacak.'

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

# --- mesin pemindaian --------------------------------------------------------

# pindai_pola <pola> — membaca daftar nama berkas dari stdin, mencetak baris
# temuan ke stdout. Status: 0 = tidak ada temuan, 1 = ada temuan,
# 2 = galat alat (grep tidak dapat menyelesaikan pemindaian).
pindai_pola() {
    _pola=$1
    _ada=0
    while IFS= read -r _berkas; do
        [ -n "$_berkas" ] || continue
        # `-e` WAJIB: POLA_KUNCI dimulai dengan "-----BEGIN" dan tanpa -e grep
        # memparsenya sebagai option (exit 2) => temuan tak terlihat.
        _keluaran=$(grep -nI -E -e "$_pola" -- "$_berkas") && _st=0 || _st=$?
        if [ "$_st" -gt 1 ]; then
            echo "grep gagal pada '$_berkas' (status $_st)" >&2
            return 2
        fi
        if [ "$_st" -eq 0 ]; then
            _ada=1
            printf '%s\n' "$_keluaran"
        fi
    done
    [ "$_ada" -eq 0 ]
}

# pindai_daftar <daftar-berkas-baris-baru> <pola> —
# status: 0 = bersih, 1 = temuan, 2 = galat alat.
pindai_daftar() {
    _daftar=$1
    _pola=$2
    [ -n "$_daftar" ] || return 0
    printf '%s\n' "$_daftar" | pindai_pola "$_pola" >"$tmp/temuan" 2>"$tmp/galat" && _st=0 || _st=$?
    if [ -s "$tmp/galat" ]; then
        while IFS= read -r _baris; do
            echo "check-secrets: $_baris" >&2
        done <"$tmp/galat"
        return 2
    fi
    [ "$_st" -le 1 ] || return 2
    # Baris yang nilainya berasal dari interpolasi ${...} bukan rahasia
    # (contoh konfigurasi). Blind spot yang disadari dan didokumentasikan di
    # docs/design/f1-22/HASIL.md: kunci NYATA pada baris yang sekaligus memuat
    # ${...} tidak tertangkap pemeriksaan ini — repo tidak punya pola semacam
    # itu, dan vektor lain tetap tertangkap pemeriksaan 2/3.
    grep -v '\${' "$tmp/temuan" >"$tmp/temuan-bersih" || :
    [ -s "$tmp/temuan-bersih" ] || return 0
    return 1
}

# periksa <label> <pola> — keluar 1 bila ada temuan, 2 bila alat gagal.
periksa() {
    _label=$1
    _pola=$2
    pindai_daftar "$daftar_berkas" "$_pola" && _st=0 || _st=$?
    case $_st in
        0) return 0 ;;
        1)
            echo "$_label:" >&2
            while IFS= read -r _baris; do
                printf '%s\n' "$_baris" >&2
            done <"$tmp/temuan-bersih"
            exit 1
            ;;
        *)
            echo "GAGAL: pemindaian '$_label' tidak dapat diselesaikan — gerbang menolak (fail closed)" >&2
            exit 2
            ;;
    esac
}

# --- kendali diri ------------------------------------------------------------

# Setiap pola berkeyakinan tinggi WAJIB terbukti terdeteksi, contoh interpolasi
# WAJIB bersih, dan galat alat WAJIB menggagalkan pemindaian. Tanpa ini, salah
# tulis pada pola/opsi grep hanya terlihat sebagai gerbang yang hijau hampa —
# persis cacat QA putaran 1, yang tidak dapat ditangkap uji lain mana pun.
#
# Catatan tabel: contoh ditulis dengan penanda `*` yang DIHAPUS saat uji
# berjalan (`tr -d '*'`) supaya berkas skrip ini sendiri — yang juga dipindai
# oleh pemeriksaan normal — tidak memicu gerbangnya sendiri.
uji_diri() {
    _jml=0
    _lulus=0
    while IFS=';' read -r _nama _jenis _harap _isi; do
        case $_nama in '' | '#'*) continue ;; esac
        _jml=$((_jml + 1))
        case $_jenis in
            kunci) _pola=$POLA_KUNCI ;;
            dsn) _pola=$POLA_DSN ;;
            *)
                echo "uji-diri: jenis pola tak dikenal '$_jenis'" >&2
                exit 2
                ;;
        esac
        case $_harap in
            temuan) _ingin=1 ;;
            bersih) _ingin=0 ;;
            *)
                echo "uji-diri: harapan tak dikenal '$_harap'" >&2
                exit 2
                ;;
        esac
        _berkas_uji="$tmp/uji-$_nama.txt"
        printf '%s\n' "$_isi" | tr -d '*' >"$_berkas_uji"
        pindai_daftar "$_berkas_uji" "$_pola" && _st=0 || _st=$?
        if [ "$_st" -eq "$_ingin" ]; then
            _lulus=$((_lulus + 1))
            echo "LULUS  $_nama -> status $_st (diharapkan $_ingin)"
        else
            echo "GAGAL  $_nama -> status $_st, diharapkan $_ingin" >&2
        fi
    done <<'KASUS'
# nama;kunci|dsn;harapan;contoh-isi_tanpa-penanda-bintang
kunci-privat-rsa;kunci;temuan;-----BEGIN *RSA PRIVATE KEY-----
kunci-privat-ec;kunci;temuan;-----BEGIN *EC PRIVATE KEY-----
kunci-privat-openssh;kunci;temuan;-----BEGIN *OPENSSH PRIVATE KEY-----
kunci-privat-pgp;kunci;temuan;-----BEGIN *PGP PRIVATE KEY-----
aws-access-key-id;kunci;temuan;AKIA*IOSFODNN7EXAMPLE
aws-secret-access-key;kunci;temuan;aws_secret_access_key = *abcdefghijklmnopqrstuvwxyz123456
anthropic-api-key;kunci;temuan;sk-ant-*api03abcdefghijklmnop
github-token;kunci;temuan;ghp_*ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789
slack-token;kunci;temuan;xoxb-*123456789012-abcdefghij
gcp-service-account;kunci;temuan;SERVICE_ACCOUNT_JSON=*{
rajah-interpolasi;kunci;bersih;aws_secret_access_key = *ABCDEFGHIJKLMNOPQRSTUVWX${XXX}
konfigurasi-biasa;kunci;bersih;GRAFANA_ADMIN_PASSWORD=${GRAFANA_ADMIN_PASSWORD}
dsn-kredensial-literal;dsn;temuan;post*gres://grahita:rahasia123@db:5432/grahita
dsn-interpolasi;dsn;bersih;DATABASE_URL=post*${GRAHITA_DSN_ISI}
KASUS

    # Kendali galat alat: berkas yang tidak dapat dibaca grep HARUS membuat
    # pemindaian menolak (status 2), bukan melaporkan bersih.
    if [ "$(id -u)" -ne 0 ]; then
        _jml=$((_jml + 1))
        printf '%s\n' 'konfigurasi tanpa rahasia' >"$tmp/uji-tak-terbaca.txt"
        chmod 000 "$tmp/uji-tak-terbaca.txt"
        pindai_daftar "$tmp/uji-tak-terbaca.txt" "$POLA_KUNCI" && _st=0 || _st=$?
        chmod 600 "$tmp/uji-tak-terbaca.txt"
        if [ "$_st" -eq 2 ]; then
            _lulus=$((_lulus + 1))
            echo "LULUS  berkas-tak-terbaca -> status 2 (fail closed)"
        else
            echo "GAGAL  berkas-tak-terbaca -> status $_st, diharapkan 2 (fail closed)" >&2
        fi
    else
        echo "LEWAT  berkas-tak-terbaca (dijalankan sebagai root: izin berkas tidak berlaku)"
    fi

    echo
    if [ "$_lulus" -eq "$_jml" ]; then
        echo "UJI DIRI check-secrets: LULUS ($_lulus/$_jml kasus)"
        return 0
    fi
    echo "UJI DIRI check-secrets: GAGAL ($_lulus/$_jml kasus)" >&2
    return 1
}

# --- utama -------------------------------------------------------------------

jalankan_pemindaian() {
    if ! command -v git >/dev/null 2>&1; then
        echo "git tidak tersedia" >&2
        exit 2
    fi

    # Berkas contoh environment memang berisi kredensial pengembangan, bukan
    # rahasia; ia tetap diperiksa untuk pola kunci privat/token.
    daftar_berkas=$(git ls-files | grep -v -E '(^|/)\.env\.example$' || true)

    periksa "Rahasia (kunci privat/kredensial/token) terdeteksi pada berkas yang dilacak" "$POLA_KUNCI"
    periksa "DSN dengan kredensial literal ditemukan" "$POLA_DSN"

    # Berkas rahasia tidak boleh dilacak.
    terlarang=$(printf '%s\n' "$daftar_berkas" | grep -E "$POLA_BERKAS_TERLARANG" 2>"$tmp/galat") && _st=0 || _st=$?
    if [ -s "$tmp/galat" ] || [ "$_st" -gt 1 ]; then
        echo "GAGAL: pemeriksaan nama berkas tidak dapat diselesaikan — gerbang menolak (fail closed)" >&2
        exit 2
    fi
    if [ -n "$terlarang" ]; then
        echo "Berkas rahasia tidak boleh dilacak git:" >&2
        printf '%s\n' "$terlarang" >&2
        exit 1
    fi

    echo "$PESAN_BERSIH"
}

case ${1:-} in
    '') jalankan_pemindaian ;;
    --uji-diri) uji_diri ;;
    -h | --help)
        printf 'Pemakaian: %s [--uji-diri]\n' "$0"
        printf '  (tanpa argumen) memindai berkas yang dilacak git\n'
        printf '  --uji-diri       kendali negatif/positif mesin pemindaian\n'
        ;;
    *)
        echo "argumen tidak dikenal: $1" >&2
        exit 2
        ;;
esac
