import type { ReactNode } from "react";

import { cn } from "../cn";
import { Brand } from "./Brand";
import styles from "./AuthLayout.module.css";

export interface AuthLayoutProps {
  children: ReactNode;
  /** Teks kecil di bawah kartu (mis. catatan prototype). */
  hint?: ReactNode;
  /** Jumlah langkah wizard; bila diisi, tampilkan indikator langkah. */
  steps?: { total: number; aktif: number };
  className?: string;
}

/**
 * AuthLayout meniru blok "Auth" prototype (.auth/.auth-wrap/.auth-card).
 * Halaman masuk/daftar/lupa-password memakai layout ini, terpisah dari shell.
 */
export function AuthLayout({ children, hint, steps, className }: AuthLayoutProps) {
  return (
    <div className={cn(styles.auth, className)}>
      <div className={styles.authWrap}>
        <div className={styles.authBrand}>
          <Brand center />
        </div>
        <div className={styles.authCard}>
          {steps ? (
            <div className={styles.steps} aria-hidden="true">
              {Array.from({ length: steps.total }, (_, i) => (
                <span key={i} className={i < steps.aktif ? styles.on : undefined} />
              ))}
            </div>
          ) : null}
          {children}
        </div>
        {hint ? <p className={styles.hint}>{hint}</p> : null}
      </div>
    </div>
  );
}

export { styles as authLayoutStyles };

/** AuthLinks adalah baris tautan di bawah form auth (.auth-links). */
export function AuthLinks({ children }: { children: ReactNode }) {
  return <div className={styles.authLinks}>{children}</div>;
}
