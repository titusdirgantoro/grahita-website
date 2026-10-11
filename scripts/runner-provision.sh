#!/usr/bin/env bash
# scripts/runner-provision.sh — provisioning runner TANPA sudo dan tanpa
# rahasia: menautkan biner toolchain (go, gofmt, golangci-lint) ke sebuah
# direktori yang sudah ada di PATH runner (default `~/bin`) sehingga perintah
# polos seperti `go version`, `make test`, dan `cd api && golangci-lint run ./...`
# menemukannya tanpa set PATH interaktif.
#
# Latar (kartu t_ac58a28e): runner uji tidak punya `go` di PATH. Perbaikan
# utama ada di repo (`scripts/go-env.sh` + Makefile) dan itu sudah cukup untuk
# `make *`; skrip ini hanya menutup celah perintah `go`/`golangci-lint` polos
# yang dipanggil langsung dari shell tester.
#
# Pemakaian:
#   ./scripts/runner-provision.sh            # pasang tautan (idempotent)
#   ./scripts/runner-provision.sh --cek      # hanya laporkan keadaan
#   ./scripts/runner-provision.sh --hapus    # hapus tautan yang dibuat skrip ini
#   GRAHITA_RUNNER_BIN=/jalur/bin ./scripts/runner-provision.sh
#
# Aman dijalankan berulang: tautan yang sudah benar dibiarkan, tautan yang
# menunjuk berkas LAIN dilaporkan dan tidak ditimpa (kecuali --paksa).
set -u

_diri=$(cd "$(dirname "$0")" && pwd)
_skrip_toolchain="$_diri/go-env.sh"
BIN_DIR=${GRAHITA_RUNNER_BIN:-$HOME/bin}
PAKSA=0
MODE=pasang

for _a in "$@"; do
  case "$_a" in
    --cek) MODE=cek ;;
    --hapus) MODE=hapus ;;
    --paksa) PAKSA=1 ;;
    -h | --help)
      printf '%s\n' "Pemakaian: $0 [--cek|--hapus] [--paksa] (BIN_DIR=$BIN_DIR)"
      exit 0
      ;;
    *)
      printf 'argumen tidak dikenal: %s\n' "$_a" >&2
      exit 2
      ;;
  esac
done

if [ ! -x "$_skrip_toolchain" ]; then
  printf 'Tidak menemukan %s\n' "$_skrip_toolchain" >&2
  exit 1
fi

_pasang() { # $1 = nama biner tujuan, $2 = sumber
  _tujuan="$BIN_DIR/$1"
  if [ -e "$_tujuan" ] || [ -L "$_tujuan" ]; then
    _kini=$(readlink "$_tujuan" 2>/dev/null || true)
    if [ "$_kini" = "$2" ]; then
      printf '  %-14s sudah benar -> %s\n' "$1" "$2"
      return 0
    fi
    if [ "$PAKSA" -ne 1 ]; then
      printf '  %-14s DILEWATI: %s sudah ada (bukan tautan ke %s)\n' "$1" "$_tujuan" "$2"
      return 1
    fi
  fi
  ln -sfn "$2" "$_tujuan"
  printf '  %-14s dipasang -> %s\n' "$1" "$2"
}

_hapus() {
  for _n in go gofmt golangci-lint; do
    _tujuan="$BIN_DIR/$_n"
    if [ -L "$_tujuan" ]; then
      _kini=$(readlink "$_tujuan" 2>/dev/null || true)
      case "$_kini" in
        "$HOME"/toolchain/* | *"$HOME"/sdk/* | "$_diri"/*)
          rm -f "$_tujuan"
          printf '  %-14s tautan dihapus (dulu -> %s)\n' "$_n" "$_kini"
          ;;
        *) printf '  %-14s DIBIARKAN: bukan tautan toolchain (%s)\n' "$_n" "$_kini" ;;
      esac
    fi
  done
}

_lapor() {
  printf 'BIN_DIR        : %s\n' "$BIN_DIR"
  printf 'PATH memuatnya  : %s\n' "$(printf '%s' "$PATH" | tr ':' '\n' | grep -Fx "$BIN_DIR" >/dev/null 2>&1 && echo ya || echo TIDAK)"
  for _n in go gofmt golangci-lint; do
    if _p=$(command -v "$_n" 2>/dev/null); then
      printf '%-15s: %s (%s)\n' "$_n" "$_p" "$(readlink "$_p" 2>/dev/null || printf 'bukan tautan')"
    else
      printf '%-15s: TIDAK ADA di PATH\n' "$_n"
    fi
  done
}

case "$MODE" in
  cek)
    _lapor
    ;;
  hapus)
    _hapus
    _lapor
    ;;
  pasang)
    if _go=$("$_skrip_toolchain" --go); then :; else
      printf 'Toolchain Go >= 1.27 tidak ditemukan; jalankan dulu %s lalu perbaiki toolchain.\n' "$_skrip_toolchain" >&2
      exit 1
    fi
    _godir=$(dirname "$_go")
    mkdir -p "$BIN_DIR"
    printf 'Provisioning runner di %s\n' "$BIN_DIR"
    _pasang go "$_go" || true
    if [ -x "$_godir/gofmt" ]; then _pasang gofmt "$_godir/gofmt" || true; fi
    if _gl=$("$_skrip_toolchain" --golangci-lint); then
      case "$_gl" in
        golangci-lint) printf '  %-14s DILEWATI: golangci-lint tidak ditemukan\n' golangci-lint ;;
        *) _pasang golangci-lint "$_gl" || true ;;
      esac
    fi
    printf '\nKeadaan setelah provisioning:\n'
    _lapor
    printf '\nLangkah berikutnya (dari root repo): make toolchain-cek && make test\n'
    ;;
esac
