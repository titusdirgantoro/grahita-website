import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(data: unknown) {
  return new Response(JSON.stringify({ data }), { status: 200, headers: { "Content-Type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

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
});
