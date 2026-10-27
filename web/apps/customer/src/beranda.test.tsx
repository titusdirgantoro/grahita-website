import { render, screen, waitFor, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";
import type { Pesanan } from "./pages/pesanan";
import { hariIni } from "./pages/beranda";

/**
 * Uji pBeranda (kartu F1-19): jumlah/status diambil dari daftar pesanan yang
 * sama dengan halaman Pesanan, dan hanya 5 pesanan terakhir yang ditampilkan.
 */

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const penggunaCabang = {
  id: "u1",
  kind: "branch",
  role: "branch",
  nama: "Grahita Bandung",
  email: "bandung@example.test",
  branch_id: "b1",
};

interface Panggilan {
  url: string;
  init?: RequestInit;
}

function fetchBeranda(orders: Pesanan[]) {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
      if (input.endsWith("/orders")) return json({ data: orders });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

function buatRouter(path = "/") {
  return createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
}

/** pad melengkapi angka menjadi dua digit. */
const pad = (n: number) => String(n).padStart(2, "0");

/** wib menggeser `sekarang` ke tanggal bisnis WIB (UTC+7), sama dengan produksi. */
function wib(sekarang: Date): Date {
  return new Date(sekarang.getTime() + 7 * 60 * 60 * 1000);
}

/** bulanGeser mengembalikan YYYY-MM bulan WIB ini digeser `delta` bulan. */
function bulanGeser(delta: number, sekarang = new Date()): string {
  const d = wib(sekarang);
  const m = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + delta, 1));
  return `${m.getUTCFullYear()}-${pad(m.getUTCMonth() + 1)}`;
}

/** pesanan membuat satu pesanan uji dengan field minimum yang dipakai beranda. */
function pesanan(ubah: Partial<Pesanan> & { id: string }): Pesanan {
  return {
    nomor: `2026-10-${ubah.id}`,
    status: "submitted",
    is_cnc: false,
    total: 100000,
    jumlah_item: 1,
    created_at: "2026-10-01T00:00:00Z",
    ...ubah,
  } as Pesanan;
}

describe("Customer Portal — Beranda (F1-19)", () => {
  it("menampilkan ringkasan periode, jumlah menunggu verifikasi, dan draft", async () => {
    const bulan = bulanGeser(0);
    const lalu = bulanGeser(-1);
    const orders: Pesanan[] = [
      pesanan({ id: "0001", status: "done", tanggal: `${bulan}-05`, total: 300000 }),
      pesanan({
        id: "0002",
        status: "submitted",
        tanggal: `${bulan}-06`,
        total: 200000,
        is_cnc: true,
      }),
      pesanan({ id: "0003", status: "draft", nomor: undefined, tanggal: undefined, total: 50000 }),
      pesanan({ id: "0004", status: "done", tanggal: `${lalu}-20`, total: 400000 }),
      pesanan({ id: "0005", status: "done", tanggal: `${lalu}-10`, total: 700000 }),
      pesanan({ id: "0006", status: "done", tanggal: `${lalu}-05`, total: 800000 }),
      pesanan({ id: "0007", status: "done", tanggal: `${lalu}-01`, total: 900000 }),
    ];
    const panggilan = fetchBeranda(orders);

    render(<RouterProvider router={buatRouter("/")} />);

    // Ringkasan periode berjalan: 2 pesanan non-draft bulan ini, total Rp500.000.
    expect((await screen.findByTestId("kpi-pesanan-periode")).textContent?.trim()).toBe("2");
    expect(screen.getByTestId("kpi-total-periode").textContent).toContain("500.000");
    expect(screen.getByTestId("kpi-menunggu").textContent?.trim()).toBe("1");
    expect(screen.getByTestId("kpi-draft").textContent?.trim()).toBe("1");

    // Pesanan C&C (dibayar deposit) tetap "Menunggu verifikasi" sampai Pusat
    // memverifikasi; beranda tidak lagi punya pengingat jatuh tempo.
    const tabel = screen.getByRole("table", { name: "Lima pesanan terakhir cabang" });
    const baris = within(tabel)
      .getAllByRole("row")
      .find((r) => r.textContent?.includes("2026-10-0002"));
    expect(baris?.textContent).toContain("Menunggu verifikasi");
    expect(baris?.textContent).not.toContain("Selesai");

    // Tepat 5 pesanan terakhir yang tampil (satu baris header + 5 baris data).
    expect(within(tabel).getAllByRole("row")).toHaveLength(6);
    expect(within(tabel).queryByText(/2026-10-0007/)).toBeNull();
    // Urutan baris = urutan server apa adanya (klien tidak menyortir ulang).
    const nomor = within(tabel)
      .getAllByRole("row")
      .slice(1)
      .map((r) => r.querySelector("b")?.textContent);
    expect(nomor).toEqual(orders.slice(0, 5).map((o) => o.nomor ?? "Draft"));

    // Beranda tidak pernah mengubah data: seluruh panggilan adalah GET.
    expect(panggilan.every((p) => !p.init || (p.init.method ?? "GET") === "GET")).toBe(true);
    // Sumber data tunggal: daftar pesanan cabang sesi (tanpa parameter cabang).
    expect(panggilan.filter((p) => p.url === "/api/v1/orders")).toHaveLength(1);
    expect(panggilan.some((p) => p.url.includes("branch_id"))).toBe(false);
  });

  it("menampilkan keadaan kosong saat cabang belum punya pesanan", async () => {
    fetchBeranda([]);

    render(<RouterProvider router={buatRouter("/")} />);

    expect((await screen.findByTestId("kpi-pesanan-periode")).textContent?.trim()).toBe("0");
    expect(screen.getByTestId("kpi-menunggu").textContent?.trim()).toBe("0");
    expect(screen.getByTestId("kpi-draft").textContent?.trim()).toBe("0");
    expect(
      await screen.findByText(
        "Belum ada pesanan. Pesanan yang Anda kirim akan tampil di sini beserta statusnya.",
      ),
    ).toBeDefined();
  });

  it("membedakan Tempo disetujui dari pesanan yang masih menunggu verifikasi", async () => {
    const bulan = bulanGeser(0);
    fetchBeranda([
      pesanan({ id: "tempo", status: "approved_unpaid", tanggal: `${bulan}-05`, is_tempo: true }),
      pesanan({ id: "pending", status: "submitted", tanggal: `${bulan}-06`, is_tempo: true }),
    ]);

    render(<RouterProvider router={buatRouter("/")} />);

    const tabel = await screen.findByRole("table", { name: "Lima pesanan terakhir cabang" });
    const barisTempo = within(tabel).getAllByRole("row")
      .find((row) => row.textContent?.includes("2026-10-tempo"));
    expect(barisTempo?.textContent).toContain("Disetujui — belum lunas");
    expect(barisTempo?.textContent).not.toContain("Menunggu verifikasi");
    expect(screen.getByTestId("kpi-menunggu").textContent?.trim()).toBe("1");
    expect(screen.getByText("Admin Pusat sedang meninjau pesanan Anda.")).toBeDefined();
  });

  it("menampilkan galat dari API, bukan angka palsu", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
        return json({ error: { code: "UNAVAILABLE", message: "Layanan pesanan belum tersedia." } }, 503);
      }),
    );

    render(<RouterProvider router={buatRouter("/")} />);

    await waitFor(() => {
      expect(screen.getByText("Layanan pesanan belum tersedia.")).toBeDefined();
    });
    expect(screen.queryByTestId("kpi-pesanan-periode")).toBeNull();
  });
});

describe("Customer Portal — tanggal bisnis beranda (F1-19)", () => {
  it("hariIni memakai tanggal bisnis WIB, bukan zona waktu browser", () => {
    // 2026-10-09T18:30Z = 2026-10-10 01:30 WIB (sudah ganti hari).
    expect(hariIni(new Date("2026-10-09T18:30:00Z"))).toBe("2026-10-10");
    // 2026-10-31T17:00Z = 2026-11-01 00:00 WIB (pergantian bulan).
    expect(hariIni(new Date("2026-10-31T17:00:00Z"))).toBe("2026-11-01");
    // Tengah hari UTC tetap hari yang sama di WIB.
    expect(hariIni(new Date("2026-10-09T05:00:00Z"))).toBe("2026-10-09");
  });
});
