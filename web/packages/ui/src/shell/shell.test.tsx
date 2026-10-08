import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";

import { AdminShell } from "./AdminShell";
import { CustomerShell } from "./CustomerShell";
import { bolehLihat, filterGroups, filterNav, isActive, type NavGroup, type NavItem } from "./nav";
import adminStyles from "./AdminShell.module.css";

const NAV_CUSTOMER: NavItem[] = [
  { to: "/", label: "Beranda", icon: "squares-four", bottomLabel: "Beranda" },
  { to: "/katalog", label: "Katalog", icon: "tag", bottomLabel: "Katalog" },
  { to: "/pesanan", label: "Pesanan", icon: "receipt", bottomLabel: "Pesanan" },
  { to: "/invoice", label: "Riwayat invoice", icon: "file-text", bottomLabel: "Invoice" },
  { to: "/tutorial", label: "Tutorial", icon: "question" },
  { to: "/profil", label: "Profil", icon: "user-circle", bottomLabel: "Profil", sidebar: false },
];

const NAV_ADMIN: NavGroup[] = [
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

describe("model menu & guard peran", () => {
  it("bolehLihat hanya menolak item superadminOnly untuk Manager", () => {
    const item: NavItem = { to: "/rekening", label: "Rekening", icon: "bank", superadminOnly: true };
    expect(bolehLihat(item, "superadmin")).toBe(true);
    expect(bolehLihat(item, "manager")).toBe(false);
    expect(bolehLihat({ to: "/cabang", label: "Cabang", icon: "storefront" }, "manager")).toBe(true);
  });

  it("filterNav membuang item yang tidak berhak", () => {
    const hasil = filterNav(NAV_ADMIN[1].items, "manager");
    expect(hasil.map((i) => i.to)).toEqual(["/cabang", "/katalog"]);
  });

  it("filterGroups membuang grup yang menjadi kosong", () => {
    const grup: NavGroup[] = [
      { label: "Kosong", items: [{ to: "/x", label: "X", icon: "x", superadminOnly: true }] },
      { label: "Umum", items: [{ to: "/y", label: "Y", icon: "y" }] },
    ];
    const hasil = filterGroups(grup, "manager");
    expect(hasil.map((g) => g.label)).toEqual(["Umum"]);
  });

  it("isActive menangani rute beranda dan anak rute", () => {
    expect(isActive("/", "/")).toBe(true);
    expect(isActive("/", "/katalog")).toBe(false);
    expect(isActive("/pesanan", "/pesanan/baru")).toBe(true);
    expect(isActive("/pesanan", "/pesanans")).toBe(false);
  });
});

describe("CustomerShell", () => {
  const akun = { nama: "Grahita Bandung", email: "bandung@grahitaindonesia.com" };

  it("menampilkan sidebar tanpa Profil dan kartu akun", () => {
    render(
      <CustomerShell nav={NAV_CUSTOMER} active="/katalog" account={akun}>
        <p>isi halaman</p>
      </CustomerShell>,
    );
    const sidebar = screen.getByRole("complementary");
    const nav = within(sidebar).getByRole("navigation");
    expect(within(nav).getByRole("link", { name: /Katalog/ })).toBeDefined();
    expect(within(nav).queryByRole("link", { name: /Profil/ })).toBeNull();
    expect(within(sidebar).getByText("Grahita Bandung")).toBeDefined();
    expect(within(sidebar).getByText("bandung@grahitaindonesia.com")).toBeDefined();
  });

  it("menandai item aktif dengan aria-current pada sidebar dan bottom-nav", () => {
    render(
      <CustomerShell nav={NAV_CUSTOMER} active="/katalog" account={akun}>
        <p>isi</p>
      </CustomerShell>,
    );
    const sidebar = screen.getByRole("complementary");
    const katalogSide = within(sidebar).getByRole("link", { name: /Katalog/ });
    expect(katalogSide.getAttribute("aria-current")).toBe("page");
    const sidebarBeranda = within(sidebar).getByRole("link", { name: /Beranda/ });
    expect(sidebarBeranda.getAttribute("aria-current")).toBeNull();

    const bawah = screen.getByRole("navigation", { name: "Navigasi bawah" });
    const katalogBawah = within(bawah).getByRole("link", { name: /Katalog/ });
    expect(katalogBawah.getAttribute("aria-current")).toBe("page");
  });

  it("bottom-nav hanya memuat 5 item berlabel pendek", () => {
    render(
      <CustomerShell nav={NAV_CUSTOMER} active="/" account={akun}>
        <p>isi</p>
      </CustomerShell>,
    );
    const bawah = screen.getByRole("navigation", { name: "Navigasi bawah" });
    expect(within(bawah).getAllByRole("link")).toHaveLength(5);
    expect(within(bawah).queryByRole("link", { name: /Tutorial/ })).toBeNull();
  });

  it("merender topbar mobile berisi nama cabang dan tautan profil", () => {
    render(
      <CustomerShell nav={NAV_CUSTOMER} active="/" account={akun}>
        <p>isi</p>
      </CustomerShell>,
    );
    const profil = screen.getAllByRole("link", { name: "Profil" });
    expect(profil.length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Grahita Bandung").length).toBeGreaterThanOrEqual(2);
  });

  it("memakai komponen tautan yang dipasok aplikasi", () => {
    const Link = vi.fn(({ to, children }: { to: string; children: React.ReactNode }) => (
      <a href={to} data-uji="ya">
        {children}
      </a>
    ));
    render(
      <CustomerShell nav={NAV_CUSTOMER} active="/" account={akun} Link={Link}>
        <p>isi</p>
      </CustomerShell>,
    );
    expect(Link).toHaveBeenCalled();
    expect(screen.getAllByText("Katalog").length).toBeGreaterThanOrEqual(1);
  });
});

describe("AdminShell", () => {
  it("Superadmin melihat semua grup menu", () => {
    render(
      <AdminShell groups={NAV_ADMIN} active="/" account={{ nama: "Rina", role: "superadmin" }}>
        <p>isi</p>
      </AdminShell>,
    );
    expect(screen.getByRole("link", { name: /Rekening bank/ })).toBeDefined();
    expect(screen.getByRole("link", { name: /Reporting/ })).toBeDefined();
    expect(screen.getByRole("link", { name: /Pengaturan/ })).toBeDefined();
    expect(screen.getByText("Superadmin")).toBeDefined();
  });

  it("Manager tidak melihat item Superadmin", () => {
    render(
      <AdminShell groups={NAV_ADMIN} active="/cabang" account={{ nama: "Budi", role: "manager" }}>
        <p>isi</p>
      </AdminShell>,
    );
    expect(screen.queryByRole("link", { name: /Rekening bank/ })).toBeNull();
    expect(screen.queryByRole("link", { name: /Reporting/ })).toBeNull();
    expect(screen.queryByRole("link", { name: /Pengaturan/ })).toBeNull();
    expect(screen.getByRole("link", { name: /Cabang/ })).toBeDefined();
    expect(screen.getByText("Manager")).toBeDefined();
  });

  it("menandai menu aktif dan menampilkan Profil di kaki sidebar", () => {
    render(
      <AdminShell groups={NAV_ADMIN} active="/katalog" account={{ nama: "Rina", role: "superadmin" }}>
        <p>isi</p>
      </AdminShell>,
    );
    expect(screen.getByRole("link", { name: /Katalog product/ }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("link", { name: /Dashboard/ }).getAttribute("aria-current")).toBeNull();
    const sidebar = screen.getByRole("complementary");
    expect(within(sidebar).getByRole("link", { name: "Profil" })).toBeDefined();
  });

  it("tombol ciutkan menambahkan kelas collapsed pada shell", () => {
    const { container } = render(
      <AdminShell groups={NAV_ADMIN} active="/" account={{ nama: "Rina", role: "superadmin" }}>
        <p>isi</p>
      </AdminShell>,
    );
    const root = container.firstElementChild as HTMLElement;
    expect(root.className).not.toContain(adminStyles.collapsed);
    fireEvent.click(screen.getByRole("button", { name: "Ciutkan sidebar" }));
    expect(root.className).toContain(adminStyles.collapsed);
  });

  it("tombol menu membuka drawer dan Escape menutupnya", () => {
    const { container } = render(
      <AdminShell groups={NAV_ADMIN} active="/" account={{ nama: "Rina", role: "superadmin" }}>
        <p>isi</p>
      </AdminShell>,
    );
    const root = container.firstElementChild as HTMLElement;
    const tombol = screen.getByRole("button", { name: "Buka menu" });
    expect(tombol.getAttribute("aria-expanded")).toBe("false");
    expect(tombol.getAttribute("aria-controls")).toBe("admin-sidebar");

    fireEvent.click(tombol);
    expect(root.className).toContain(adminStyles.drawer);
    expect(tombol.getAttribute("aria-expanded")).toBe("true");

    fireEvent.keyDown(document, { key: "Escape" });
    expect(root.className).not.toContain(adminStyles.drawer);
  });

  it("memanggil onLogout dari tombol keluar", () => {
    const onLogout = vi.fn();
    render(
      <AdminShell
        groups={NAV_ADMIN}
        active="/"
        account={{ nama: "Rina", role: "superadmin" }}
        onLogout={onLogout}
      >
        <p>isi</p>
      </AdminShell>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Keluar" }));
    expect(onLogout).toHaveBeenCalledTimes(1);
  });

  it("menampilkan area topbar sesuai prop", () => {
    render(
      <AdminShell
        groups={NAV_ADMIN}
        active="/"
        account={{ nama: "Rina", role: "superadmin" }}
        topLeft={<span>filter-kiri</span>}
        topCenter={<span>periode-tengah</span>}
        topRight={<span>lonceng-kanan</span>}
      >
        <p>isi</p>
      </AdminShell>,
    );
    expect(screen.getByText("filter-kiri")).toBeDefined();
    expect(screen.getByText("periode-tengah")).toBeDefined();
    expect(screen.getByText("lonceng-kanan")).toBeDefined();
  });
});
