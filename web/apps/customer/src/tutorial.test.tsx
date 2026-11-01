import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}

describe("Tutorial portal cabang", () => {
  it("menampilkan satu panduan lengkap tanpa topik lama dan tanpa memanggil API topik", async () => {
    const panggilan: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string) => {
      const url = String(input);
      panggilan.push(url);
      if (url.endsWith("/auth/me")) return json({ data: { user: {
        id: "u1", kind: "branch", role: "branch", nama: "Grahita Bandung",
        email: "bandung@example.test", branch_id: "b1",
      } } });
      return json({ data: {} });
    }));

    const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/tutorial"] }) });
    render(<RouterProvider router={router} />);

    expect(await screen.findByRole("heading", { name: "Tutorial" })).toBeDefined();
    expect(screen.getByRole("heading", { name: "Alur penggunaan dari awal sampai selesai" })).toBeDefined();
    expect(screen.getByRole("heading", { name: "Buat pesanan per instansi dan produk" })).toBeDefined();
    expect(screen.getByRole("heading", { name: "Jika memilih Transfer di muka" })).toBeDefined();
    expect(screen.getByRole("heading", { name: "Jika memilih Deposit (Cash & Carry)" })).toBeDefined();
    expect(screen.getByRole("heading", { name: "Jika memilih Tempo dan melunasi tagihan" })).toBeDefined();
    expect(screen.getByRole("heading", { name: "Pantau status dan unduh dokumen" })).toBeDefined();
    expect(screen.getByRole("navigation", { name: "Daftar isi tutorial" })).toBeDefined();
    expect(screen.getAllByRole("img")).toHaveLength(8);
    expect(screen.getAllByRole("link", { name: "Buka gambar ukuran penuh" })).toHaveLength(8);
    expect(screen.queryByRole("heading", { name: "Panduan tambahan dari Pusat" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Cara memesan" })).toBeNull();
    expect(panggilan.some((url) => url.endsWith("/tutorial"))).toBe(false);
  });
});
