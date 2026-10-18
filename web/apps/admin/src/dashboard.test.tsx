import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";
import type { DashboardData } from "./pages/dashboard";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function buatRouter(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

const BULAN = ["Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"];

/**
 * dashboardUji menyusun satu respons GET /admin/dashboard. `nilai: false`
 * menirukan respons Manager: kunci `nilai` TIDAK ada sama sekali (bukan nol),
 * persis seperti yang dikirim server.
 */
function dashboardUji(nilai: boolean, patch?: Partial<DashboardData>): DashboardData {
  const metrik = (jumlah: number, rp: number) => (nilai ? { jumlah, nilai: rp } : { jumlah });
  return {
    filter: {
      periode: "bulan",
      dari: "2026-10-01",
      sampai: "2026-10-09",
      hari_ini: "2026-10-09",
      zona: "Asia/Jakarta",
    },
    ringkasan: metrik(5, 800000),
    pembayaran: {
      terverifikasi: metrik(1, 300000),
      menunggu: metrik(3, 300000),
      perlu_review: metrik(1, 200000),
    },
    chip: [
      { kunci: "menunggu", label: "menunggu verifikasi", jumlah: 3 },
      { kunci: "perlu_review", label: "perlu review", jumlah: 1 },
      { kunci: "email_gagal", label: "email gagal", jumlah: 0 },
    ],
    tren_bulanan: BULAN.map((label, i) => ({
      kunci: `2026-${String(i + 1).padStart(2, "0")}`,
      label,
      terverifikasi: i === 9 ? 1 : 0,
      ...(nilai ? { jumlah: i === 9 ? 5 : i === 8 ? 4 : 0, nilai: i === 9 ? 800000 : i === 8 ? 640000 : 0 } : { jumlah: i === 9 ? 5 : i === 8 ? 4 : 0 }),
    })),
    harian: Array.from({ length: 9 }, (_, i) => ({
      kunci: `2026-10-0${i + 1}`,
      label: String(i + 1),
      terverifikasi: 0,
      ...(nilai ? { jumlah: i === 8 ? 5 : 0, nilai: 0 } : { jumlah: i === 8 ? 5 : 0 }),
    })),
    peringkat_cabang: [
      { branch_id: "b1", nama: "Grahita Bandung", ...(nilai ? { jumlah: 3, nilai: 400000 } : { jumlah: 3 }) },
      { branch_id: "b2", nama: "Grahita Surabaya", ...(nilai ? { jumlah: 2, nilai: 350000 } : { jumlah: 2 }) },
    ],
    item: 12,
    pembanding: { dari: "2026-09-01", sampai: "2026-09-09", ringkasan: metrik(2, 300000), item: 4 },
    bulan_lalu: metrik(4, 640000),
    harian_pembanding: 2,
    komposisi: { reguler: 4, cnc: 1, sertifikat: 1 },
    produk_terlaris: [
      { product_id: "p1", nama: "Bio Magnetik", qty: 9 },
      { product_id: "p2", nama: "Rimpang Daun", qty: 3 },
    ],
    ...patch,
  };
}

/**
 * stubDashboard memasang fetch semu untuk dashboard, master cabang, dan
 * riwayat pemesanan (tujuan chip diklik).
 */
function stubDashboard(opsi?: { peran?: string; nilai?: boolean }) {
  const peran = opsi?.peran ?? "manager";
  const nilai = opsi?.nilai ?? peran === "superadmin";
  const panggilan: string[] = [];

  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = String(input);
      panggilan.push(url);
      if (url.endsWith("/auth/me")) {
        return json({
          data: {
            user: { id: "a1", kind: "admin", role: peran, nama: "Budi", email: "budi@example.test" },
          },
        });
      }
      if (url.includes("/admin/branches")) {
        return json({
          data: [
            { id: "b1", nama: "Grahita Bandung", is_active: true },
            { id: "b2", nama: "Grahita Surabaya", is_active: true },
          ],
        });
      }
      if (url.includes("/admin/dashboard")) {
        return json({ data: dashboardUji(nilai) });
      }
      if (url.includes("/admin/orders")) {
        return json({
          data: { items: [], total: 3, halaman: 1, per_halaman: 25 },
        });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return { panggilan };
}

describe("Dashboard admin (F1-20, tata letak admin_v2.html)", () => {
  it("Manager melihat jumlah pesanan beserta pembandingnya tanpa satu pun nilai rupiah", async () => {
    stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);

    expect((await screen.findByTestId("kpi-periode")).textContent).toBe("5 pesanan");
    expect(screen.getByText(/Halo, Budi\. Ini ringkasan pesanan cabang bulan ini/)).toBeDefined();
    // 5 pesanan bulan ini dibanding 2 pada periode sepadan bulan lalu.
    expect(screen.getByText("Lebih tinggi dari bulan lalu").textContent).toContain("+3");
    expect(screen.queryByText(/Rp/)).toBeNull();
  });

  it("Superadmin melihat penjualan, rata-rata nilai, dan cabang teraktif dalam rupiah", async () => {
    stubDashboard({ peran: "superadmin" });
    render(<RouterProvider router={buatRouter("/")} />);

    expect((await screen.findByTestId("kpi-periode")).textContent).toMatch(/Rp[\s\u00a0]?800\.000/);
    expect(screen.getByText("Penjualan per bulan")).toBeDefined();
    // Rata-rata 800.000/5 = 160.000 vs 300.000/2 = 150.000 → 10.000 lebih tinggi.
    expect(screen.getByText(/Rp[\s\u00a0]?10\.000 lebih tinggi/)).toBeDefined();
    expect(screen.getByTestId("cabang-teraktif").textContent).toMatch(/3 pesanan · Rp[\s\u00a0]?400\.000/);
  });

  it("chip tampil dengan angkanya dan membuka daftar terfilter saat diklik", async () => {
    const { panggilan } = stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);

    expect(await screen.findByText("3 menunggu verifikasi")).toBeDefined();
    expect(screen.getByLabelText("Sembunyikan: 1 perlu review")).toBeDefined();
    // Chip berangka 0 tidak ditampilkan.
    expect(screen.queryByLabelText("Sembunyikan: 0 email gagal")).toBeNull();

    fireEvent.click(screen.getByText("3 menunggu verifikasi"));
    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("/admin/orders?") && u.includes("verif=belum"))).toBe(true);
    });
  });

  it("chip peringatan lain memakai padanan filternya masing-masing", async () => {
    const { panggilan } = stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByText("1 perlu review");

    fireEvent.click(screen.getByText("1 perlu review"));
    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("/admin/orders?") && u.includes("verif=perlu_review"))).toBe(true);
    });
  });

  it("chip dapat disembunyikan lalu ditampilkan kembali", async () => {
    stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByLabelText("Sembunyikan: 3 menunggu verifikasi");

    fireEvent.click(screen.getByLabelText("Sembunyikan: 3 menunggu verifikasi"));
    await waitFor(() => {
      expect(screen.queryByLabelText("Sembunyikan: 3 menunggu verifikasi")).toBeNull();
    });
    expect(screen.getByLabelText("Sembunyikan: 1 perlu review")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "Tampilkan semua chip" }));
    expect(await screen.findByLabelText("Sembunyikan: 3 menunggu verifikasi")).toBeDefined();
  });

  it("filter cabang & periode dari URL dikirim sebagai query string dan tampil di topbar", async () => {
    const { panggilan } = stubDashboard();
    render(<RouterProvider router={buatRouter("/?cabang=b1&periode=hari")} />);
    await screen.findByTestId("kpi-periode");

    const panggil = panggilan.find((u) => u.includes("/admin/dashboard?"));
    expect(panggil).toContain("periode=hari");
    expect(panggil).toContain("cabang=b1");
    await waitFor(() => expect((screen.getByLabelText("Filter cabang") as HTMLSelectElement).value).toBe("b1"));
    expect(screen.getByRole("button", { name: "Hari ini" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("mengubah periode di topbar memuat ulang metrik dengan periode baru", async () => {
    const { panggilan } = stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByTestId("kpi-periode");

    fireEvent.click(screen.getByRole("button", { name: "Minggu ini" }));
    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("/admin/dashboard?periode=minggu"))).toBe(true);
    });
  });

  it("grafik bulanan: 12 slot bulan, batang hanya untuk bulan berisi, gelembung perubahan bulan berjalan", async () => {
    stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByTestId("kpi-periode");

    const grafik = screen.getByRole("img", { name: /Pesanan per bulan/ });
    expect(grafik.querySelectorAll("[data-bulan]").length).toBe(12);
    expect(grafik.querySelectorAll("[data-batang]").length).toBe(2);
    // Oktober 5 pesanan vs September (bulan lalu) 4 → +25,0%.
    expect(grafik.textContent).toContain("+25,0%");
  });

  it("grafik harian menandai hari ini dan pembanding periode sama bulan lalu", async () => {
    stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByTestId("kpi-periode");

    const harian = screen.getByRole("img", { name: "Pesanan harian bulan ini" });
    expect(harian.querySelector("[data-area]")).not.toBeNull();
    expect(screen.getByTestId("tip-harian").textContent).toBe("09.10 · 5");
    // 5 pesanan Oktober sampai tanggal 9 vs 2 pada 1–9 September.
    expect(screen.getByText("Dibanding periode sama bulan lalu").textContent).toContain("+3");
  });

  it("status pembayaran, komposisi, dan product terlaris memakai angka dari server", async () => {
    stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByTestId("kpi-periode");

    const legenda = screen.getByLabelText("Rincian status pembayaran").textContent ?? "";
    expect(legenda).toContain("Terverifikasi1");
    expect(legenda).toContain("Menunggu3");
    expect(legenda).toContain("Perlu review1");
    // Rata-rata item: 12/5 = 2,4 vs 4/2 = 2,0.
    expect(screen.getByText("2,4")).toBeDefined();
    expect(screen.getByText("0,4 lebih banyak")).toBeDefined();
    expect(screen.getByText("1 sertifikat")).toBeDefined();
    expect(screen.getByText("dibayar deposit")).toBeDefined();
    expect(screen.getByText("80,0%")).toBeDefined();
    const produk = screen.getByTestId("produk-terlaris").textContent ?? "";
    expect(produk.indexOf("Bio Magnetik")).toBeLessThan(produk.indexOf("Rimpang Daun"));
    expect(produk).toContain("9 item");
  });

  it("tombol + di topbar membuka form tambah cabang", async () => {
    stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByTestId("kpi-periode");

    fireEvent.click(screen.getByRole("button", { name: "Tambah cabang" }));
    expect(await screen.findByRole("heading", { name: "Tambah cabang" })).toBeDefined();
  });
});
