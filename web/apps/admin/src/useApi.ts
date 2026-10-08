import { useCallback, useEffect, useState } from "react";

import { ApiClientError, api } from "@grahita/api-client";

export interface HasilApi<T> {
  data: T | null;
  memuat: boolean;
  galat: string | null;
  /** muatUlang memaksa pemuatan ulang tanpa mengubah path. */
  muatUlang: () => void;
}

/**
 * useApi memuat satu endpoint GET dan mengembalikan status memuat/galat.
 *
 * Kartu F1-03 hanya butuh pembacaan sederhana untuk mengisi shell; kartu
 * riwayat pesanan admin (F1-15) menambah `muatUlang` supaya detail bisa
 * disegarkan setelah mutasi (mis. menyimpan catatan internal). Cache TanStack
 * Query menyusul pada kartu berikutnya.
 */
export function useApi<T>(path: string, aktif = true): HasilApi<T> {
  const [data, setData] = useState<T | null>(null);
  const [memuat, setMemuat] = useState(aktif);
  const [galat, setGalat] = useState<string | null>(null);
  const [versi, setVersi] = useState(0);

  const muatUlang = useCallback(() => setVersi((v) => v + 1), []);

  useEffect(() => {
    if (!aktif) return;
    let hidup = true;
    setMemuat(true);
    setGalat(null);
    api
      .get<T>(path)
      .then((v) => {
        if (hidup) setData(v);
      })
      .catch((err) => {
        if (!hidup) return;
        setGalat(err instanceof ApiClientError ? err.message : "Tidak bisa memuat data.");
      })
      .finally(() => {
        if (hidup) setMemuat(false);
      });
    return () => {
      hidup = false;
    };
  }, [path, aktif, versi]);

  return { data, memuat, galat, muatUlang };
}
