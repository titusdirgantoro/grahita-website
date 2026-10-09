#!/usr/bin/env sh
# deploy.sh — menaikkan satu rilis Grahita ke host staging/produksi lewat SSH.
#
# Dipanggil oleh .github/workflows/release.yml. Seluruh nilai rahasia datang
# dari environment (GitHub Secrets) dan TIDAK pernah ditulis di repo.
#
# Wajib: SSH_HOST, SSH_USER, SSH_KEY, IMAGE_TAG, GITHUB_REPOSITORY
# Opsional: SSH_PORT (22), REMOTE_DIR (/srv/grahita), LINGKUNGAN (staging),
#           GHCR_USER, GHCR_TOKEN
#
# Langkah di host (urutannya disengaja):
#   1. checkout commit IMAGE_TAG supaya berkas Compose/Caddy cocok dengan image
#   2. login GHCR, tarik image api/worker/web
#   3. JALANKAN MIGRASI lebih dulu — migrasi wajib kompatibel mundur
#      (Architecture.md §9), jadi versi lama masih bisa melayani saat migrasi naik
#   4. naikkan api/worker/web tanpa menyentuh layanan lain
#
# Rollback = jalankan ulang skrip ini dengan IMAGE_TAG commit sebelumnya.
set -eu

: "${SSH_HOST:?SSH_HOST wajib diisi}"
: "${SSH_USER:?SSH_USER wajib diisi}"
: "${SSH_KEY:?SSH_KEY wajib diisi (kunci privat SSH)}"
: "${IMAGE_TAG:?IMAGE_TAG wajib diisi}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY wajib diisi}"

SSH_PORT=${SSH_PORT:-22}
REMOTE_DIR=${REMOTE_DIR:-/srv/grahita}
LINGKUNGAN=${LINGKUNGAN:-staging}
REGISTRY=${REGISTRY:-ghcr.io}
IMAGE_PREFIX="${REGISTRY}/${GITHUB_REPOSITORY}"

case "$LINGKUNGAN" in
staging | production) ;;
*)
    echo "LINGKUNGAN harus staging atau production (dapat: $LINGKUNGAN)" >&2
    exit 1
    ;;
esac

# Berkas Compose yang dipakai di host. Overlay observability selalu ikut supaya
# metrik/alarm tetap hidup selama rilis.
COMPOSE_FILES="-f docker-compose.yml -f docker-compose.${LINGKUNGAN}.yml -f docker-compose.observability.yml"

KUNCI=$(mktemp)
chmod 600 "$KUNCI"
printf '%s\n' "$SSH_KEY" > "$KUNCI"
trap 'rm -f "$KUNCI"' EXIT

ssh_ke() {
    ssh -i "$KUNCI" -p "$SSH_PORT" \
        -o StrictHostKeyChecking=accept-new \
        -o BatchMode=yes \
        -o ConnectTimeout=15 \
        "$SSH_USER@$SSH_HOST" "$@"
}

echo "== deploy $LINGKUNGAN: commit $IMAGE_TAG ke $SSH_USER@$SSH_HOST:$REMOTE_DIR =="

# Seluruh perintah remote dikirim sebagai satu skrip agar tidak ada langkah
# setengah jalan bila koneksi terputus.
skrip=$(cat <<REMOTE
set -eu
cd '$REMOTE_DIR'
echo '-- checkout $IMAGE_TAG --'
git fetch --prune --tags origin
git checkout --force '$IMAGE_TAG'

echo '-- login registry --'
printf '%s' '$GHCR_TOKEN' | docker login '$REGISTRY' -u '$GHCR_USER' --password-stdin

export IMAGE_TAG='$IMAGE_TAG'
export IMAGE_PREFIX='$IMAGE_PREFIX'

echo '-- tarik image --'
docker compose $COMPOSE_FILES pull api worker web

echo '-- migrasi (kompatibel mundur) --'
docker compose $COMPOSE_FILES run --rm migrate

echo '-- naikkan layanan --'
docker compose $COMPOSE_FILES up -d --no-deps --remove-orphans api worker web

echo '-- status --'
docker compose $COMPOSE_FILES ps
REMOTE
)

ssh_ke "$skrip"
echo "== deploy $LINGKUNGAN selesai =="
