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
  PesananPage,
  ProfilPage,
  TutorialPage,
} from "./pages/shell-pages";
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

const masRoute = createRoute({ getParentRoute: () => rootRoute, path: "/masuk", component: HalamanMasuk });
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
const pesananRoute = createRoute({ getParentRoute: () => shellRoute, path: "/pesanan", component: PesananPage });
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
