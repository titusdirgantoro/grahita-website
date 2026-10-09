package vatest

import "testing"

// TestMockMemenuhiKontrak menjalankan SELURUH kontrak VA terhadap tiruan
// provider tanpa akses bank. Bila kelak `va.MockProvider` diubah, uji ini
// gagal lebih dahulu daripada adapter sungguhan (F3-30) yang memakainya.
func TestMockMemenuhiKontrak(t *testing.T) {
	Uji(t, PabrikMock)
}
