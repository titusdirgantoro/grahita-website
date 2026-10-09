//go:build linux

package observability

import "syscall"

// statfsRasio mengembalikan (total blok, blok bebas, blok tersedia, error)
// untuk satu titik mount. Dipisah per platform karena syscall.Statfs_t
// berbeda-beda; produksi berjalan di Linux (Architecture.md §5.1).
func statfsRasio(path string) (uint64, uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	return uint64(st.Blocks), uint64(st.Bfree), uint64(st.Bavail), nil
}
