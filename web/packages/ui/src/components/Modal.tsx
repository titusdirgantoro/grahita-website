import { useCallback, useEffect, useId, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";

import { cn } from "../cn";
import { Button } from "./Button";
import styles from "./Modal.module.css";

export interface ModalProps {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}

const FOKUS_SELEKTOR =
  'a[href], button:not([disabled]), textarea, input, select, [tabindex]:not([tabindex="-1"])';

/**
 * Modal meniru `.modal` prototype: overlay gelap, judul, isi, footer.
 *
 * Aksesibilitas: `role="dialog"` + `aria-modal="true"` + `aria-labelledby`,
 * fokus dipindah ke kontrol pertama saat dibuka, Tab terperangkap di dalam
 * dialog, Escape menutup, dan fokus dikembalikan ke pemicu saat ditutup.
 */
export function Modal({ open, title, onClose, children, footer, wide }: ModalProps) {
  const judulId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const pemicuRef = useRef<Element | null>(null);

  const tutupDenganFokus = useCallback(() => onClose(), [onClose]);

  useEffect(() => {
    if (!open) return;

    pemicuRef.current = document.activeElement;
    const panel = panelRef.current;
    // Fokus utama: kontrol pertama di badan modal (seperti prototype), baru
    // jatuh ke tombol Tutup bila badan tidak punya kontrol.
    const utama = bodyRef.current?.querySelector<HTMLElement>(FOKUS_SELEKTOR);
    (utama ?? panel?.querySelector<HTMLElement>(FOKUS_SELEKTOR))?.focus();

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.stopPropagation();
        tutupDenganFokus();
        return;
      }
      if (event.key !== "Tab" || !panel) return;
      const fokusable = Array.from(panel.querySelectorAll<HTMLElement>(FOKUS_SELEKTOR));
      if (fokusable.length === 0) return;
      const pertama = fokusable[0];
      const terakhir = fokusable[fokusable.length - 1];
      const aktif = document.activeElement;
      if (event.shiftKey && aktif === pertama) {
        event.preventDefault();
        terakhir.focus();
      } else if (!event.shiftKey && aktif === terakhir) {
        event.preventDefault();
        pertama.focus();
      }
    }

    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
      const pemicu = pemicuRef.current;
      if (pemicu instanceof HTMLElement) pemicu.focus();
    };
  }, [open, tutupDenganFokus]);

  if (!open) return null;

  return createPortal(
    <div
      className={styles.overlay}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) tutupDenganFokus();
      }}
    >
      <div
        ref={panelRef}
        className={cn(styles.modal, wide && styles.wide)}
        role="dialog"
        aria-modal="true"
        aria-labelledby={judulId}
      >
        <div className={styles.head}>
          <h2 id={judulId}>{title}</h2>
          <Button
            iconOnly
            icon="x"
            onClick={tutupDenganFokus}
            aria-label="Tutup"
          />
        </div>
        <div className={styles.body} ref={bodyRef}>{children}</div>
        {footer ? <div className={styles.foot}>{footer}</div> : null}
      </div>
    </div>,
    document.body,
  );
}

export { styles as modalStyles };
