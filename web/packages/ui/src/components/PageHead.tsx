import type { ReactNode } from "react";

import { cn } from "../cn";
import styles from "./PageHead.module.css";

export interface PageHeadProps {
  title: ReactNode;
  sub?: ReactNode;
  /** Tombol/aksi di sisi kanan judul. */
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
}

/** PageHead meniru `.page-head` prototype (judul + subjudul + aksi). */
export function PageHead({ title, sub, actions, children, className }: PageHeadProps) {
  return (
    <div className={cn(styles.pageHead, className)}>
      <div className={styles.crumb}>
        <h1>{title}</h1>
        {sub ? <p className={styles.sub}>{sub}</p> : null}
      </div>
      {children}
      {actions ? <div className={styles.actions}>{actions}</div> : null}
    </div>
  );
}

export { styles as pageHeadStyles };
