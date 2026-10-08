import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const PENGATURAN = {
  biaya_packaging: 3000,
  cnc_hari: 7,
  token_hari: 14,
  reset_menit: 60,
  max_mb: 5,
  max_file: 5,
  digit_invoice: 2,
  manager_boleh_revisi: true,
  ai: { aktif: false, min_keyakinan: 0.9, tol_kurang: 0, tol_lebih: 0, digit_rek: 4, mundur: 1 },
};

const BACKUP = {
  terakhir: [
    {
      id: "run-2",
      jenis: "postgres",
      mulai: "2026-10-08T19:00:00Z", // 02:00 WIB 9 Okt 2026
      selesai: "2026-10-08T19:01:00Z",
      sukses: true,
      ukuran_byte: 3_145_728,
      tujuan: "rclone:gdrive:grahita-backup:postgres/grahita-2026-10-09.dump.age",
      pesan: "dump grahita@127.0.0.1:5455/grahita",
    },
    {
      id: "run-1",
      jenis: "postgres",
      mulai: "2026-10-07T19:00:00Z",
      selesai: "2026-10-07T19:00:05Z",
      sukses: false,
      ukuran_byte: 0,
      tujuan: "",
      pesan: "rclone rcat gagal: kredensial Drive kedaluwarsa",
    },
  ],
  perlu_tindakan: true,
  pesan: "Cadangan postgres gagal pada 08 Okt 2026 02:00 WIB dan belum ada cadangan sukses sesudahnya.",
  tujuan: "rclone:gdrive:grahita-backup",
  jadwal: "setiap hari 02:00 WIB",
  retensi: { harian: 30, bulanan: 12, wal_hari: 7, bukti_tanpa_batas: true },
  sukses_terakhir: { postgres: "2026-10-08T19:01:00Z" },
};

function buatRouter(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

function stubFetch(role: string, backup: unknown = BACKUP) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      if (input.endsWith("/auth/me")) {
        return json({
          data: {
            user: { id: "a1", kind: "admin", role, nama: "Rina Kartika", email: "rina@example.test" },
          },
        });
      }
      if (input.endsWith("/admin/settings")) return json({ data: PENGATURAN });
      if (input.endsWith("/admin/backup-runs")) return json({ data: backup });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
}

beforeEach(() => {
  document.cookie = "grahita_csrf=token-uji";
});

describe("Admin Portal — /pengaturan (F1-21)", () => {
  it("Superadmin melihat status cadangan, retensi tanpa batas bukti, dan alarm kegagalan", async () => {
    stubFetch("superadmin");

    render(<RouterProvider router={buatRouter("/pengaturan")} />);

    // Judul bagian backup dan jadwal harian 02:00 WIB (muncul setelah data tiba).
    expect(await screen.findByText("Backup harian")).toBeDefined();
    expect(await screen.findByText(/setiap hari 02:00 WIB/)).toBeDefined();

    // Retensi: 30 harian, 12 bulanan, WAL 7 hari, bukti transfer TANPA batas.
    expect(
      await screen.findByText(/30 harian, 12 bulanan, WAL 7 hari, bukti transfer tanpa batas/),
    ).toBeDefined();

    // Riwayat: satu sukses, satu gagal + alarm "perlu tindakan".
    expect(await screen.findByText("Sukses")).toBeDefined();
    expect(screen.getByText("Gagal")).toBeDefined();
    expect(
      screen.getByText(/Cadangan postgres gagal pada 08 Okt 2026 02:00 WIB dan belum ada cadangan sukses sesudahnya/),
    ).toBeDefined();
    expect(screen.getByText(/kredensial Drive kedaluwarsa/)).toBeDefined();
  });

  it("Manager ditolak dari halaman Pengaturan (tidak pernah melihat status backup)", async () => {
    stubFetch("manager");

    render(<RouterProvider router={buatRouter("/pengaturan")} />);

    expect(await screen.findByText("Tidak berwenang")).toBeDefined();
    expect(screen.queryByText("Backup harian")).toBeNull();
  });

  it("tujuan cadangan yang belum dikonfigurasi ditampilkan apa adanya", async () => {
    stubFetch("superadmin", {
      ...BACKUP,
      terakhir: [],
      perlu_tindakan: false,
      pesan: undefined,
      tujuan: "",
    });

    render(<RouterProvider router={buatRouter("/pengaturan")} />);

    expect(await screen.findByText(/belum dikonfigurasi/)).toBeDefined();
    expect(await screen.findByText(/Belum ada pekerjaan cadangan tercatat/)).toBeDefined();
  });
});
