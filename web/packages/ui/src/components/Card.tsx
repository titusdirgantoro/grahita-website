import type { ReactNode } from "react";

import { cn } from "../cn";
import styles from "./Card.module.css";

export interface CardProps {
  children: ReactNode;
  className?: string;
  as?: "div" | "section" | "article" | "aside";
}

/** Card adalah `.card` prototype (radius & padding mengikuti token portal). */
export function Card({ children, className, as: Tag = "div" }: CardProps) {
  return <Tag className={cn(styles.card, className)}>{children}</Tag>;
}

export interface CardHeadProps {
  title: ReactNode;
  /** Aksi di sisi kanan head (tombol/segmented). */
  tools?: ReactNode;
  children?: ReactNode;
  className?: string;
}

/** CardHead meniru `.card-head` + `.card-title` prototype. */
export function CardHead({ title, tools, children, className }: CardHeadProps) {
  return (
    <div className={cn(styles.head, className)}>
      <h2 className={styles.title}>{title}</h2>
      {children}
      {tools ? <div className={styles.tools}>{tools}</div> : null}
    </div>
  );
}

export { styles as cardStyles };
