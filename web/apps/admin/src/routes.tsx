import { useEffect } from "react";
import {
  Link,
  Outlet,
  createRootRoute,
  createRoute,
  createRouter,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";

import { AdminShell, Button, SkeletonList, type ShellLinkProps } from "@grahita/ui";

import { SessionProvider, useSesi } from "./session";
import { NAV_ADMIN } from "./nav";
import { HalamanLupa, HalamanMasuk } from "./pages/auth";
import { ProfilAdminPage, ReportingPage } from "./pages/shell-pages";
import { DashboardPage, FilterPeriodeDashboard, type FilterDashboard } from "./pages/dashboard";
import { FilterCabangTopbar } from "./pages/filter-cabang";
import {
  PesananAdminPage,
  PesananDetailAdminPage,
  type FilterPesanan,
} from "./pages/pesanan";
import { CabangPage } from "./pages/cabang";
import { KatalogPage } from "./pages/katalog";
import { RekeningPage } from "./pages/rekening";
import { DepositAdminPage } from "./pages/deposit";
import { TagihanAdminPage } from "./pages/tagihan";
import { PengaturanPage } from "./pages/pengaturan";
import { PenggunaPage } from "./pages/pengguna";
import { GayaPage } from "./pages/gaya";
import { AuditPage, type FilterAudit } from "./pages/audit";

/** RouterLink menyambungkan shell (yang lepas router) ke <Link> TanStack. */
function RouterLink({ to, className, active, children, title }: ShellLinkProps) {
  return (
    <Link to={to} className={className} title={title} aria-current={active ? "page" : undefined}>
      {children}
    </Link>
  );
}

/**
 * ShellLayout menjaga rute ber-shell admin: skeleton saat sesi dimuat,
 * alihkan ke /masuk bila belum masuk, lalu pasang AdminShell dengan menu
 * yang sudah disaring peran.
 *
 * Topbar mengikuti admin_v2.html: pada dashboard & riwayat pemesanan berisi
 * pemilih cabang (kiri), dan pada dashboard juga periode (tengah) — satu-
 * satunya pemilih halaman itu, state-nya di URL search param rutenya. Tombol "+" membuka form tambah cabang dan lonceng
 * membuka pesanan yang masih menunggu penanganan.
 */
function ShellLayout() {
  const { status, pengguna, peran, keluar } = useSesi();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });

  useEffect(() => {
    if (status === "belum") void navigate({ to: "/masuk", replace: true });
  }, [status, navigate]);

  if (status === "memuat") {
    return (
      <div style={{ padding: 32 }}>
        <SkeletonList rows={4} />
      </div>
    );
  }
  if (!pengguna) return null;

  return (
    <AdminShell
      groups={NAV_ADMIN}
      active={pathname}
      account={{ nama: pengguna.nama, role: peran }}
      Link={RouterLink}
      onLogout={() => void keluar()}
      topLeft={
        pathname === "/" || pathname === "/pesanan" ? (
          <>
            <FilterCabangTopbar ke={pathname === "/" ? "/" : "/pesanan"} />
            <Button
              iconOnly
              icon="plus"
              aria-label="Tambah cabang"
              title="Tambah cabang"
              onClick={() => void navigate({ to: "/cabang", search: { tambah: true } })}
            />
          </>
        ) : undefined
      }
      topCenter={pathname === "/" ? <FilterPeriodeDashboard /> : undefined}
      topRight={
        <Button
          iconOnly
          icon="bell"
          aria-label="Perlu tindakan"
          title="Pesanan yang menunggu penanganan"
          onClick={() => void navigate({ to: "/pesanan", search: { status: "submitted" } })}
        />
      }
    >
      <Outlet />
    </AdminShell>
  );
}

const rootRoute = createRootRoute({
  component: () => (
    <SessionProvider>
      <Outlet />
    </SessionProvider>
  ),
});

const masukRoute = createRoute({ getParentRoute: () => rootRoute, path: "/masuk", component: HalamanMasuk });
/**
 * /lupa adalah jalur pemulihan akun admin: minta tautan (tanpa query) atau
 * membuat kata sandi baru dari tautan email (`?token=…`). Rutenya harus ada di
 * host admin supaya tautan PASSWORD_RESET_PATH tidak menyeberang subdomain.
 */
const lupaRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/lupa",
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token: typeof search.token === "string" ? search.token : undefined,
  }),
  component: function HalamanLupaRoute() {
    const { token } = lupaRoute.useSearch();
    return <HalamanLupa token={token} />;
  },
});
const gayaRoute = createRoute({ getParentRoute: () => rootRoute, path: "/gaya", component: GayaPage });

const shellRoute = createRoute({ getParentRoute: () => rootRoute, id: "shell", component: ShellLayout });
/**
 * / (dashboard admin) menyimpan filter cabang & periode di URL search param
 * (SystemDesign.md §7.2) supaya ringkasan bisa di-bookmark/di-share. Nilai
 * yang tidak dikenal diabaikan, bukan membuat halaman gagal.
 */
const dashboardRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/",
  validateSearch: (search: Record<string, unknown>): FilterDashboard => ({
    cabang: teksMungkin(search.cabang),
    periode: teksMungkin(search.periode),
  }),
  component: DashboardPage,
});

/** teksMungkin menormalkan search param teks (kosong -> undefined). */
function teksMungkin(v: unknown): string | undefined {
  return typeof v === "string" && v.trim() !== "" ? v : undefined;
}

/** boolMungkin menormalkan search param boolean ("true"/"false"). */
function boolMungkin(v: unknown): boolean | undefined {
  if (v === true || v === "true") return true;
  if (v === false || v === "false") return false;
  return undefined;
}

/** angkaMungkin menormalkan nomor halaman (selalu >= 1). */
function angkaMungkin(v: unknown): number | undefined {
  const n = typeof v === "number" ? v : typeof v === "string" ? Number(v) : Number.NaN;
  return Number.isFinite(n) && n >= 1 ? Math.floor(n) : undefined;
}

/**
 * /pesanan menyimpan seluruh filter riwayat di URL search param
 * (SystemDesign.md §7.2) sehingga daftar terfilter bisa di-bookmark/di-share.
 * Nilai yang tidak dikenal diabaikan, bukan membuat halaman gagal.
 */
const pesananRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/pesanan",
  validateSearch: (search: Record<string, unknown>): FilterPesanan => ({
    cari: teksMungkin(search.cari),
    cabang: teksMungkin(search.cabang),
    status: teksMungkin(search.status),
    verif: teksMungkin(search.verif),
    cnc: boolMungkin(search.cnc),
    email_gagal: boolMungkin(search.email_gagal),
    dari: teksMungkin(search.dari),
    sampai: teksMungkin(search.sampai),
    grup: teksMungkin(search.grup),
    halaman: angkaMungkin(search.halaman),
  }),
  component: PesananAdminPage,
});
const pesananDetailRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/pesanan/$id",
  component: PesananDetailAdminPage,
});
/** /cabang?tambah=true langsung membuka form tambah cabang (tombol "+" topbar). */
const cabangRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/cabang",
  validateSearch: (search: Record<string, unknown>): { tambah?: boolean } => ({ tambah: boolMungkin(search.tambah) }),
  component: CabangPage,
});
const katalogRoute = createRoute({ getParentRoute: () => shellRoute, path: "/katalog", component: KatalogPage });
const depositRoute = createRoute({ getParentRoute: () => shellRoute, path: "/deposit", component: DepositAdminPage });
const tagihanRoute = createRoute({ getParentRoute: () => shellRoute, path: "/tagihan", component: TagihanAdminPage });
const rekeningRoute = createRoute({ getParentRoute: () => shellRoute, path: "/rekening", component: RekeningPage });
const reportingRoute = createRoute({ getParentRoute: () => shellRoute, path: "/reporting", component: ReportingPage });
const penggunaRoute = createRoute({ getParentRoute: () => shellRoute, path: "/pengguna", component: PenggunaPage });
const pengaturanRoute = createRoute({ getParentRoute: () => shellRoute, path: "/pengaturan", component: PengaturanPage });
/**
 * /audit (F2-28) menyimpan filter audit log di URL search param supaya hasil
 * terfilter bisa di-bookmark/di-share (SystemDesign.md §7.2). Nilai yang tidak
 * dikenal diabaikan, bukan membuat halaman gagal.
 */
const auditRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/audit",
  validateSearch: (search: Record<string, unknown>): FilterAudit => ({
    entitas: teksMungkin(search.entitas),
    aksi: teksMungkin(search.aksi),
    aktor: teksMungkin(search.aktor),
    dari: teksMungkin(search.dari),
    sampai: teksMungkin(search.sampai),
    halaman: angkaMungkin(search.halaman),
  }),
  component: AuditPage,
});
const profilRoute = createRoute({ getParentRoute: () => shellRoute, path: "/profil", component: ProfilAdminPage });

const routeTree = rootRoute.addChildren([
  masukRoute,
  lupaRoute,
  gayaRoute,
  shellRoute.addChildren([
    dashboardRoute,
    pesananRoute,
    pesananDetailRoute,
    depositRoute,
    tagihanRoute,
    cabangRoute,
    katalogRoute,
    rekeningRoute,
    reportingRoute,
    penggunaRoute,
    pengaturanRoute,
    auditRoute,
    profilRoute,
  ]),
]);

export { routeTree };

export const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
