import { useNavigate, useSearch } from "@tanstack/react-router";

import { Select } from "@grahita/ui";

import { useApi } from "../useApi";
import type { Cabang } from "./shell-pages";
import styles from "./admin-pages.module.css";

/**
 * FilterCabangTopbar adalah pemilih cabang di topbar admin (admin_v2.html):
 * dipakai dashboard dan riwayat pemesanan. Nilainya search param `cabang`
 * milik rute itu sendiri, jadi satu-satunya sumber filter cabang halaman;
 * pada riwayat, mengganti cabang mengembalikan paginasi ke halaman 1.
 */
export function FilterCabangTopbar({ ke }: { ke: "/" | "/pesanan" }) {
  const { cabang } = useSearch({ strict: false }) as { cabang?: string };
  const navigate = useNavigate();
  const { data } = useApi<Cabang[]>("/admin/branches");
  return (
    <Select
      aria-label="Filter cabang"
      inputClassName={styles.cabSelect}
      value={cabang ?? ""}
      onChange={(e) =>
        void navigate({
          to: ke,
          search: ((prev: Record<string, unknown>) => ({
            ...prev,
            cabang: e.target.value || undefined,
            ...(ke === "/pesanan" ? { halaman: 1 } : {}),
          })) as never,
        })
      }
    >
      <option value="">Semua cabang</option>
      {(data ?? []).map((c) => (
        <option key={c.id} value={c.id}>
          {c.nama}
        </option>
      ))}
    </Select>
  );
}
