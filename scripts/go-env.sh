#!/usr/bin/env bash
# scripts/go-env.sh — pemilih toolchain Go/golangci-lint yang eksplisit dan
# repeatable untuk runner uji maupun pengembang.
#
# Latar (kartu t_ac58a28e, kelanjutan T5-ulang t_e36576b6): runner uji tidak
# memiliki `go` pada PATH, sehingga `make test` berhenti dengan
# `/bin/sh: 1: go: not found` (exit 127) dan `golangci-lint run ./...` berhenti
# dengan `go command required, not found` (exit 3). Keduanya kegagalan
# provisioning/PATH, bukan cacat kode Go. Skrip ini menemukan toolchain
# Go >= 1.27 secara deterministik lalu mencetak jalur/PATH yang dapat dipakai
# ulang oleh Makefile (lihat `GO`/`GOLANGCI_LINT`) maupun tester.
#
# Pemakaian:
#   ./scripts/go-env.sh                      # laporan jalur + versi
#   eval "$(./scripts/go-env.sh --export)"   # tambahkan toolchain ke PATH sesi ini
#   ./scripts/go-env.sh --go                 # cetak jalur biner go (exit 1 bila tak memadai)
#   ./scripts/go-env.sh --golangci-lint      # cetak jalur golangci-lint
#   ./scripts/go-env.sh --cek                # gerbang: exit 1 bila Go < 1.27 / lint hilang
#   ./scripts/go-env.sh --uji-diri           # kendali positif + negatif skrip ini
#
# Urutan pencarian (kandidat pertama yang versinya >= 1.27 dipakai):
#   1. GRAHITA_GO                  (jalur biner go; otoritatif — salah versi = gagal)
#   2. GOROOT/bin/go
#   3. $HOME/toolchain/go/bin/go   (lokasi toolchain runner ini; docs/design/f3-29)
#   4. $HOME/sdk/go*/bin/go        (instalasi lewat `go install golang.org/dl/...`)
#   5. /usr/local/go/bin/go, /usr/lib/go-1.27/bin/go, /usr/lib/go/bin/go,
#      /snap/bin/go, /opt/go/bin/go
#   6. `go` yang ada di PATH
# GRAHITA_GO_KANDIDAT menggantikan daftar 2-6 (dipakai kendali diri/uji dan
# override darurat). GRAHITA_GOLANGCI_LINT menetapkan golangci-lint secara
# otoritatif.
#
# Tanpa rahasia: skrip hanya membaca jalur biner dan versinya.
set -u

MAYOR_MIN=1
MINOR_MIN=27

_info() { printf '%s\n' "$*" >&2; }

_pemakaian() {
  printf '%s\n' \
    'Pemakaian: ./scripts/go-env.sh [MODE]' \
    '  (tanpa mode)        laporan jalur + versi toolchain' \
    '  --export            cetak baris export PATH untuk `eval`' \
    '  --go                cetak jalur biner go (exit 1 bila < 1.27)' \
    '  --golangci-lint     cetak jalur golangci-lint' \
    '  --cek               gerbang: exit 1 bila Go < 1.27 / lint hilang' \
    '  --uji-diri          kendali positif + negatif skrip ini'
}

_versi_penuh() { # $1 = biner go
  "$1" version 2>/dev/null | head -n1
}

_mayor_minor() { # $1 = biner go -> "1 27" (kosong bila tak terparse)
  "$1" version 2>/dev/null | sed -n '1s/^go version go\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p'
}

_memadai() { # $1 mayor $2 minor
  case "${1:-}${2:-}" in
    *[!0-9]*|"") return 1 ;;
  esac
  if [ "$1" -gt "$MAYOR_MIN" ]; then return 0; fi
  if [ "$1" -eq "$MAYOR_MIN" ] && [ "$2" -ge "$MINOR_MIN" ]; then return 0; fi
  return 1
}

_daftar_kandidat() {
  if [ -n "${GRAHITA_GO_KANDIDAT:-}" ]; then
    for _k in $GRAHITA_GO_KANDIDAT; do printf '%s\n' "$_k"; done
    return 0
  fi
  if [ -n "${GOROOT:-}" ]; then printf '%s\n' "$GOROOT/bin/go"; fi
  printf '%s\n' "$HOME/toolchain/go/bin/go"
  for _k in "$HOME"/sdk/go*/bin/go; do
    [ -x "$_k" ] && printf '%s\n' "$_k"
  done
  printf '%s\n' /usr/local/go/bin/go /usr/lib/go-1.27/bin/go /usr/lib/go/bin/go /snap/bin/go /opt/go/bin/go
  _jalur=$(command -v go 2>/dev/null || true)
  [ -n "$_jalur" ] && printf '%s\n' "$_jalur"
  return 0
}

_normalisasi_kandidat() { # $1 = kandidat; direktori toolchain -> <dir>/go
  if [ -d "$1" ] && [ -x "$1/go" ]; then
    printf '%s\n' "$1/go"
    return 0
  fi
  printf '%s\n' "$1"
}

_cari_go() {
  if [ -n "${GRAHITA_GO:-}" ]; then
    _g=$(_normalisasi_kandidat "$GRAHITA_GO")
    if [ ! -f "$_g" ] || [ ! -x "$_g" ]; then
      _info "GRAHITA_GO=$GRAHITA_GO bukan berkas yang dapat dieksekusi."
      return 1
    fi
    _mm=$(_mayor_minor "$_g")
    if [ -n "$_mm" ] && _memadai $_mm; then
      printf '%s\n' "$_g"
      return 0
    fi
    _info "GRAHITA_GO=$GRAHITA_GO bukan Go >= ${MAYOR_MIN}.${MINOR_MIN} (terbaca: ${_mm:-tidak dikenali})."
    return 1
  fi

  _ditolak=""
  for _k in $(_daftar_kandidat); do
    [ -n "$_k" ] || continue
    _k=$(_normalisasi_kandidat "$_k")
    # Direktori juga lolos uji -x, jadi berkasnya harus diuji eksplisit.
    [ -f "$_k" ] || continue
    [ -x "$_k" ] || continue
    _mm=$(_mayor_minor "$_k")
    if [ -n "$_mm" ] && _memadai $_mm; then
      printf '%s\n' "$_k"
      return 0
    fi
    _ditolak="$_ditolak ${_k}(go${_mm:-?})"
  done
  _info "Tidak menemukan biner Go >= ${MAYOR_MIN}.${MINOR_MIN}. Kandidat dinilai:${_ditolak:- (tidak ada)}"
  _info "Pasang Go ${MAYOR_MIN}.${MINOR_MIN}+ di \$HOME/toolchain/go, atau setel GRAHITA_GO=/jalur/ke/go."
  return 1
}

_cari_golangci() {
  if [ -n "${GRAHITA_GOLANGCI_LINT:-}" ]; then
    if [ -x "$GRAHITA_GOLANGCI_LINT" ]; then
      printf '%s\n' "$GRAHITA_GOLANGCI_LINT"
      return 0
    fi
    return 1
  fi
  for _k in "$HOME/bin/golangci-lint" "$HOME/go/bin/golangci-lint" /usr/local/bin/golangci-lint \
    /usr/bin/golangci-lint "$(command -v golangci-lint 2>/dev/null || true)"; do
    [ -n "$_k" ] || continue
    [ -x "$_k" ] || continue
    printf '%s\n' "$_k"
    return 0
  done
  return 1
}

_laporan() {
  printf 'Toolchain grahita-website (Go >= %s.%s; Architecture.md \xc2\xa71, CI GO_VERSION=1.27)\n' "$MAYOR_MIN" "$MINOR_MIN"
  if _go=$(_cari_go); then
    printf '  go            : %s\n' "$_go"
    printf '  versi         : %s\n' "$(_versi_penuh "$_go")"
  else
    printf '  go            : TIDAK MEMADAI\n'
  fi
  if _gl=$(_cari_golangci); then
    printf '  golangci-lint : %s\n' "$_gl"
    printf '  versi lint    : %s\n' "$("$_gl" version 2>/dev/null | head -n1)"
  else
    printf '  golangci-lint : TIDAK DITEMUKAN\n'
  fi
  printf '  pakai di sesi : eval "$(./scripts/go-env.sh --export)"\n'
  printf '  lewat make    : make toolchain-cek / make test / make lint\n'
}

_kutip() { # kutip aman untuk dieval oleh sh/bash; `$` DIBIARKAN agar $PATH mengembang saat eval
  printf '"%s"' "$(printf '%s' "$1" | sed 's/[\\"`]/\\&/g')"
}

_ekspor() {
  _go=$(_cari_go) || exit 1
  _dirs=$(dirname "$_go")
  if _gl=$(_cari_golangci); then
    _gldir=$(dirname "$_gl")
    [ "$_gldir" = "$_dirs" ] || _dirs="$_dirs:$_gldir"
  fi
  printf 'export PATH=%s\n' "$(_kutip "$_dirs:\$PATH")"
}

_cek() {
  _ok=0
  if _go=$(_cari_go); then
    printf 'Go            : %s (%s)\n' "$_go" "$(_versi_penuh "$_go")"
  else
    printf 'Go            : TIDAK MEMADAI\n' >&2
    _ok=1
  fi
  if _gl=$(_cari_golangci); then
    printf 'golangci-lint : %s\n' "$_gl"
  else
    printf 'golangci-lint : TIDAK DITEMUKAN\n' >&2
    _ok=1
  fi
  return $_ok
}

# ---------- kendali diri (positif + negatif) ----------

_stub_go() { # $1 = direktori, $2 = versi (mis. 1.27.1)
  mkdir -p "$1"
  cat >"$1/go" <<EOF
#!/bin/sh
if [ "\${1:-}" = "version" ]; then
  echo "go version go$2 linux/amd64"
  exit 0
fi
exit 0
EOF
  chmod +x "$1/go"
}

_stub_golangci() { # $1 = direktori
  mkdir -p "$1"
  cat >"$1/golangci-lint" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "version" ]; then echo "golangci-lint has version 2.14.0 (stub)"; exit 0; fi
exit 0
EOF
  chmod +x "$1/golangci-lint"
}

_uji_diri() {
  _tmp=$(mktemp -d "${TMPDIR:-/tmp}/uji-go-env.XXXXXX") || return 1
  trap 'rm -rf "$_tmp"' EXIT HUP INT TERM
  _lulus=0
  _gagal=0
  _luluskan() { printf 'LULUS  %s\n' "$1"; _lulus=$((_lulus + 1)); }
  _gagalkan() { printf 'GAGAL  %s\n' "$1"; _gagal=$((_gagal + 1)); }

  _skrip_dir=$(cd "$(dirname "$0")" && pwd)
  _skrip="$_skrip_dir/go-env.sh"
  _repo=$(cd "$_skrip_dir/.." && pwd)

  _stub_go "$_tmp/go1271" 1.27.1
  _stub_go "$_tmp/go1265" 1.26.5
  _stub_go "$_tmp/go1280" 1.28.0
  _stub_golangci "$_tmp/lint"

  # (1) kandidat Go 1.27.1 diterima
  _kel=$(_cari_go_kandidat "$_tmp/go1271") || _kel=""
  if [ "$_kel" = "$_tmp/go1271/go" ]; then
    _luluskan "kandidat go1.27.1 dipilih"
  else
    _gagalkan "kandidat go1.27.1 dipilih (dapat: ${_kel:-kosong})"
  fi

  # (2) kandidat Go 1.28.0 diterima (>= 1.27)
  _kel=$(_cari_go_kandidat "$_tmp/go1280") || _kel=""
  if [ "$_kel" = "$_tmp/go1280/go" ]; then
    _luluskan "kandidat go1.28.0 diterima"
  else
    _gagalkan "kandidat go1.28.0 diterima (dapat: ${_kel:-kosong})"
  fi

  # (3) kandidat Go 1.26.5 DITOLAK (kendali negatif versi)
  _kel=$(_cari_go_kandidat "$_tmp/go1265") && _kel="diterima:$_kel"
  if [ -z "$_kel" ]; then
    _luluskan "go1.26.5 ditolak"
  else
    _gagalkan "go1.26.5 ditolak ($_kel)"
  fi

  # (4) tidak ada kandidat sama sekali -> gagal dengan pesan jelas
  _pesan=$(GRAHITA_GO_KANDIDAT="$_tmp/tidak-ada" "$_skrip" --go 2>&1 >/dev/null) && _sts=0 || _sts=$?
  if [ "$_sts" -ne 0 ] && printf '%s' "$_pesan" | grep -q "Tidak menemukan biner Go"; then
    _luluskan "tanpa kandidat: exit != 0 + pesan jelas"
  else
    _gagalkan "tanpa kandidat: exit != 0 + pesan jelas (exit=$_sts, pesan=$_pesan)"
  fi

  # (5) GRAHITA_GO otoritatif: versi salah = gagal walau ada kandidat lain
  GRAHITA_GO="$_tmp/go1265/go" GRAHITA_GO_KANDIDAT="$_tmp/go1271" "$_skrip" --go >/dev/null 2>&1 && _sts=0 || _sts=$?
  if [ "$_sts" -ne 0 ]; then
    _luluskan "GRAHITA_GO versi salah ditolak (otoritatif)"
  else
    _gagalkan "GRAHITA_GO versi salah ditolak (otoritatif)"
  fi

  # (5b) GRAHITA_GO berupa DIREKTORI toolchain diterima (dinormalkan ke <dir>/go)
  _kel=$(GRAHITA_GO="$_tmp/go1271" "$_skrip" --go 2>/dev/null) || _kel=""
  if [ "$_kel" = "$_tmp/go1271/go" ]; then
    _luluskan "GRAHITA_GO berupa direktori toolchain dinormalkan"
  else
    _gagalkan "GRAHITA_GO berupa direktori toolchain dinormalkan (dapat: ${_kel:-kosong})"
  fi

  # (5c) direktori tanpa biner go DITOLAK (kendali negatif: direktori juga lolos uji -x)
  mkdir -p "$_tmp/kosong"
  _pesan=$(GRAHITA_GO="$_tmp/kosong" "$_skrip" --go 2>&1 >/dev/null) && _sts=0 || _sts=$?
  if [ "$_sts" -ne 0 ] && printf '%s' "$_pesan" | grep -q "bukan berkas yang dapat dieksekusi"; then
    _luluskan "direktori tanpa biner go ditolak dengan pesan jelas"
  else
    _gagalkan "direktori tanpa biner go ditolak dengan pesan jelas (exit=$_sts, pesan=$_pesan)"
  fi

  # (6) --go mencetak kandidat yang benar via lingkungan terkendali
  _kel=$(GRAHITA_GO_KANDIDAT="$_tmp/go1271" "$_skrip" --go) || _kel=""
  if [ "$_kel" = "$_tmp/go1271/go" ]; then
    _luluskan "--go mencetak jalur biner terpilih"
  else
    _gagalkan "--go mencetak jalur biner terpilih (dapat: ${_kel:-kosong})"
  fi

  # (7) --export membuat `go` terlihat pada PATH sesi (diuji lewat sh baru)
  _kel=$(GRAHITA_GO_KANDIDAT="$_tmp/go1271" GRAHITA_GOLANGCI_LINT="$_tmp/lint/golangci-lint" \
    sh -c 'eval "$('"$_skrip"' --export)"; command -v go; command -v golangci-lint') || _kel=""
  if [ "$_kel" = "$_tmp/go1271/go
$_tmp/lint/golangci-lint" ]; then
    _luluskan "--export menambahkan go & golangci-lint ke PATH"
  else
    _gagalkan "--export menambahkan go & golangci-lint ke PATH (dapat: $_kel)"
  fi

  # (8) --golangci-lint otoritatif bila ada, dan nama polos bila tidak ada
  _kel=$(GRAHITA_GOLANGCI_LINT="$_tmp/lint/golangci-lint" "$_skrip" --golangci-lint) || _kel=""
  if [ "$_kel" = "$_tmp/lint/golangci-lint" ]; then
    _luluskan "--golangci-lint memakai override GRAHITA_GOLANGCI_LINT"
  else
    _gagalkan "--golangci-lint memakai override GRAHITA_GOLANGCI_LINT (dapat: ${_kel:-kosong})"
  fi

  # (9) --cek gagal bila golangci-lint tidak ada (kendali negatif lint)
  GRAHITA_GO_KANDIDAT="$_tmp/go1271" GRAHITA_GOLANGCI_LINT="$_tmp/tidak-ada/golangci-lint" \
    "$_skrip" --cek >/dev/null 2>&1 && _sts=0 || _sts=$?
  if [ "$_sts" -ne 0 ]; then
    _luluskan "--cek exit != 0 saat golangci-lint hilang"
  else
    _gagalkan "--cek exit != 0 saat golangci-lint hilang"
  fi

  # (10) --cek hijau saat go >= 1.27 + golangci-lint tersedia
  GRAHITA_GO_KANDIDAT="$_tmp/go1271" GRAHITA_GOLANGCI_LINT="$_tmp/lint/golangci-lint" \
    "$_skrip" --cek >/dev/null 2>&1 && _sts=0 || _sts=$?
  if [ "$_sts" -eq 0 ]; then
    _luluskan "--cek hijau saat toolchain lengkap"
  else
    _gagalkan "--cek hijau saat toolchain lengkap (exit=$_sts)"
  fi

  # (11) integrasi Makefile: resep memakai jalur absolut hasil pemilih,
  #      tanpa mengandalkan PATH (dry-run, tanpa menjalankan apa pun)
  if command -v make >/dev/null 2>&1; then
    _kel=$(cd "$_repo" && PATH=/usr/bin:/bin GRAHITA_GO_KANDIDAT="$_tmp/go1271" \
      make -n test 2>/dev/null) || _kel=""
    if printf '%s' "$_kel" | grep -q "$_tmp/go1271/go test ./..."; then
      _luluskan "Makefile: resep 'test' memakai Go hasil pemilih (tanpa PATH interaktif)"
    else
      _gagalkan "Makefile: resep 'test' memakai Go hasil pemilih (dry-run: $_kel)"
    fi

    _kel=$(cd "$_repo" && PATH=/usr/bin:/bin GRAHITA_GO_KANDIDAT="$_tmp/go1271" \
      make -n lint 2>/dev/null) || _kel=""
    if printf '%s' "$_kel" | grep -q "golangci-lint run ./..."; then
      _luluskan "Makefile: target lint ada dan memanggil golangci-lint run ./..."
    else
      _gagalkan "Makefile: target lint ada dan memanggil golangci-lint run ./... (dry-run: $_kel)"
    fi

    _kel=$(cd "$_repo" && PATH=/usr/bin:/bin GRAHITA_GO_KANDIDAT="$_tmp/go1271" \
      GRAHITA_GOLANGCI_LINT="$_tmp/lint/golangci-lint" make --no-print-directory toolchain-cek 2>/dev/null) || _kel=""
    if printf '%s' "$_kel" | grep -q "$_tmp/go1271/go"; then
      _luluskan "Makefile: toolchain-cek melaporkan jalur toolchain resep"
    else
      _gagalkan "Makefile: toolchain-cek melaporkan jalur toolchain resep (keluaran: $_kel)"
    fi
  else
    _gagalkan "integrasi Makefile tidak diuji: make tidak tersedia"
  fi

  printf '\nRingkasan kendali diri scripts/go-env.sh: %s lulus, %s gagal\n' "$_lulus" "$_gagal"
  [ "$_gagal" -eq 0 ]
}

_cari_go_kandidat() { # $1 = satu kandidat, pakai jalur pemilih yang sama
  GRAHITA_GO_KANDIDAT="$1" "$_skrip" --go 2>/dev/null
}

case "${1:---laporan}" in
  --laporan|--lapor) _laporan ;;
  --go)
    _cari_go || exit 1
    ;;
  --golangci-lint)
    if _gl=$(_cari_golangci); then printf '%s\n' "$_gl"; else printf '%s\n' golangci-lint; fi
    ;;
  --export) _ekspor ;;
  --cek) _cek || exit 1 ;;
  --uji-diri) _uji_diri || exit 1 ;;
  -h|--help) _pemakaian ;;
  *)
    _info "mode tidak dikenal: $1 (lihat --help)"
    exit 2
    ;;
esac
