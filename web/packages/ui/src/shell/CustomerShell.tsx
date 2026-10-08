import type { ReactNode } from "react";

import { cn } from "../cn";
import { Icon } from "../icon";
import { Avatar, AvatarLink } from "../components/Avatar";
import { Brand } from "../components/Brand";
import { DefaultShellLink, isActive, type NavItem, type ShellLink } from "./nav";
import styles from "./CustomerShell.module.css";

export interface CustomerShellAccount {
  nama: string;
  email: string;
}

export interface CustomerShellProps {
  /** Item menu customer (Beranda…Tutorial, Profil). */
  nav: readonly NavItem[];
  /** Rute aktif saat ini, mis. hasil useRouterState().location.pathname. */
  active: string;
  account: CustomerShellAccount;
  /** Komponen tautan; aplikasi meneruskan <Link> TanStack Router. */
  Link?: ShellLink;
  children: ReactNode;
  /** Rute profil (dipakai kartu akun & avatar mobile). */
  profileTo?: string;
}

/**
 * CustomerShell memetakan tata letak customer_v2.html:
 *  - desktop : sidebar 240px (brand + menu + kartu akun), panel ber-brand-50
 *  - mobile  : topbar sticky + bottom-nav 5 kolom; sidebar disembunyikan CSS
 * Keduanya selalu dirender; media query di CSS yang memilih tampilannya.
 */
export function CustomerShell({
  nav,
  active,
  account,
  Link = DefaultShellLink,
  children,
  profileTo = "/profil",
}: CustomerShellProps) {
  const sideItems = nav.filter((n) => n.sidebar !== false);
  const bottomItems = nav.filter((n) => n.bottomLabel);

  return (
    <div className={styles.shell}>
      <aside className={styles.side} aria-label="Navigasi">
        <div className={styles.sideBrand}>
          <Brand />
        </div>
        <p className={styles.navlabel}>Menu</p>
        <nav className={styles.nav}>
          {sideItems.map((item) => (
            <Link
              key={item.to}
              to={item.to}
              title={item.label}
              active={isActive(item.to, active)}
            >
              <span className={styles.ic}>
                <Icon name={item.icon} />
              </span>
              {item.label}
            </Link>
          ))}
        </nav>
        <a
          className={styles.account}
          href={profileTo}
          aria-current={isActive(profileTo, active) ? "page" : undefined}
        >
          <Avatar nama={account.nama} />
          <span className={styles.who}>
            <b>{account.nama}</b>
            <span>{account.email}</span>
          </span>
        </a>
      </aside>

      <main className={styles.panel}>
        <header className={styles.mtop}>
          <Brand label="" />
          <span className={styles.ttl}>{account.nama}</span>
          <AvatarLink small nama={account.nama} href={profileTo} label="Profil" />
        </header>
        <div className={styles.content}>{children}</div>
      </main>

      <nav className={cn(styles.bottom)} aria-label="Navigasi bawah">
        {bottomItems.map((item) => (
          <Link key={item.to} to={item.to} title={item.label} active={isActive(item.to, active)}>
            <Icon name={item.icon} />
            <span>{item.bottomLabel}</span>
          </Link>
        ))}
      </nav>
    </div>
  );
}

export { styles as customerShellStyles };
