import type { ReactNode } from "react";

import styles from "./kartu-master.module.css";

/**
 * Tampilan kartu master data (Cabang, Katalog product, Rekening bank): grid
 * kartu yang masing-masing satu tombol pembuka form detail. Aksi lanjutan
 * (nonaktifkan, hapus, kelola rekening) ada di form detail, jadi kartu tidak
 * memuat kontrol bersarang dan seluruh permukaannya bisa diklik/di-Enter.
 */

export function GridKartu({ label, children }: { label: string; children: ReactNode }) {
  return (
    <ul className={styles.grid} aria-label={label}>
      {children}
    </ul>
  );
}

export interface KartuMasterProps {
  judul: ReactNode;
  /** Badge status di pojok kanan atas. */
  status?: ReactNode;
  sub?: ReactNode;
  children?: ReactNode;
  /** Baris badge/keterangan di kaki kartu. */
  kaki?: ReactNode;
  /** Item nonaktif ditampilkan redup. */
  redup?: boolean;
  onBuka: () => void;
}

export function KartuMaster({ judul, status, sub, children, kaki, redup, onBuka }: KartuMasterProps) {
  return (
    <li className={redup ? `${styles.item} ${styles.redup}` : styles.item}>
      <button type="button" className={styles.kartu} onClick={onBuka} aria-haspopup="dialog">
        <span className={styles.kepala}>
          <span className={styles.judul}>{judul}</span>
          {status}
        </span>
        {sub ? <span className={styles.sub}>{sub}</span> : null}
        {children ? <span className={styles.isi}>{children}</span> : null}
        {kaki ? <span className={styles.kaki}>{kaki}</span> : null}
      </button>
    </li>
  );
}

/** BarAlat adalah baris filter di atas grid kartu (segmented, cari, pilihan). */
export function BarAlat({ children, info }: { children: ReactNode; info?: ReactNode }) {
  return (
    <div className={styles.alat}>
      <div className={styles.alatKontrol}>{children}</div>
      {info ? <span className="muted small">{info}</span> : null}
    </div>
  );
}

export { styles as kartuMasterStyles };
