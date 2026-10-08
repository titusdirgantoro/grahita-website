import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from "react";

import { cn } from "../cn";
import { Icon } from "../icon";
import styles from "./Badge.module.css";

export type BadgeTone =
  | "draft"
  | "neutral"
  | "warning"
  | "danger"
  | "success"
  | "info"
  | "cnc"
  | "rev";

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  tone?: BadgeTone;
  icon?: string;
  children?: ReactNode;
}

/** Badge meniru `.badge` + `b-*` prototype (status pesanan, C&C, revisi). */
export function Badge({ tone = "neutral", icon, children, className, ...rest }: BadgeProps) {
  return (
    <span className={cn(styles.badge, styles[tone], className)} {...rest}>
      {icon ? <Icon name={icon} /> : null}
      {children}
    </span>
  );
}

export interface ChipProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "onClick" | "onToggle"> {
  /** Status tekan (dipakai untuk filter yang bisa dinyalakan). */
  pressed?: boolean;
  onToggle?: (pressed: boolean) => void;
  onRemove?: () => void;
  removeLabel?: string;
  children: ReactNode;
}

/**
 * Chip adalah `.chip` prototype: tombol filter dengan `aria-pressed` dan
 * tombol silang opsional untuk menyembunyikan (dipakai chip "perlu tindakan").
 */
export function Chip({
  pressed,
  onToggle,
  onRemove,
  removeLabel = "Sembunyikan",
  children,
  className,
  ...rest
}: ChipProps) {
  return (
    <button
      type="button"
      className={cn(styles.chip, className)}
      aria-pressed={pressed ?? undefined}
      onClick={onToggle ? () => onToggle(!pressed) : undefined}
      {...rest}
    >
      {children}
      {onRemove ? (
        <span
          className={styles.chipX}
          role="button"
          tabIndex={0}
          aria-label={removeLabel}
          onClick={(e) => {
            e.stopPropagation();
            onRemove();
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") {
              e.preventDefault();
              e.stopPropagation();
              onRemove();
            }
          }}
        >
          <Icon name="x" />
        </span>
      ) : null}
    </button>
  );
}

/** PillNote adalah label kecil netral (mis. "dibuat otomatis"). */
export function PillNote({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn(styles.pillnote, className)}>{children}</span>;
}
