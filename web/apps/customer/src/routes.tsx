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

import { CustomerShell, SkeletonList, type ShellLinkProps } from "@grahita/ui";

import { SessionProvider, useSesi } from "./session";
import { NAV_CUSTOMER } from "./nav";
import { HalamanDaftar, HalamanLupa, HalamanMasuk } from "./pages/auth";
import {
  BerandaPage,
  InvoicePage,
  KatalogPage,
  ProfilPage,
  TutorialPage,
} from "./pages/shell-pages";
import {
  PesananDetailPage,
  PesananEditorPage,
  PesananPage,
} from "./pages/pesanan";
import { GayaPage } from "./pages/gaya";

/** RouterLink menyambungkan shell (yang lepas router) ke <Link> TanStack. */
function RouterLink({ to, className, active, children, title }: ShellLinkProps) {
  return (
    <Link to={to} className={className} title={title} aria-current={active ? "page" : undefined}>
      {children}
    </Link>
  );
}

/**
 * ShellLayout menjaga rute ber-shell: selama sesi dimuat tampilkan skeleton,
 * bila belum masuk alihkan ke /masuk (guard rute; API tetap penentu akhir).
 */
function ShellLayout() {
  const { status, pengguna } = useSesi();
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
    <CustomerShell
      nav={NAV_CUSTOMER}
      active={pathname}
      account={{ nama: pengguna.nama, email: pengguna.email }}
      Link={RouterLink}
    >
      <Outlet />
    </CustomerShell>
  );
}

const rootRoute = createRootRoute({
  component: () => (
    <SessionProvider>
      <Outlet />
    </SessionProvider>
  ),
});

/**
 * /masuk menerima `?daftar=sukses` dari akhir alur pendaftaran (F1-07b) supaya
 * halaman masuk bisa menampilkan pesan sukses tanpa menyimpan apa pun di klien.
 */
const masRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/masuk",
  validateSearch: (search: Record<string, unknown>): { daftar?: string } => ({
    daftar: search.daftar === "sukses" ? "sukses" : undefined,
  }),
  component: function HalamanMasukRoute() {
    const { daftar } = masRoute.useSearch();
    return <HalamanMasuk terdaftar={daftar === "sukses"} />;
  },
});
const daftarRoute = createRoute({ getParentRoute: () => rootRoute, path: "/daftar", component: HalamanDaftar });
/**
 * /lupa melayani dua keadaan: minta tautan (tanpa query) dan membuat kata sandi
 * baru dari tautan email (`?token=…`). Token dibaca dari search param router
 * supaya rute ini tetap dapat diuji tanpa menyentuh window.location.
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
const berandaRoute = createRoute({ getParentRoute: () => shellRoute, path: "/", component: BerandaPage });
const katalogRoute = createRoute({ getParentRoute: () => shellRoute, path: "/katalog", component: KatalogPage });
/**
 * Rute pesanan (F1-11): daftar, editor baru/ubah, dan detail.
 *
 * Filter status disimpan di URL search param `/pesanan?status=draft` supaya
 * dapat di-bookmark (SystemDesign.md §7.2). Detail memakai `/pesanan/{id}`
 * persis peta layar SystemDesign.md §7.1; draft hanya bisa diubah lewat
 * `/pesanan/{id}/ubah` dan API menolak bila statusnya bukan draft lagi.
 */
const pesananRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/pesanan",
  validateSearch: (search: Record<string, unknown>): { status?: string } => ({
    status: typeof search.status === "string" ? search.status : undefined,
  }),
  component: function PesananRoute() {
    const { status } = pesananRoute.useSearch();
    return <PesananPage statusAwal={status} />;
  },
});
const pesananBaruRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/pesanan/baru",
  component: PesananEditorPage,
});
const pesananUbahRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/pesanan/$id/ubah",
  component: function PesananUbahRoute() {
    const { id } = pesananUbahRoute.useParams();
    return <PesananEditorPage id={id} />;
  },
});
const pesananDetailRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/pesanan/$id",
  component: function PesananDetailRoute() {
    const { id } = pesananDetailRoute.useParams();
    return <PesananDetailPage id={id} />;
  },
});
const invoiceRoute = createRoute({ getParentRoute: () => shellRoute, path: "/invoice", component: InvoicePage });
const tutorialRoute = createRoute({ getParentRoute: () => shellRoute, path: "/tutorial", component: TutorialPage });
const profilRoute = createRoute({ getParentRoute: () => shellRoute, path: "/profil", component: ProfilPage });

const routeTree = rootRoute.addChildren([
  masRoute,
  daftarRoute,
  lupaRoute,
  gayaRoute,
  shellRoute.addChildren([
    berandaRoute,
    katalogRoute,
    pesananRoute,
    pesananBaruRoute,
    pesananUbahRoute,
    pesananDetailRoute,
    invoiceRoute,
    tutorialRoute,
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
