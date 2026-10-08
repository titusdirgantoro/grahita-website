import { useEffect, useState } from "react";
import {
  Link,
  Outlet,
  createRootRoute,
  createRoute,
  createRouter,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";

import { AdminShell, Button, Segmented, SkeletonList, type ShellLinkProps } from "@grahita/ui";

import { SessionProvider, useSesi } from "./session";
import { NAV_ADMIN } from "./nav";
import { HalamanLupa, HalamanMasuk } from "./pages/auth";
import {
  DashboardPage,
  PesananAdminPage,
  ProfilAdminPage,
  ReportingPage,
} from "./pages/shell-pages";
import { CabangPage } from "./pages/cabang";
import { KatalogPage } from "./pages/katalog";
import { RekeningPage } from "./pages/rekening";
import { PengaturanPage } from "./pages/pengaturan";
import { PenggunaPage } from "./pages/pengguna";
import { GayaPage } from "./pages/gaya";

/** RouterLink menyambungkan shell (yang lepas router) ke <Link> TanStack. */
function RouterLink({ to, className, active, children, title }: ShellLinkProps) {
  return (
    <Link to={to} className={className} title={title} aria-current={active ? "page" : undefined}>
      {children}
    </Link>
  );
}

const PERIODE = [
  { value: "hari", label: "Hari ini" },
  { value: "minggu", label: "Minggu ini" },
  { value: "bulan", label: "Bulan ini" },
  { value: "tahun", label: "Tahun ini" },
] as const;

/**
 * ShellLayout menjaga rute ber-shell admin: skeleton saat sesi dimuat,
 * alihkan ke /masuk bila belum masuk, lalu pasang AdminShell dengan menu
 * yang sudah disaring peran.
 */
function ShellLayout() {
  const { status, pengguna, peran, keluar } = useSesi();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const [periode, setPeriode] = useState<(typeof PERIODE)[number]["value"]>("bulan");

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

  const tampilPeriode = pathname === "/" || pathname === "/pesanan";

  return (
    <AdminShell
      groups={NAV_ADMIN}
      active={pathname}
      account={{ nama: pengguna.nama, role: peran }}
      Link={RouterLink}
      onLogout={() => void keluar()}
      topLeft={
        tampilPeriode ? (
          <Button iconOnly icon="plus" aria-label="Tambah cabang" title="Tambah cabang" />
        ) : undefined
      }
      topCenter={
        tampilPeriode ? (
          <Segmented label="Periode" options={PERIODE} value={periode} onChange={setPeriode} />
        ) : undefined
      }
      topRight={<Button iconOnly icon="bell" aria-label="Perlu tindakan" />}
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
const dashboardRoute = createRoute({ getParentRoute: () => shellRoute, path: "/", component: DashboardPage });
const pesananRoute = createRoute({ getParentRoute: () => shellRoute, path: "/pesanan", component: PesananAdminPage });
const cabangRoute = createRoute({ getParentRoute: () => shellRoute, path: "/cabang", component: CabangPage });
const katalogRoute = createRoute({ getParentRoute: () => shellRoute, path: "/katalog", component: KatalogPage });
const rekeningRoute = createRoute({ getParentRoute: () => shellRoute, path: "/rekening", component: RekeningPage });
const reportingRoute = createRoute({ getParentRoute: () => shellRoute, path: "/reporting", component: ReportingPage });
const penggunaRoute = createRoute({ getParentRoute: () => shellRoute, path: "/pengguna", component: PenggunaPage });
const pengaturanRoute = createRoute({ getParentRoute: () => shellRoute, path: "/pengaturan", component: PengaturanPage });
const profilRoute = createRoute({ getParentRoute: () => shellRoute, path: "/profil", component: ProfilAdminPage });

const routeTree = rootRoute.addChildren([
  masukRoute,
  lupaRoute,
  gayaRoute,
  shellRoute.addChildren([
    dashboardRoute,
    pesananRoute,
    cabangRoute,
    katalogRoute,
    rekeningRoute,
    reportingRoute,
    penggunaRoute,
    pengaturanRoute,
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
