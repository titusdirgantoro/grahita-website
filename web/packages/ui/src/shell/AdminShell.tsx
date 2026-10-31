import { useEffect, useRef, useState, type ReactNode } from "react";

import { cn } from "../cn";
import { Icon } from "../icon";
import { Avatar, AvatarLink } from "../components/Avatar";
import { Brand } from "../components/Brand";
import { Button } from "../components/Button";
import {
  DefaultShellLink,
  filterGroups,
  isActive,
  labelPeran,
  type NavGroup,
  type Role,
  type ShellLink,
} from "./nav";
import styles from "./AdminShell.module.css";

export interface AdminShellAccount {
  nama: string;
  role: Role;
}

export interface AdminShellProps {
  /** Menu bergrup (Menu / Data master / Lainnya) sebelum disaring peran. */
  groups: readonly NavGroup[];
  /** Rute aktif saat ini. */
  active: string;
  account: AdminShellAccount;
  /** Komponen tautan; aplikasi meneruskan <Link> TanStack Router. */
  Link?: ShellLink;
  onLogout?: () => void;
  children: ReactNode;
  /** Isi topbar: filter/aksi kiri, filter periode tengah. */
  topLeft?: ReactNode;
  topCenter?: ReactNode;
  topRight?: ReactNode;
  profileTo?: string;
}

/**
 * useMediaQuery menjaga state aksesibilitas drawer sejalan dengan breakpoint
 * CSS. Tanpa ini, sidebar yang sudah digeser ke luar layar tetap menerima Tab.
 */
function useMediaQuery(query: string) {
  const baca = () =>
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia(query).matches
      : false;
  const [cocok, setCocok] = useState(baca);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const media = window.matchMedia(query);
    const ubah = (event: MediaQueryListEvent) => setCocok(event.matches);
    setCocok(media.matches);
    media.addEventListener("change", ubah);
    return () => media.removeEventListener("change", ubah);
  }, [query]);

  return cocok;
}

/**
 * AdminShell memetakan tata letak admin_v2.html:
 *  - sidebar kartu 232px yang bisa diciutkan jadi 72px (`.collapsed`)
 *  - di <900px sidebar menjadi drawer (`translateX`) dengan scrim + tombol menu
 * Menu disaring per peran lewat filterGroups (guard UI; API tetap menolak).
 */
export function AdminShell({
  groups,
  active,
  account,
  Link = DefaultShellLink,
  onLogout,
  children,
  topLeft,
  topCenter,
  topRight,
  profileTo = "/profil",
}: AdminShellProps) {
  const [ciut, setCiut] = useState(false);
  const [drawer, setDrawer] = useState(false);
  const layarSempit = useMediaQuery("(max-width: 899px)");
  const drawerTertutup = layarSempit && !drawer;
  const sebelumnyaTerbuka = useRef(false);
  const pembungkusMenu = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (layarSempit && sebelumnyaTerbuka.current && !drawer) {
      pembungkusMenu.current?.querySelector<HTMLButtonElement>("button")?.focus();
    }
    sebelumnyaTerbuka.current = drawer;
  }, [drawer, layarSempit]);

  useEffect(() => {
    if (!drawer) return;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setDrawer(false);
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [drawer]);

  useEffect(() => {
    setDrawer(false);
  }, [active]);

  const grupTampil = filterGroups(groups, account.role);

  return (
    <div className={cn(styles.shell, ciut && styles.collapsed, drawer && styles.drawer)}>
      <div className={styles.scrim} onClick={() => setDrawer(false)} aria-hidden="true" />
      <aside
        className={styles.side}
        id="admin-sidebar"
        aria-label="Navigasi"
        aria-hidden={drawerTertutup || undefined}
        inert={drawerTertutup || undefined}
      >
        <div className={styles.top}>
          <Brand labelClassName={styles.wordmark} />
          <Button
            iconOnly
            icon="arrow-line-left"
            className={styles.collapseBtn}
            aria-label="Ciutkan sidebar"
            aria-expanded={!ciut}
            onClick={() => setCiut((v) => !v)}
          />
        </div>

        {grupTampil.map((grup) => (
          <div key={grup.label}>
            <p className={styles.group}>{grup.label}</p>
            <nav className={styles.nav}>
              {grup.items.map((item) => (
                <Link
                  key={item.to}
                  to={item.to}
                  title={item.label}
                  active={isActive(item.to, active)}
                >
                  <span className={styles.ic}>
                    <Icon name={item.icon} />
                  </span>
                  <span className={styles.lbl}>{item.label}</span>
                </Link>
              ))}
            </nav>
          </div>
        ))}

        <div className={styles.foot}>
          <div className={styles.me}>
            <Link
              to={profileTo}
              className={styles.accountLink}
              title={`Profil ${account.nama}`}
              aria-label={`Buka profil ${account.nama}`}
              active={isActive(profileTo, active)}
            >
              <Avatar nama={account.nama} />
              <span className={styles.who}>
                <b>{account.nama}</b>
                <span>{labelPeran(account.role)}</span>
              </span>
            </Link>
            {onLogout ? (
              <Button iconOnly icon="sign-out" onClick={onLogout} aria-label="Keluar" title="Keluar" />
            ) : null}
          </div>
        </div>
      </aside>

      <main className={styles.main}>
        <div className={styles.topbar}>
          <div className={styles.l} ref={pembungkusMenu}>
            <Button
              iconOnly
              icon="list"
              className={styles.menuBtn}
              aria-label="Buka menu"
              aria-expanded={drawer}
              aria-controls="admin-sidebar"
              onClick={() => setDrawer((v) => !v)}
            />
            {topLeft}
          </div>
          <div className={styles.c}>{topCenter}</div>
          <div className={styles.r}>
            {topRight}
            <AvatarLink nama={account.nama} href={profileTo} label="Profil" />
          </div>
        </div>
        {children}
      </main>
    </div>
  );
}

export { styles as adminShellStyles };
