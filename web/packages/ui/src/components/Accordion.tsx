import type { ReactNode } from "react";

import { cn } from "../cn";
import { Icon } from "../icon";
import styles from "./Accordion.module.css";

export interface AccordionProps {
  summary: ReactNode;
  children: ReactNode;
  open?: boolean;
  className?: string;
}

/**
 * Accordion meniru `details.tut` prototype: ringkasan bisa dibuka-tutup,
 * dipakai halaman Tutorial. Memakai elemen native <details> agar dapat
 * dioperasikan keyboard tanpa JavaScript tambahan.
 */
export function Accordion({ summary, children, open, className }: AccordionProps) {
  return (
    <details className={cn(styles.tut, className)} open={open}>
      <summary>
        {summary}
        <Icon name="caret-down" className={styles.caret} />
      </summary>
      <ol className={styles.body}>{children}</ol>
    </details>
  );
}

export { styles as accordionStyles };
