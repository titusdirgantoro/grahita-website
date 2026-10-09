//go:build !linux

package observability

import "errors"

// statfsRasio tidak tersedia di luar Linux; pemantauan disk dilewati dan
// dilaporkan sebagai kegagalan lunak (tidak menjatuhkan proses).
func statfsRasio(string) (uint64, uint64, uint64, error) {
	return 0, 0, 0, errors.New("pemantauan disk hanya didukung di Linux")
}
