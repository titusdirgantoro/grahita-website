import type { NavItem } from "@grahita/ui";

/**
 * Menu Customer Portal (SystemDesign.md §7.1), berbasis nav customer_v2.html:
 * sidebar "Beranda · Katalog · Pesanan · Riwayat invoice · Deposit · Tutorial",
 * kartu akun di bawah menuju Profil, dan bottom-nav mobile
 * (Beranda · Katalog · Pesanan · Invoice · Deposit · Profil). Deposit ikut di
 * bottom-nav karena di layar sempit tidak ada jalur lain menuju halamannya;
 * Tutorial tetap terjangkau dari kartu "Cara membuat pesanan" di Beranda.
 *
 * Deposit hanya milik cabang Cash & Carry (pesanannya dibayar dari saldo), jadi
 * menu itu disaring lewat navCabang.
 */
export const NAV_CUSTOMER: NavItem[] = [
  { to: "/", label: "Beranda", icon: "squares-four", bottomLabel: "Beranda" },
  { to: "/katalog", label: "Katalog", icon: "tag", bottomLabel: "Katalog" },
  { to: "/pesanan", label: "Pesanan", icon: "receipt", bottomLabel: "Pesanan" },
  { to: "/invoice", label: "Riwayat invoice", icon: "file-text", bottomLabel: "Invoice" },
  { to: "/deposit", label: "Deposit", icon: "wallet", bottomLabel: "Deposit" },
  { to: "/tagihan", label: "Tagihan", icon: "file-text", bottomLabel: "Tagihan" },
  { to: "/tutorial", label: "Tutorial", icon: "question" },
  { to: "/profil", label: "Profil", icon: "user-circle", bottomLabel: "Profil", sidebar: false },
];

/** navCabang adalah menu cabang sesi: tanpa Deposit untuk cabang non-C&C. */
export function navCabang(isCNC: boolean, isTempo = false): NavItem[] {
  return NAV_CUSTOMER.filter((item) => (item.to !== "/deposit" || isCNC) && (item.to !== "/tagihan" || isTempo))
    .map((item) => isTempo && item.to === "/invoice" ? { ...item, bottomLabel: undefined } : item);
}
