/**
 * Komponen tarif sel matriks harga: HJM, P1, dan SPV
 * (docs/design/master-impact-import.md §7).
 *
 * API menyimpan P1 sebagai rasio desimal 0–1 ("0.1"), sedangkan admin
 * mengetik persen ("10" atau "2,5"). Konversi dilakukan dengan menggeser titik
 * desimal, bukan aritmetika float, supaya rasio yang dikirim persis sama
 * dengan yang diketik. Pratinjau tarif di sini hanya bantuan tampilan: tarif
 * yang disimpan selalu dihitung ulang server dengan rumus yang sama.
 */

/** Maksimal empat angka di belakang koma persen (enam digit rasio), sama dengan server. */
const POLA_PERSEN = /^(\d{1,3})(?:[.,](\d{1,4}))?$/;
const POLA_RASIO = /^(\d+)(?:\.(\d+))?$/;

/** persenKeRasio mengubah "10" → "0.1" dan "2,5" → "0.025"; null bila tidak valid atau di atas 100%. */
export function persenKeRasio(persen: string): string | null {
  const teks = persen.trim();
  if (teks === "") return "0";
  const cocok = POLA_PERSEN.exec(teks);
  if (!cocok) return null;
  const pecahan = cocok[2] ?? "";
  const nilai = BigInt(cocok[1] + pecahan);
  if (nilai > 100n * 10n ** BigInt(pecahan.length)) return null;
  return desimal(nilai, pecahan.length + 2);
}

/** rasioKePersen mengubah rasio API "0.025" menjadi teks persen "2,5". */
export function rasioKePersen(rasio: string): string {
  const cocok = POLA_RASIO.exec(rasio.trim());
  if (!cocok) return rasio;
  const pecahan = cocok[2] ?? "";
  const nilai = BigInt(cocok[1] + pecahan);
  return desimal(nilai, Math.max(pecahan.length - 2, 0), pecahan.length < 2 ? 10n ** BigInt(2 - pecahan.length) : 1n).replace(
    ".",
    ",",
  );
}

/**
 * hitungTarif menghitung round_half_up(HJM + HJM × P1 + SPV) ke Rupiah, sama
 * seperti server (pembulatan sekali setelah ketiga komponen dijumlahkan).
 */
export function hitungTarif(hjm: number, rasio: string, spv: number): number | null {
  const cocok = POLA_RASIO.exec(rasio);
  if (!cocok || !Number.isSafeInteger(hjm) || !Number.isSafeInteger(spv) || hjm < 0 || spv < 0) return null;
  const pecahan = cocok[2] ?? "";
  const penyebut = 10n ** BigInt(pecahan.length);
  const h = BigInt(hjm);
  const pembilang = (h + BigInt(spv)) * penyebut + h * BigInt(cocok[1] + pecahan);
  const hasil = (2n * pembilang + penyebut) / (2n * penyebut);
  return hasil > BigInt(Number.MAX_SAFE_INTEGER) ? null : Number(hasil);
}

/** desimal menulis (nilai × kali) / 10^skala tanpa nol di belakang koma. */
function desimal(nilai: bigint, skala: number, kali = 1n): string {
  const teks = (nilai * kali).toString().padStart(skala + 1, "0");
  const bulat = teks.slice(0, teks.length - skala);
  const pecahan = teks.slice(teks.length - skala).replace(/0+$/, "");
  return pecahan ? `${bulat}.${pecahan}` : bulat;
}
