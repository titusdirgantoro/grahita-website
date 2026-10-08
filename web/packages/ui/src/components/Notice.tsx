import type { HTMLAttributes, ReactNode } from "react";

import { cn } from "../cn";
import { Icon } from "../icon";
import styles from "./Notice.module.css";

export type NoticeTone = "info" | "warning" | "success" | "danger";

const IKON_BAWAAN: Record<NoticeTone, string> = {
  info: "info",
  warning: "warning",
  success: "check-circle",
  danger: "warning-octagon",
};

export interface NoticeProps extends HTMLAttributes<HTMLDivElement> {
  tone?: NoticeTone;
  icon?: string;
  children: ReactNode;
}

/**
 * Notice meniru `.notice` + `n-*` prototype. `role="alert"` dipakai untuk nada
 * danger agar pembaca layar langsung mengumumkannya.
 */
export function Notice({ tone = "info", icon, children, className, ...rest }: NoticeProps) {
  return (
    <div
      className={cn(styles.notice, styles[tone], className)}
      role={tone === "danger" ? "alert" : "status"}
      {...rest}
    >
      <Icon name={icon ?? IKON_BAWAAN[tone]} />
      <span>{children}</span>
    </div>
  );
}
