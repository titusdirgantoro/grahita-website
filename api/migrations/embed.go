// Package migrations menyimpan file SQL migrasi yang di-embed ke binary,
// sehingga container tidak perlu membawa file terpisah.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
