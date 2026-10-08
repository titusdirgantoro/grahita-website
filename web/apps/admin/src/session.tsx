import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import { api } from "@grahita/api-client";
import type { Role } from "@grahita/ui";

export interface Pengguna {
  id: string;
  kind: string;
  role: string;
  nama: string;
  email: string;
}

export type StatusSesi = "memuat" | "masuk" | "belum";

export interface Sesi {
  status: StatusSesi;
  pengguna: Pengguna | null;
  peran: Role;
  masuk: (email: string, password: string) => Promise<void>;
  keluar: () => Promise<void>;
}

const SesiContext = createContext<Sesi | null>(null);

/** peranDari memetakan role API ke peran UI (selain superadmin → manager). */
export function peranDari(role: string | undefined): Role {
  return role === "superadmin" ? "superadmin" : "manager";
}

export function SessionProvider({
  children,
  penggunaAwal,
}: {
  children: ReactNode;
  /** Dipakai pengujian/gallery agar tidak memanggil API. */
  penggunaAwal?: Pengguna | null;
}) {
  const [status, setStatus] = useState<StatusSesi>(penggunaAwal !== undefined ? "masuk" : "memuat");
  const [pengguna, setPengguna] = useState<Pengguna | null>(penggunaAwal ?? null);

  useEffect(() => {
    if (penggunaAwal !== undefined) return;
    let aktif = true;
    api
      .get<{ user: Pengguna }>("/auth/me")
      .then((r) => {
        if (!aktif) return;
        setPengguna(r.user);
        setStatus("masuk");
      })
      .catch(() => {
        if (!aktif) return;
        setPengguna(null);
        setStatus("belum");
      });
    return () => {
      aktif = false;
    };
  }, [penggunaAwal]);

  const masuk = useCallback(async (email: string, password: string) => {
    const hasil = await api.post<{ user: Pengguna }>("/auth/login", { email, password });
    setPengguna(hasil.user);
    setStatus("masuk");
  }, []);

  const keluar = useCallback(async () => {
    await api.post("/auth/logout").catch(() => undefined);
    setPengguna(null);
    setStatus("belum");
  }, []);

  const nilai = useMemo<Sesi>(
    () => ({ status, pengguna, peran: peranDari(pengguna?.role), masuk, keluar }),
    [status, pengguna, masuk, keluar],
  );

  return <SesiContext.Provider value={nilai}>{children}</SesiContext.Provider>;
}

/** useSesi mengembalikan konteks sesi; error bila dipakai di luar provider. */
export function useSesi(): Sesi {
  const ctx = useContext(SesiContext);
  if (!ctx) throw new Error("useSesi harus dipakai di dalam <SessionProvider>");
  return ctx;
}
