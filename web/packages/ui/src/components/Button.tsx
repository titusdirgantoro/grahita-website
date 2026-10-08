import type { AnchorHTMLAttributes, ButtonHTMLAttributes, ReactNode } from "react";

import { cn } from "../cn";
import { Icon } from "../icon";
import styles from "./Button.module.css";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger-ghost";

const KELAS_VARIAN: Record<ButtonVariant, string> = {
  primary: styles.primary,
  secondary: styles.secondary,
  ghost: styles.ghost,
  "danger-ghost": styles.dangerGhost,
};

interface Umum {
  variant?: ButtonVariant;
  block?: boolean;
  icon?: string;
  children?: ReactNode;
  className?: string;
}

function isi({ icon, children }: Pick<Umum, "icon" | "children">) {
  return (
    <>
      {icon ? <Icon name={icon} /> : null}
      {children}
    </>
  );
}

export interface ButtonProps
  extends Umum,
    Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children" | "className"> {
  /** true untuk tombol hanya-ikon (36×36) seperti prototype .icon-btn. */
  iconOnly?: boolean;
  /** Label aksesibilitas wajib bila iconOnly dan tidak ada teks. */
  "aria-label"?: string;
}

/**
 * Button memetakan .btn prototype (.btn-primary/.btn-secondary/.btn-ghost/
 * .btn-danger-ghost) ke komponen React.
 */
export function Button({
  variant = "secondary",
  block,
  icon,
  iconOnly,
  className,
  children,
  type = "button",
  ...rest
}: ButtonProps) {
  return (
    <button
      type={type}
      className={cn(
        iconOnly ? styles.iconBtn : styles.btn,
        !iconOnly && KELAS_VARIAN[variant],
        block && styles.block,
        className,
      )}
      {...rest}
    >
      {iconOnly ? (icon ? <Icon name={icon} /> : children) : isi({ icon, children })}
    </button>
  );
}

export interface ButtonLinkProps
  extends Umum,
    Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "children" | "className"> {
  href: string;
  iconOnly?: boolean;
}

/**
 * ButtonLink adalah varian <a> dari Button — dipakai untuk tautan bergaya
 * tombol (mis. "Kembali", aksi navigasi) tanpa kehilangan semantik tautan.
 */
export function ButtonLink({
  href,
  variant = "secondary",
  block,
  icon,
  iconOnly,
  className,
  children,
  ...rest
}: ButtonLinkProps) {
  return (
    <a
      href={href}
      className={cn(
        iconOnly ? styles.iconBtn : styles.btn,
        !iconOnly && KELAS_VARIAN[variant],
        !iconOnly && styles.btnIconSized,
        block && styles.block,
        className,
      )}
      {...rest}
    >
      {iconOnly ? (icon ? <Icon name={icon} /> : children) : isi({ icon, children })}
    </a>
  );
}
