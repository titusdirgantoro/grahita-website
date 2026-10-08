import type { AnchorHTMLAttributes, HTMLAttributes } from "react";

import { cn } from "../cn";
import styles from "./Avatar.module.css";

/**
 * initials mengambil huruf awal tiap kata setelah awalan "Grahita " dibuang,
 * lalu dipotong dua huruf — persis rumus prototype
 * (`s.replace(/^Grahita\s+/i,'').split(/\s+/).map(w=>w[0]).join('').slice(0,2)`).
 * Contoh: "Grahita Bandung" → "B", "Dra. Sri Wahyuni" → "DS".
 */
export function initials(nama: string): string {
  const bersih = nama.replace(/^Grahita\s+/i, "").trim();
  if (!bersih) return "?";
  return bersih
    .split(/\s+/)
    .map((w) => w[0] ?? "")
    .join("")
    .slice(0, 2)
    .toUpperCase();
}

export interface AvatarProps extends HTMLAttributes<HTMLSpanElement> {
  nama: string;
  small?: boolean;
}

/** Avatar bulat berisi inisial (`.avatar` prototype). */
export function Avatar({ nama, small, className, ...rest }: AvatarProps) {
  return (
    <span className={cn(styles.avatar, small && styles.sm, className)} aria-hidden="true" {...rest}>
      {initials(nama)}
    </span>
  );
}

export interface AvatarLinkProps extends AnchorHTMLAttributes<HTMLAnchorElement> {
  nama: string;
  /** Label aksesibilitas, mis. "Profil". */
  label: string;
  small?: boolean;
}

/** AvatarLink adalah avatar yang bisa diklik menuju Profil. */
export function AvatarLink({ nama, label, small, className, ...rest }: AvatarLinkProps) {
  return (
    <a
      className={cn(styles.avatar, small && styles.sm, className)}
      aria-label={label}
      title={label}
      {...rest}
    >
      {initials(nama)}
    </a>
  );
}

export { styles as avatarStyles };
