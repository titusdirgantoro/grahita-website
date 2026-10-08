import { cn } from "../cn";
import styles from "./Skeleton.module.css";

export interface SkeletonProps {
  /** Bentuk skeleton: garis teks, judul, blok, atau lingkaran (avatar). */
  variant?: "line" | "title" | "block" | "circle";
  width?: string;
  className?: string;
}

/** Skeleton adalah placeholder pemuatan bergaya hatch prototype. */
export function Skeleton({ variant = "line", width, className }: SkeletonProps) {
  return (
    <span
      className={cn(styles.skeleton, styles[variant], className)}
      style={width ? { width } : undefined}
      aria-hidden="true"
    />
  );
}

export interface SkeletonListProps {
  /** Jumlah baris skeleton yang ditampilkan. */
  rows?: number;
  className?: string;
}

/** SkeletonList meniru pola daftar/tabel yang sedang dimuat. */
export function SkeletonList({ rows = 3, className }: SkeletonListProps) {
  return (
    <div className={cn(styles.stack, className)} aria-busy="true" aria-live="polite">
      <span className="sr">Memuat data…</span>
      {Array.from({ length: rows }, (_, i) => (
        <div className={styles.row} key={i}>
          <Skeleton variant="circle" />
          <div className={styles.grow}>
            <Skeleton variant="title" />
            <Skeleton />
          </div>
        </div>
      ))}
    </div>
  );
}

export { styles as skeletonStyles };
