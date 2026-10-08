import { useEffect, useState } from "react";

import { ApiClientError, api } from "@grahita/api-client";

export interface HasilApi<T> {
  data: T | null;
  memuat: boolean;
  galat: string | null;
}

/**
 * useApi memuat satu endpoint GET dan mengembalikan status memuat/galat.
 * Kartu F1-03 hanya butuh pembacaan sederhana untuk mengisi shell; cache
 * TanStack Query menggantikan ini pada kartu fitur berikutnya.
 */
export function useApi<T>(path: string, aktif = true): HasilApi<T> {
  const [data, setData] = useState<T | null>(null);
  const [memuat, setMemuat] = useState(aktif);
  const [galat, setGalat] = useState<string | null>(null);

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
  }, [path, aktif]);

  return { data, memuat, galat };
}
