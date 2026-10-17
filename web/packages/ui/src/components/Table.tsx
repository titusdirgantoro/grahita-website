import { useLayoutEffect, useRef, type ReactNode } from "react";

import { cn } from "../cn";
import styles from "./Table.module.css";

/**
 * Ambang lebar KONTAINER (bukan viewport) tempat tabel `stack` berubah menjadi
 * kartu berlabel: `narrow` < 640px untuk tabel 4–6 kolom, `wide` < 960px untuk
 * tabel lebar (7+ kolom atau sel aksi bertombol banyak).
 */
export type TableStack = "narrow" | "wide";

export interface TableProps {
  children: ReactNode;
  /** true: baris berubah menjadi kartu di layar sempit (.tbl-m-cards). */
  mobileCards?: boolean;
  /**
   * Tumpuk tiap baris menjadi kartu berlabel saat kontainernya sempit. Label
   * setiap sel diambil dari teks `<th>` kolomnya; sel di bawah header kosong
   * (kolom aksi) memakai lebar penuh tanpa label.
   */
  stack?: TableStack;
  className?: string;
  /** Keterangan tabel untuk pembaca layar. */
  caption?: string;
}

/**
 * labelSel menyalin teks header kolom ke `data-label` setiap sel tbody
 * (memperhitungkan `colSpan`) sehingga CSS bisa menampilkan label di mode
 * kartu tanpa setiap halaman mengulang teks header di tiap `<td>`.
 */
function labelSel(table: HTMLTableElement) {
  const headRow = table.tHead?.rows[table.tHead.rows.length - 1];
  const labels: string[] = [];
  for (const th of Array.from(headRow?.cells ?? [])) {
    const teks = th.textContent?.trim() ?? "";
    for (let i = 0; i < Math.max(1, th.colSpan); i++) labels.push(teks);
  }
  for (const body of Array.from(table.tBodies)) {
    for (const tr of Array.from(body.rows)) {
      let kolom = 0;
      for (const td of Array.from(tr.cells)) {
        const label = td.colSpan > 1 ? "" : (labels[kolom] ?? "");
        if (td.getAttribute("data-label") !== label) td.setAttribute("data-label", label);
        kolom += Math.max(1, td.colSpan);
      }
    }
  }
}

/** Table meniru `table.tbl` + pembungkus `.table-wrap` prototype. */
export function Table({ children, mobileCards, stack, className, caption }: TableProps) {
  const ref = useRef<HTMLTableElement>(null);

  useLayoutEffect(() => {
    const table = ref.current;
    if (!stack || !table) return;
    labelSel(table);
    // Baris bisa bertambah/berganti tanpa Table ikut render ulang (state di
    // dalam sel), jadi label disegarkan setiap kali struktur tabel berubah.
    const observer = new MutationObserver(() => labelSel(table));
    observer.observe(table, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [stack]);

  return (
    <div className={cn(styles.tableWrap, stack && styles.stackWrap)}>
      <table
        ref={ref}
        className={cn(
          styles.tbl,
          mobileCards && styles.mobileCards,
          stack === "narrow" && styles.stackNarrow,
          stack === "wide" && styles.stackWide,
          className,
        )}
      >
        {caption ? <caption className="sr">{caption}</caption> : null}
        {children}
      </table>
    </div>
  );
}

export { styles as tableStyles };
