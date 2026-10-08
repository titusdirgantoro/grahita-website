import { cn } from "../cn";
import markUrl from "../assets/logo-grahita-mark-128.png";
import lockupUrl from "../assets/logo-grahita-lockup-320.png";
import styles from "./Logo.module.css";

export type LogoVariant = "mark" | "lockup";

export interface LogoProps {
  /**
   * `mark` — lambang (kotak kuning + huruf G) untuk sidebar/topbar yang
   * sempit; `lockup` — lambang + tulisan "GRAHITA INDONESIA" untuk halaman
   * auth dan profil. Keduanya diturunkan dari SATU aset resmi yang sama
   * (lihat catatan di Logo.module.css).
   */
  variant?: LogoVariant;
  /** Tinggi tampilan dalam piksel; lebar mengikuti proporsi aset. */
  height?: number;
  /**
   * Teks alternatif. Bawaannya bermakna ("Grahita Indonesia"); kirim `""`
   * hanya bila nama itu sudah diucapkan teks di sebelahnya (mis. Brand yang
   * punya wordmark) supaya pembaca layar tidak menyebut dua kali.
   */
  alt?: string;
  className?: string;
}

/**
 * Logo adalah aset branding resmi GRAHITA INDONESIA (keputusan Tius dp,
 * 8 Oktober 2026).
 *
 * Aset TIDAK digambar ulang dan warnanya TIDAK diubah: berkas di
 * `src/assets/` hanya dipotong/diperkecil dari berkas sumber yang sama
 * (`logo-grahita-source.png`). Proporsi dipertahankan (tinggi ditentukan,
 * lebar otomatis) supaya tidak terdistorsi.
 */
export function Logo({ variant = "mark", height, alt = "Grahita Indonesia", className }: LogoProps) {
  const src = variant === "lockup" ? lockupUrl : markUrl;
  return (
    <img
      className={cn(styles.logo, variant === "lockup" ? styles.lockup : styles.mark, className)}
      src={src}
      alt={alt}
      style={height ? { height, width: "auto" } : undefined}
      draggable={false}
    />
  );
}

export { styles as logoStyles };
