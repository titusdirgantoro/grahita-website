import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(status < 400 ? { data } : data), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const topik = [{
  id: "t1",
  ikon: "memesan",
  judul: "Cara memesan",
  ringkas: "Panduan awal.",
  langkah: ["Buka Pesanan.", "Kirim pesanan."],
}];

beforeEach(() => {
  document.cookie = "grahita_csrf=csrf-tutorial";
});

describe("Pengaturan tutorial portal cabang", () => {
  it("Superadmin memuat, mengubah, lalu menyimpan topik melalui API", async () => {
    const panggilan: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input);
      panggilan.push({ url, init });
      if (url.endsWith("/auth/me")) return json({ user: { id: "sa", kind: "admin", role: "superadmin", nama: "Rina", email: "rina@example.test" } });
      if (url.endsWith("/admin/settings/ocr-status")) return json({ ai_active: false, provider_ready: false });
      if (url.endsWith("/admin/settings")) return json({ biaya_packaging: 3000, manager_boleh_revisi: false, ai: { aktif: false } });
      if (url.endsWith("/admin/backup-runs")) return json({ terakhir: [], konfigurasi_api_siap: true, perlu_tindakan: false, tujuan: "uji", jadwal: "harian", retensi: { harian: 7, bulanan: 3, wal_hari: 7, bukti_tanpa_batas: true } });
      if (url.endsWith("/admin/tutorial") && (init?.method ?? "GET") === "GET") return json({ topik });
      if (url.endsWith("/admin/tutorial") && init?.method === "PUT") {
        const dikirim = JSON.parse(String(init.body));
        return json({ topik: dikirim.topik });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }));

    const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/pengaturan"] }) });
    render(<RouterProvider router={router} />);

    expect(await screen.findByText("Tutorial portal cabang")).toBeDefined();
    fireEvent.change(await screen.findByLabelText("Judul"), { target: { value: "Cara memesan terbaru" } });
    fireEvent.change(screen.getByLabelText("Langkah (satu per baris)"), { target: { value: "Langkah pertama\nLangkah kedua" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan tutorial" }));

    await waitFor(() => {
      const put = panggilan.find((p) => p.url.endsWith("/admin/tutorial") && p.init?.method === "PUT");
      expect(put).toBeDefined();
      expect(JSON.parse(String(put?.init?.body))).toEqual({ topik: [{
        ...topik[0],
        judul: "Cara memesan terbaru",
        langkah: ["Langkah pertama", "Langkah kedua"],
      }] });
      expect((put?.init?.headers as Record<string, string>)["X-CSRF-Token"]).toBe("csrf-tutorial");
    });
    expect(await screen.findByText("Tutorial tersimpan dan langsung tersedia di portal cabang.")).toBeDefined();
  });
});
