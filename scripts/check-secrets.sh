#!/usr/bin/env sh
# check-secrets.sh — memastikan tidak ada rahasia yang ikut masuk repo
# (Architecture.md §6).
#
# Sengaja memakai daftar pola berkeyakinan tinggi agar tidak menghasilkan
# alarm palsu, dan hanya memeriksa berkas yang dilacak git.
set -eu

cd "$(dirname "$0")/.."

if ! command -v git >/dev/null 2>&1; then
    echo "git tidak tersedia" >&2
    exit 1
fi

# Berkas contoh environment memang berisi kredensial pengembangan, bukan
# rahasia; ia tetap diperiksa untuk pola kunci privat/token.
berkas=$(git ls-files | grep -v -E '(^|/)\.env\.example$' || true)

# Pola berkeyakinan tinggi: kunci privat, kredensial AWS, token penyedia.
pola='-----BEGIN (RSA|EC|OPENSSH|PGP) PRIVATE KEY-----|AKIA[0-9A-Z]{16}|aws_secret_access_key[[:space:]]*=[[:space:]]*[A-Za-z0-9/+=]{20,}|sk-ant-[A-Za-z0-9_-]{20,}|ghp_[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|SERVICE_ACCOUNT_JSON[[:space:]]*=[[:space:]]*\{'

if [ -n "$berkas" ]; then
    hasil=$(printf '%s\n' "$berkas" | xargs -r grep -nI -E "$pola" 2>/dev/null | grep -v '\${' || true)
    if [ -n "$hasil" ]; then
        echo "Rahasia terdeteksi pada berkas yang dilacak:" >&2
        printf '%s\n' "$hasil" >&2
        exit 1
    fi
fi

# DSN database dengan kredensial literal (bukan interpolasi ${...}).
if [ -n "$berkas" ]; then
    dsn=$(printf '%s\n' "$berkas" | xargs -r grep -nI -E 'postgres(ql)?://[^:@/[:space:]]+:[^@/[:space:]]+@' 2>/dev/null | grep -v '\${' || true)
    if [ -n "$dsn" ]; then
        echo "DSN dengan kredensial literal ditemukan:" >&2
        printf '%s\n' "$dsn" >&2
        exit 1
    fi
fi

# Berkas rahasia tidak boleh dilacak.
terlarang=$(printf '%s\n' "$berkas" | grep -E '(^|/)(\.env|id_rsa|id_ed25519)$|\.(pem|p12|pfx|key)$' || true)
if [ -n "$terlarang" ]; then
    echo "Berkas rahasia tidak boleh dilacak git:" >&2
    printf '%s\n' "$terlarang" >&2
    exit 1
fi

echo "Tidak ada rahasia terdeteksi pada berkas yang dilacak."
