import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";

import styles from "./Toast.module.css";

interface ToastItem {
  id: number;
  pesan: string;
}

interface ToastApi {
  /** Tampilkan pesan singkat (hilang otomatis setelah fewDetik). */
  tampilkan: (pesan: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

export interface ToastProviderProps {
  children: ReactNode;
  /** Lama pesan tampil (ms). */
  durasi?: number;
}

/**
 * ToastProvider meniru pola `#toasts` prototype: pesan singkat muncul di
 * kiri-bawah (mobile: atas) lalu hilang sendiri. Area pesan `aria-live="polite"`
 * supaya pembaca layar mengumumkannya.
 */
export function ToastProvider({ children, durasi = 4000 }: ToastProviderProps) {
  const [daftar, setDaftar] = useState<ToastItem[]>([]);
  const seq = useRef(0);

  const tampilkan = useCallback(
    (pesan: string) => {
      const id = ++seq.current;
      setDaftar((kini) => [...kini, { id, pesan }]);
      window.setTimeout(() => {
        setDaftar((kini) => kini.filter((t) => t.id !== id));
      }, durasi);
    },
    [durasi],
  );

  const api = useMemo(() => ({ tampilkan }), [tampilkan]);

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className={styles.viewport} aria-live="polite" aria-atomic="false">
        {daftar.map((t) => (
          <div className={styles.toast} key={t.id} role="status">
            {t.pesan}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

/** useToast mengembalikan API untuk menampilkan pesan; aman bila tanpa provider. */
export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  return ctx ?? { tampilkan: () => undefined };
}

export { styles as toastStyles };
