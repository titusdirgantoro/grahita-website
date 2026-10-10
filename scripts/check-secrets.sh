#!/usr/bin/env sh
# check-secrets.sh — memastikan tidak ada rahasia yang ikut masuk repo
# (Architecture.md §6).
#
# Sengaja memakai daftar pola berkeyakinan tinggi agar tidak menghasilkan
# alarm palsu, dan hanya memeriksa berkas yang dilacak git.
#
# Gerbang ini FAIL CLOSED pada SEMUA jalur: bila pendaftaran berkas
# (`git ls-files`) gagal atau melaporkan daftar berkas kosong tak terduga,
# atau bila grep sendiri gagal (mis. berkas tak terbaca), skrip MENOLAK (exit 2) —
# tidak pernah melaporkan "bersih" karena alatnya tidak jalan.
#
# Perbaikan QA F1-22 putaran 2 (cacat NON-PEMBLOKIR "status ditelan" pada
# perintah KEDUA): `daftar_berkas=$(git ls-files | grep -v ... || true)`
# menelan status `git ls-files`, sehingga di direktori TANPA `.git` skrip
# mencetak "Tidak ada rahasia terdeteksi" + exit 0 tanpa memindai apa pun.
# Sekarang: status `git ls-files` diperiksa, daftar kosong (mentah maupun
# sesudah penyaringan) DITOLAK dengan exit 2, `pindai_daftar` menolak daftar
# kosong, dan `--uji-diri` memuat kendali tetap `registrasi-berkas-gagal` /
# `registrasi-berkas-kosong` (wajib status 2) + `registrasi-berkas-bersih`
# (wajib status 0, supaya kendalinya tidak hampa/"selalu menolak").
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
# Keluar: 0 bersih, 1 rahasia terdeteksi, 2 alat pemindai/pendaftaran berkas
# gagal (fail closed).
set -eu

# Jalur absolut skrip ini dihitung SEBELUM `cd`: kendali `--uji-diri` menjalankan
# SKRIP INI SENDIRI dengan `git` palsu di depan PATH, sehingga jalur itu harus
# tetap benar setelah skrip berpindah ke akar repo.
SKRIP_DIR=$(cd "$(dirname "$0")" && pwd)
SKRIP_ABS="$SKRIP_DIR/$(basename "$0")"

cd "$SKRIP_DIR/.."

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
# status: 0 = bersih, 1 = temuan, 2 = galat alat ATAU daftar berkas kosong.
# Daftar kosong TIDAK boleh dibaca sebagai "bersih": itu jalur fail-open yang
# membuat gerbang hijau tanpa memindai apa pun (temuan QA F1-22 putaran 2).
pindai_daftar() {
    _daftar=$1
    _pola=$2
    [ -n "$_daftar" ] || return 2
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

# Kendali FAIL CLOSED jalur pendaftaran berkas (temuan QA F1-22 putaran 2).
# Tiga kasus menjalankan SKRIP INI SENDIRI (jalur pemindaian normal, `$0` tanpa
# argumen) dengan `git` palsu di depan PATH — jadi yang diuji adalah perilaku
# gerbang yang sesungguhnya, bukan salinan logikanya di dalam uji:
#   registrasi-berkas-gagal  : `git ls-files` gagal  -> WAJIB status 2
#   registrasi-berkas-kosong : git sukses, daftar kosong -> WAJIB status 2
#   registrasi-berkas-bersih : jalur normal tetap hijau -> WAJIB status 0
# Kasus ketiga penting supaya kendalinya tidak hampa: implementasi yang "selalu
# exit 2" akan lulus dua kasus pertama tetapi GAGAL di kasus ketiga.
uji_registrasi_berkas() {
    _dir_gagal="$tmp/git-palsu-gagal"
    _dir_kosong="$tmp/git-palsu-kosong"
    _dir_bersih="$tmp/git-palsu-bersih"
    mkdir -p "$_dir_gagal" "$_dir_kosong" "$_dir_bersih"

    # `git` palsu 1: meniru "not a git repository" (exit 128, seperti di
    # direktori tanpa .git).
    cat >"$_dir_gagal/git" <<'GIT_PALSU_GAGAL'
#!/usr/bin/env sh
printf '%s\n' 'fatal: not a git repository (or any of the parent directories): .git' >&2
exit 128
GIT_PALSU_GAGAL

    # `git` palsu 2: sukses, tetapi tidak melaporkan berkas apa pun.
    cat >"$_dir_kosong/git" <<'GIT_PALSU_KOSONG'
#!/usr/bin/env sh
exit 0
GIT_PALSU_KOSONG

    # `git` palsu 3 (kendali positif): satu berkas terlacak yang bersih.
    _bersih="$tmp/uji-registrasi-bersih.txt"
    printf '%s\n' 'konfigurasi tanpa rahasia' >"$_bersih"
    cat >"$_dir_bersih/git" <<GIT_PALSU_BERSIH
#!/usr/bin/env sh
printf '%s\n' '$_bersih'
exit 0
GIT_PALSU_BERSIH

    chmod +x "$_dir_gagal/git" "$_dir_kosong/git" "$_dir_bersih/git"

    for _kasus in gagal kosong bersih; do
        case $_kasus in
            gagal)
                _dir="$_dir_gagal"
                _ingin=2
                _ket='git ls-files gagal -> wajib 2 (fail closed)'
                ;;
            kosong)
                _dir="$_dir_kosong"
                _ingin=2
                _ket='daftar berkas kosong -> wajib 2 (fail closed)'
                ;;
            *)
                _dir="$_dir_bersih"
                _ingin=0
                _ket='jalur normal tetap hijau (bukan "selalu menolak")'
                ;;
        esac
        _jml=$((_jml + 1))
        _keluaran="$tmp/uji-registrasi-$_kasus.keluaran"
        PATH="$_dir:$PATH" sh "$SKRIP_ABS" >"$_keluaran" 2>&1 && _st=0 || _st=$?
        if [ "$_st" -eq "$_ingin" ]; then
            _lulus=$((_lulus + 1))
            echo "LULUS  registrasi-berkas-$_kasus -> status $_st (diharapkan $_ingin; $_ket)"
        else
            echo "GAGAL  registrasi-berkas-$_kasus -> status $_st, diharapkan $_ingin; $_ket" >&2
            while IFS= read -r _baris; do
                printf '       | %s\n' "$_baris" >&2
            done <"$_keluaran"
        fi
    done
}


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

    uji_registrasi_berkas

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

    # Status `git ls-files` TIDAK boleh ditelan. Cacat QA F1-22 putaran 2:
    # `daftar_berkas=$(git ls-files | grep -v ... || true)` — bila git gagal
    # (mis. dijalankan di luar pohon git), `|| true` menelan statusnya, daftar
    # menjadi kosong, ketiga pemeriksaan dilewati, dan gerbang mencetak
    # "Tidak ada rahasia terdeteksi" dengan exit 0 TANPA memindai apa pun.
    _semua=$(git ls-files) && _st=0 || _st=$?
    if [ "$_st" -ne 0 ]; then
        echo "GAGAL: pendaftaran berkas (git ls-files) gagal (status $_st) — gerbang menolak (fail closed)" >&2
        exit 2
    fi
    if [ -z "$_semua" ]; then
        echo "GAGAL: git ls-files melaporkan daftar berkas kosong — gerbang menolak (fail closed)" >&2
        exit 2
    fi

    # Berkas contoh environment memang berisi kredensial pengembangan, bukan
    # rahasia; ia tetap diperiksa untuk pola kunci privat/token.
    daftar_berkas=$(printf '%s\n' "$_semua" | grep -v -E '(^|/)\.env\.example$') && _st=0 || _st=$?
    if [ "$_st" -gt 1 ]; then
        echo "GAGAL: penyaringan daftar berkas gagal (grep status $_st) — gerbang menolak (fail closed)" >&2
        exit 2
    fi
    if [ -z "$daftar_berkas" ]; then
        echo "GAGAL: daftar berkas yang dipindai kosong setelah penyaringan — gerbang menolak (fail closed)" >&2
        exit 2
    fi

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
