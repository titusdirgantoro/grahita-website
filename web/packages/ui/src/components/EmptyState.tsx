import type { ReactNode } from "react";

import { cn } from "../cn";
import { Icon } from "../icon";
import styles from "./EmptyState.module.css";

export interface EmptyStateProps {
  /** Nama ikon Phosphor, mis. "receipt". */
  icon: string;
  children: ReactNode;
  /** Tombol aksi opsional di bawah teks. */
  action?: ReactNode;
  className?: string;
}

/** EmptyState meniru `.empty` prototype (blob + ikon + teks + aksi). */
export function EmptyState({ icon, children, action, className }: EmptyStateProps) {
  return (
    <div className={cn(styles.empty, className)}>
      <div className={cn(styles.blob, "hatch")}>
        <Icon name={icon} />
      </div>
      <p className={styles.text}>{children}</p>
      {action}
    </div>
  );
}
