import { render, screen, waitFor, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";
import type { Pesanan } from "./pages/pesanan";

/**
 * Uji pBeranda (kartu F1-19): jumlah/status diambil dari daftar pesanan yang
 * sama dengan halaman Pesanan, pengingat C&C yang lewat jatuh tempo tampil
 * TANPA mengubah status pesanan, dan hanya 5 pesanan terakhir yang ditampilkan.
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

/** bulanGeser mengembalikan YYYY-MM bulan ini digeser `delta` bulan. */
function bulanGeser(delta: number, sekarang = new Date()): string {
  const d = new Date(Date.UTC(sekarang.getFullYear(), sekarang.getMonth() + delta, 1));
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}`;
}

/** tanggalGeser mengembalikan tanggal LOKAL hari ini digeser `hari`. */
function tanggalGeser(hari: number, sekarang = new Date()): string {
  const d = new Date(sekarang.getFullYear(), sekarang.getMonth(), sekarang.getDate() + hari);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
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
  it("menampilkan ringkasan periode, jumlah menunggu verifikasi, draft, dan pengingat C&C", async () => {
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
        // Lewat jatuh tempo dua hari: harus tampil sebagai pengingat, tetapi
        // pesanannya TIDAK berubah status.
        jatuh_tempo: tanggalGeser(-2),
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

    // Pengingat C&C yang sudah lewat jatuh tempo: tampil, tanpa auto-Done.
    const banner = await screen.findByTestId("banner-cnc");
    expect(banner.textContent).toMatch(/C&C lewat jatuh tempo/);
    expect(banner.textContent).toContain("2026-10-0002");
    // Pesanan C&C itu tetap "Menunggu verifikasi" di tabel pesanan terakhir.
    const tabel = screen.getByRole("table", { name: "Lima pesanan terakhir cabang" });
    const baris = within(tabel)
      .getAllByRole("row")
      .find((r) => r.textContent?.includes("2026-10-0002"));
    expect(baris?.textContent).toContain("Menunggu verifikasi");
    expect(baris?.textContent).not.toContain("Selesai");

    // Tepat 5 pesanan terakhir yang tampil (satu baris header + 5 baris data).
    expect(within(tabel).getAllByRole("row")).toHaveLength(6);
    expect(within(tabel).queryByText(/2026-10-0007/)).toBeNull();

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
    expect(screen.queryByTestId("banner-cnc")).toBeNull();
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
