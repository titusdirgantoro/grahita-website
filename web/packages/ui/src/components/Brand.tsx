import type { HTMLAttributes } from "react";

import { cn } from "../cn";
import styles from "./Brand.module.css";

export interface BrandProps extends HTMLAttributes<HTMLSpanElement> {
  /** Teks di samping logo (default "Grahita"). */
  label?: string;
  /** Kelas tambahan untuk teks label (mis. disembunyikan saat sidebar ciut). */
  labelClassName?: string;
  /** Warna teks putih untuk latar gelap (mis. halaman auth). */
  light?: boolean;
  /** Pusatkan isi (dipakai di halaman auth customer). */
  center?: boolean;
}

/** Brand adalah pasangan logo-mark + wordmark prototype. */
export function Brand({
  label = "Grahita",
  labelClassName,
  light,
  center,
  className,
  ...rest
}: BrandProps) {
  return (
    <span className={cn(styles.brand, light && styles.light, center && styles.center, className)} {...rest}>
      <span className={styles.mark} aria-hidden="true">
        G
      </span>
      <span className={labelClassName}>{label}</span>
    </span>
  );
}

export { styles as brandStyles };
