import type { NavGroup } from "@grahita/ui";

/**
 * Menu Admin Portal (SystemDesign.md §7.1), persis nav admin_v2.html.
 *
 * Tiga item bertanda `superadminOnly` (Rekening bank, Reporting, Pengaturan)
 * disembunyikan untuk Manager oleh AdminShell (filterGroups); rutenya juga
 * menolak lewat penjaga `PenjagaSuperadmin`, dan API tetap otoritas akhir.
 *
 * Catatan domain: prefix `/admin` pada dokumen hanya berlaku untuk endpoint API
 * (/api/v1/admin). SPA admin dibuka di root host grahita-admin.tiusclaw.com.
 */
export const NAV_ADMIN: NavGroup[] = [
  {
    label: "Menu",
    items: [
      { to: "/", label: "Dashboard", icon: "squares-four" },
      { to: "/pesanan", label: "Riwayat pemesanan", icon: "receipt" },
    ],
  },
  {
    label: "Data master",
    items: [
      { to: "/cabang", label: "Cabang", icon: "storefront" },
      { to: "/katalog", label: "Katalog product", icon: "tag" },
      { to: "/rekening", label: "Rekening bank", icon: "bank", superadminOnly: true },
    ],
  },
  {
    label: "Lainnya",
    items: [
      { to: "/reporting", label: "Reporting", icon: "chart-line", superadminOnly: true },
      { to: "/pengguna", label: "User management", icon: "users-three" },
      { to: "/pengaturan", label: "Pengaturan", icon: "gear-six", superadminOnly: true },
    ],
  },
];
