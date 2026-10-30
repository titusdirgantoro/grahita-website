import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(data: unknown) {
  return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("Reporting admin", () => {
  it("menampilkan instansi per baris dan mengekspor filter yang sedang dipakai", async () => {
    const panggilan: Array<{ url: string; method: string; body?: string }> = [];
    vi.stubGlobal("open", vi.fn());
    vi.stubGlobal("fetch", vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      panggilan.push({ url, method, body: String(init?.body ?? "") });
      if (url.endsWith("/auth/me")) return json({ user: { id: "a1", kind: "admin", role: "superadmin", nama: "Admin", email: "admin@example.test" } });
      if (url.endsWith("/admin/branches")) return json([{ id: "b1", nama: "Bandung" }]);
      if (url.includes("/admin/reports/order-lines")) return json({
        items: [
          { tanggal: "2026-10-27", nomor: "2026-10-001", versi: 0, cabang: "Bandung", line_no: 1, produk: "Bio Magnetik", nama_instansi: "TK A", serial_number: "20261027001", qty: 2, subtotal: 200000, status: "done" },
          { tanggal: "2026-10-27", nomor: "2026-10-001", versi: 0, cabang: "Bandung", line_no: 2, produk: "Bio Magnetik", nama_instansi: "SD B", serial_number: "20261027002", qty: 1, subtotal: 100000, status: "done" },
        ], total: 2, total_subtotal: 300000, halaman: 1, per_halaman: 25,
      });
      if (url.endsWith("/admin/reports/export") && method === "POST") return json({ id: "job-1", status: "selesai", jumlah_baris: 2 });
      if (url.endsWith("/admin/reports/export/job-1/download")) return json({ url: "https://example.test/laporan.xlsx" });
      throw new Error(`request tidak dikenal: ${method} ${url}`);
    }));

    const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/reporting"] }) });
    render(<RouterProvider router={router} />);

    expect(await screen.findByText("TK A")).toBeDefined();
    expect(screen.getByText("SD B")).toBeDefined();
    fireEvent.change(screen.getByLabelText("Cabang"), { target: { value: "b1" } });
    fireEvent.click(screen.getByRole("button", { name: "Terapkan filter" }));
    await waitFor(() => expect(panggilan.some((p) => p.url.includes("cabang=b1"))).toBe(true));

    fireEvent.click(screen.getByRole("button", { name: "Ekspor Excel" }));
    await waitFor(() => expect(panggilan.some((p) => p.method === "POST" && p.url.endsWith("/admin/reports/export"))).toBe(true));
    const ekspor = panggilan.find((p) => p.method === "POST" && p.url.endsWith("/admin/reports/export"));
    expect(JSON.parse(ekspor?.body ?? "{}").cabang).toEqual(["b1"]);
    fireEvent.click(await screen.findByRole("button", { name: "Unduh Excel" }));
    await waitFor(() => expect(window.open).toHaveBeenCalledWith("https://example.test/laporan.xlsx", "_blank", "noopener,noreferrer"));
  });

  it("memantau ekspor queued lalu proses sampai selesai sebelum mengizinkan unduh", async () => {
    const status: Array<"proses" | "selesai"> = ["proses", "selesai"];
    const panggilan: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input);
      panggilan.push(`${init?.method ?? "GET"} ${url}`);
      if (url.endsWith("/auth/me")) return json({ user: { id: "a1", kind: "admin", role: "superadmin", nama: "Admin", email: "admin@example.test" } });
      if (url.endsWith("/admin/branches")) return json([]);
      if (url.includes("/admin/reports/order-lines")) return json({ items: [], total: 0, total_subtotal: 0, halaman: 1, per_halaman: 25 });
      if (url.endsWith("/admin/reports/export") && init?.method === "POST") {
        return json({ id: "job-antre", status: "queued", jumlah_baris: 0 });
      }
      if (url.endsWith("/admin/reports/export/job-antre")) {
        const berikut = status.shift() ?? "selesai";
        return json({ id: "job-antre", status: berikut, jumlah_baris: berikut === "selesai" ? 7 : 0 });
      }
      throw new Error(`request tidak dikenal: ${init?.method ?? "GET"} ${url}`);
    }));

    const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/reporting"] }) });
    render(<RouterProvider router={router} />);
    await screen.findByText("Belum ada baris laporan untuk filter ini.");

    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: "Ekspor Excel" }));
    await act(async () => { await Promise.resolve(); });
    expect(screen.getByText("Ekspor sedang diproses. Berkas akan tersedia di sini.")).toBeDefined();
    expect(screen.queryByRole("button", { name: "Unduh Excel" })).toBeNull();

    await act(async () => { vi.advanceTimersByTime(2000); await Promise.resolve(); });
    expect(panggilan.filter((p) => p.endsWith("/admin/reports/export/job-antre"))).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Unduh Excel" })).toBeNull();

    await act(async () => { vi.advanceTimersByTime(2000); await Promise.resolve(); });
    expect(panggilan.filter((p) => p.endsWith("/admin/reports/export/job-antre"))).toHaveLength(2);
    expect(screen.getByText("7 baris siap diunduh.")).toBeDefined();
    expect(screen.getByRole("button", { name: "Unduh Excel" })).toBeDefined();
    vi.useRealTimers();
  });

  it("menampilkan galat ekspor, menahan unduh, lalu membuat pekerjaan baru saat dicoba lagi", async () => {
    let jumlahPost = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/auth/me")) return json({ user: { id: "a1", kind: "admin", role: "superadmin", nama: "Admin", email: "admin@example.test" } });
      if (url.endsWith("/admin/branches")) return json([]);
      if (url.includes("/admin/reports/order-lines")) return json({ items: [], total: 0, total_subtotal: 0, halaman: 1, per_halaman: 25 });
      if (url.endsWith("/admin/reports/export") && init?.method === "POST") {
        jumlahPost++;
        if (jumlahPost === 1) return json({ id: "job-gagal", status: "gagal", jumlah_baris: 0, pesan: "object storage sedang penuh" });
        return json({ id: "job-ulangan", status: "selesai", jumlah_baris: 11 });
      }
      throw new Error(`request tidak dikenal: ${init?.method ?? "GET"} ${url}`);
    }));

    const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/reporting"] }) });
    render(<RouterProvider router={router} />);
    await screen.findByText("Belum ada baris laporan untuk filter ini.");

    fireEvent.click(screen.getByRole("button", { name: "Ekspor Excel" }));
    expect(await screen.findByText("object storage sedang penuh")).toBeDefined();
    expect(screen.queryByRole("button", { name: "Unduh Excel" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Ekspor Excel" }));
    expect(await screen.findByText("11 baris siap diunduh.")).toBeDefined();
    expect(jumlahPost).toBe(2);
    expect(screen.getByRole("button", { name: "Unduh Excel" })).toBeDefined();
  });
});
