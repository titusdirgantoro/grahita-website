import type { NavItem } from "@grahita/ui";

/**
 * Menu Customer Portal (SystemDesign.md §7.1), persis nav customer_v2.html:
 * sidebar "Beranda · Katalog · Pesanan · Riwayat invoice · Tutorial",
 * kartu akun di bawah menuju Profil, dan bottom-nav mobile 5 kolom
 * (Beranda · Katalog · Pesanan · Invoice · Profil).
 */
export const NAV_CUSTOMER: NavItem[] = [
  { to: "/", label: "Beranda", icon: "squares-four", bottomLabel: "Beranda" },
  { to: "/katalog", label: "Katalog", icon: "tag", bottomLabel: "Katalog" },
  { to: "/pesanan", label: "Pesanan", icon: "receipt", bottomLabel: "Pesanan" },
  { to: "/invoice", label: "Riwayat invoice", icon: "file-text", bottomLabel: "Invoice" },
  { to: "/tutorial", label: "Tutorial", icon: "question" },
  { to: "/profil", label: "Profil", icon: "user-circle", bottomLabel: "Profil", sidebar: false },
];
