import type { ReactNode } from "react";

/**
 * Peran pengguna yang dikenali UI. Shell hanya bertugas menyembunyikan menu
 * yang tidak berhak; otorisasi sebenarnya selalu ditegakkan API
 * (SystemDesign.md §7.2: "menu Superadmin disembunyikan DAN API-nya menolak").
 */
export type Role = "superadmin" | "manager" | "branch";

export interface NavItem {
  /** Rute (host portal masing-masing; lihat SystemDesign.md §7.1). */
  to: string;
  /** Label lengkap untuk sidebar. */
  label: string;
  /** Nama ikon Phosphor, mis. "squares-four". */
  icon: string;
  /** Label pendek untuk bottom-nav mobile; tanpa ini item tidak di bottom-nav. */
  bottomLabel?: string;
  /** false = jangan tampilkan di sidebar (mis. Profil yang ada di kartu akun). */
  sidebar?: boolean;
  /** Hanya Superadmin yang melihat item ini (Rekening, Reporting, Pengaturan). */
  superadminOnly?: boolean;
}

export interface NavGroup {
  label: string;
  items: NavItem[];
}

/** bolehLihat mengembalikan true bila `role` berhak melihat item menu. */
export function bolehLihat(item: NavItem, role: Role): boolean {
  if (!item.superadminOnly) return true;
  return role === "superadmin";
}

/** filterNav menyaring daftar item sesuai peran (guard menu). */
export function filterNav(items: readonly NavItem[], role: Role): NavItem[] {
  return items.filter((item) => bolehLihat(item, role));
}

/** filterGroups menyaring setiap grup; grup kosong dibuang (seperti prototype). */
export function filterGroups(groups: readonly NavGroup[], role: Role): NavGroup[] {
  return groups
    .map((g) => ({ label: g.label, items: filterNav(g.items, role) }))
    .filter((g) => g.items.length > 0);
}

/** isActive mencocokkan rute aktif; rute beranda ("/") harus cocok persis. */
export function isActive(to: string, active: string): boolean {
  if (to === "/") return active === "/";
  return active === to || active.startsWith(`${to}/`);
}

export interface ShellLinkProps {
  to: string;
  className?: string;
  active: boolean;
  children: ReactNode;
  title?: string;
}

/**
 * ShellLink adalah komponen tautan yang dipasok aplikasi (memakai <Link>
 * TanStack Router) supaya shell tetap lepas dari router. Shell merender
 * <ShellLink key=... /> sehingga key tetap dikendalikan shell.
 */
export type ShellLink = (props: ShellLinkProps) => ReactNode;

/** Tautan default (tanpa router) — juga dipakai unit test komponen. */
export function DefaultShellLink({ to, className, active, children, title }: ShellLinkProps) {
  return (
    <a
      href={to}
      className={className}
      title={title}
      aria-current={active ? "page" : undefined}
    >
      {children}
    </a>
  );
}

/** labelPeran menerjemahkan peran admin ke teks tampilan. */
export function labelPeran(role: Role): string {
  if (role === "superadmin") return "Superadmin";
  if (role === "manager") return "Manager";
  return "Cabang";
}
