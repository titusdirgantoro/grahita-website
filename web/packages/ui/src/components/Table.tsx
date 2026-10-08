import type { ReactNode } from "react";

import { cn } from "../cn";
import styles from "./Table.module.css";

export interface TableProps {
  children: ReactNode;
  /** true: baris berubah menjadi kartu di layar sempit (.tbl-m-cards). */
  mobileCards?: boolean;
  className?: string;
  /** Keterangan tabel untuk pembaca layar. */
  caption?: string;
}

/** Table meniru `table.tbl` + pembungkus `.table-wrap` prototype. */
export function Table({ children, mobileCards, className, caption }: TableProps) {
  return (
    <div className={styles.tableWrap}>
      <table className={cn(styles.tbl, mobileCards && styles.mobileCards, className)}>
        {caption ? <caption className="sr">{caption}</caption> : null}
        {children}
      </table>
    </div>
  );
}

export { styles as tableStyles };
