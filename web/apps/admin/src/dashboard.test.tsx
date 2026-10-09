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
      { kunci: "lewat_jatuh_tempo", label: "C&C lewat jatuh tempo", jumlah: 2 },
      { kunci: "email_gagal", label: "email gagal", jumlah: 0 },
    ],
    tren_bulanan: BULAN.map((label, i) => ({
      kunci: `2026-${String(i + 1).padStart(2, "0")}`,
      label,
      ...(nilai ? { jumlah: i === 9 ? 5 : 0, nilai: i === 9 ? 800000 : 0 } : { jumlah: i === 9 ? 5 : 0 }),
    })),
    harian: Array.from({ length: 9 }, (_, i) => ({
      kunci: `2026-10-0${i + 1}`,
      label: String(i + 1),
      ...(nilai ? { jumlah: i === 8 ? 5 : 0, nilai: 0 } : { jumlah: i === 8 ? 5 : 0 }),
    })),
    peringkat_cabang: [
      { branch_id: "b1", nama: "Grahita Bandung", ...(nilai ? { jumlah: 3, nilai: 400000 } : { jumlah: 3 }) },
      { branch_id: "b2", nama: "Grahita Surabaya", ...(nilai ? { jumlah: 2, nilai: 350000 } : { jumlah: 2 }) },
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

describe("Dashboard admin (F1-20)", () => {
  it("Manager melihat jumlah pesanan tanpa satu pun nilai rupiah", async () => {
    stubDashboard({ peran: "manager" });
    render(<RouterProvider router={buatRouter("/")} />);

    expect(await screen.findByText("5 pesanan")).toBeDefined();
    expect(screen.queryByText(/Rp/)).toBeNull();
    expect(screen.getByText(/Dilihat sebagai jumlah pesanan/)).toBeDefined();
  });

  it("Superadmin melihat metrik dalam rupiah", async () => {
    stubDashboard({ peran: "superadmin" });
    render(<RouterProvider router={buatRouter("/")} />);

    await screen.findByText(/Dilihat sebagai nilai rupiah/);
    expect(screen.getAllByText(/Rp[\s\u00a0]?800\.000/).length).toBeGreaterThan(0);
  });

  it("chip tampil dengan angkanya dan membuka daftar terfilter saat diklik", async () => {
    const { panggilan } = stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);

    expect(await screen.findByText("3 menunggu verifikasi")).toBeDefined();
    expect(screen.getByLabelText("Sembunyikan: 1 perlu review")).toBeDefined();
    expect(screen.getByLabelText("Sembunyikan: 2 C&C lewat jatuh tempo")).toBeDefined();
    // Chip bernilai nol tidak ditampilkan (perilaku prototype).
    expect(screen.queryByLabelText("Sembunyikan: 0 email gagal")).toBeNull();

    fireEvent.click(screen.getByText("3 menunggu verifikasi"));
    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("/admin/orders?") && u.includes("verif=belum"))).toBe(true);
    });
  });

  it("chip peringatan lain memakai padanan filternya masing-masing", async () => {
    const { panggilan } = stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByText("2 C&C lewat jatuh tempo");

    fireEvent.click(screen.getByText("2 C&C lewat jatuh tempo"));
    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("terlambat=true"))).toBe(true);
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
    // Chip lain tetap ada.
    expect(screen.getByLabelText("Sembunyikan: 1 perlu review")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "Tampilkan semua chip" }));
    expect(await screen.findByLabelText("Sembunyikan: 3 menunggu verifikasi")).toBeDefined();
  });

  it("filter cabang & periode dikirim sebagai query string", async () => {
    const { panggilan } = stubDashboard();
    render(<RouterProvider router={buatRouter("/?cabang=b1&periode=hari")} />);
    await screen.findByText("5 pesanan");

    const panggil = panggilan.find((u) => u.includes("/admin/dashboard?"));
    expect(panggil).toBeDefined();
    expect(panggil).toContain("periode=hari");
    expect(panggil).toContain("cabang=b1");
  });

  it("mengubah periode memuat ulang metrik dengan periode baru", async () => {
    const { panggilan } = stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByText("5 pesanan");

    fireEvent.click(screen.getByRole("button", { name: "Minggu ini" }));
    await waitFor(() => {
      expect(panggilan.some((u) => u.includes("/admin/dashboard?periode=minggu"))).toBe(true);
    });
  });

  it("grafik SVG aksesibel: role img, label, dan 12 batang bulanan", async () => {
    stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByText("5 pesanan");

    const grafik = screen.getByRole("img", { name: /Tren 12 bulan/ });
    expect(grafik.getAttribute("aria-label")).toContain("Tren 12 bulan");
    // 12 batang bulanan; grafik harian bulan berjalan punya 9 batang.
    expect(grafik.querySelectorAll("rect").length).toBe(12);
    expect(grafik.getAttribute("viewBox")).toBeTruthy();
    const harian = screen.getByRole("img", { name: /Tren harian/ });
    expect(harian.querySelectorAll("rect").length).toBe(9);
  });

  it("peringkat cabang tampil berurutan dengan namanya", async () => {
    stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByText("5 pesanan");

    const baris = Array.from(document.querySelectorAll("ol > li"));
    expect(baris.length).toBe(2);
    expect(baris[0].textContent).toContain("1");
    expect(baris[0].textContent).toContain("Grahita Bandung");
    expect(baris[0].textContent).toContain("3 pesanan");
    expect(baris[1].textContent).toContain("2");
    expect(baris[1].textContent).toContain("Grahita Surabaya");
  });

  it("ringkasan status pembayaran menampilkan tiga keranjang", async () => {
    stubDashboard();
    render(<RouterProvider router={buatRouter("/")} />);
    await screen.findByText("5 pesanan");

    expect(screen.getAllByText("1 terverifikasi").length).toBeGreaterThan(0);
    expect(screen.getAllByText("3 menunggu").length).toBeGreaterThan(0);
    expect(screen.getAllByText("1 perlu review").length).toBeGreaterThan(0);
  });
});
