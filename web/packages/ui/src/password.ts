/**
 * Aturan kata sandi yang dipakai bersama kedua portal di sisi klien.
 *
 * Ini HANYA salinan untuk umpan balik cepat: aturan yang mengikat tetap di
 * backend (`auth.CheckPasswordStrength` — PRD CP-REG-05, Architecture.md §6):
 * minimal 8 karakter, memuat huruf DAN angka, tanpa spasi di tepi. Pesan yang
 * dikembalikan backend (400 VALIDATION_FAILED) selalu menimpa pesan lokal,
 * sehingga klien tidak pernah menjadi satu-satunya penentu.
 */

/** MIN_PANJANG adalah panjang minimum kata sandi baru (samakan dengan backend). */
export const MIN_PANJANG = 8;

/** GalatKataSandi memetakan field form ke pesan galat. */
export type GalatKataSandi = Record<string, string>;

export interface PeriksaKataSandiInput {
  /** Kata sandi lama (wajib untuk alur "ganti dari Profil"). */
  lama?: string;
  baru: string;
  /** Konfirmasi; bila undefined, konfirmasi tidak diperiksa. */
  konfirmasi?: string;
  /** Nama field galat untuk kata sandi baru (default "password_baru"). */
  fieldBaru?: string;
  /** Nama field galat untuk konfirmasi (default "password_konfirmasi"). */
  fieldKonfirmasi?: string;
}

/**
 * periksaKataSandiBaru mengembalikan peta galat per field; objek kosong berarti
 * input lolos pemeriksaan klien.
 */
export function periksaKataSandiBaru({
  lama,
  baru,
  konfirmasi,
  fieldBaru = "password_baru",
  fieldKonfirmasi = "password_konfirmasi",
}: PeriksaKataSandiInput): GalatKataSandi {
  const galat: GalatKataSandi = {};
  if (baru.length < MIN_PANJANG) {
    galat[fieldBaru] = `Minimal ${MIN_PANJANG} karakter.`;
  } else if (!/[A-Za-z]/.test(baru) || !/[0-9]/.test(baru)) {
    galat[fieldBaru] = "Harus memuat huruf dan angka.";
  } else if (baru.trim() !== baru) {
    galat[fieldBaru] = "Tidak boleh diawali atau diakhiri spasi.";
  } else if (lama !== undefined && baru === lama) {
    galat[fieldBaru] = "Kata sandi baru harus berbeda dari kata sandi lama.";
  }
  if (konfirmasi !== undefined && konfirmasi !== baru) {
    galat[fieldKonfirmasi] = "Konfirmasi tidak sama dengan kata sandi baru.";
  }
  return galat;
}

/**
 * pesanGalat mengubah error apa pun menjadi pesan yang bisa ditampilkan.
 * Error API (ApiClientError) membawa `fields` per field dan `message` umum;
 * keduanya dibaca secara struktural supaya paket UI tidak perlu bergantung
 * pada paket api-client.
 */
export interface GalatApiMirip {
  message?: string;
  fields?: Record<string, string>;
}

export function pesanGalatSingkat(err: unknown, bawaan = "Terjadi kesalahan tak terduga."): string {
  if (err && typeof err === "object" && typeof (err as GalatApiMirip).message === "string") {
    return (err as GalatApiMirip).message as string;
  }
  return bawaan;
}

/** fieldGalat mengembalikan pesan backend untuk satu field, bila ada. */
export function fieldGalat(err: unknown, nama: string): string | undefined {
  if (!err || typeof err !== "object") return undefined;
  const fields = (err as GalatApiMirip).fields;
  if (!fields) return undefined;
  return fields[nama];
}
