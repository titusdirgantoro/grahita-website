import type { HTMLAttributes } from "react";

import { cn } from "../cn";
import { Logo } from "./Logo";
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

/**
 * Brand adalah pasangan logo resmi + wordmark di header shell.
 *
 * Lambang memakai aset resmi GRAHITA INDONESIA (lihat <Logo>). Gambar sengaja
 * diberi `alt=""`: nama sudah diucapkan teks di sebelahnya, jadi alt tambahan
 * hanya akan membuat pembaca layar menyebut "Grahita Indonesia Grahita".
 * Halaman yang hanya menampilkan gambar (auth/profil) memakai <Logo> dengan
 * alt bermakna.
 */
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
      <Logo alt="" />
      <span className={labelClassName}>{label}</span>
    </span>
  );
}

export { styles as brandStyles };
