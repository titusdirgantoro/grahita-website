/**
 * Format angka dashboard admin, sama dengan helper admin_v2.html (rpShort,
 * axisRp, dec1, niceMax) supaya angka ringkas di kartu dan sumbu grafik
 * terbaca seperti prototype ("Rp 43,7 jt", "12,5 jt").
 */

const SATU = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 1 });
const DESIMAL1 = new Intl.NumberFormat("id-ID", { minimumFractionDigits: 1, maximumFractionDigits: 1 });
const BULAT = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 0 });
const RUPIAH = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });

/** rpRingkas: 43_700_000 → "Rp 43,7 jt"; di bawah sejuta ditulis penuh. */
export function rpRingkas(n: number): string {
  const a = Math.abs(n);
  if (a >= 1e9) return `Rp ${SATU.format(n / 1e9)} M`;
  if (a >= 1e6) return `Rp ${SATU.format(n / 1e6)} jt`;
  return rupiah(n);
}

/** rupiah menulis Rupiah penuh tanpa desimal. */
export function rupiah(n: number): string {
  return RUPIAH.format(n);
}

/** bilangan menulis bilangan bulat dengan pemisah ribuan Indonesia. */
export function bilangan(n: number): string {
  return BULAT.format(n);
}

/** desimal1 menulis satu angka di belakang koma ("12,7"). */
export function desimal1(n: number): string {
  return DESIMAL1.format(Number.isFinite(n) ? n : 0);
}

/** sumbuRp adalah label sumbu grafik rupiah ("50 jt", "750 rb"). */
export function sumbuRp(v: number): string {
  if (v >= 1e9) return `${SATU.format(v / 1e9)} M`;
  if (v >= 1e6) return `${SATU.format(v / 1e6)} jt`;
  if (v >= 1e3) return `${Math.round(v / 1e3)} rb`;
  return String(Math.round(v));
}

/** batasAtas membulatkan nilai maksimum ke 1/2/2,5/5/10 × 10ⁿ untuk sumbu grafik. */
export function batasAtas(v: number): number {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  const n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * p;
}

/** persenPerubahan: (sekarang − sebelum) / sebelum dalam persen; null bila pembandingnya nol. */
export function persenPerubahan(sekarang: number, sebelum: number): number | null {
  if (!sebelum) return null;
  return ((sekarang - sebelum) / sebelum) * 100;
}
