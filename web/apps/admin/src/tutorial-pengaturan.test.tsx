import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(data: unknown): Response {
  return new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
}

describe("Pengaturan Superadmin setelah panduan tunggal", () => {
  it("tidak menampilkan editor topik lama atau memanggil API tutorial", async () => {
    const panggilan: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: string) => {
      const url = String(input);
      panggilan.push(url);
      if (url.endsWith("/auth/me")) return json({ user: { id: "sa", kind: "admin", role: "superadmin", nama: "Rina", email: "rina@example.test" } });
      if (url.endsWith("/admin/settings/ocr-status")) return json({ ai_active: false, provider_ready: false });
      if (url.endsWith("/admin/settings")) return json({ biaya_packaging: 3000, manager_boleh_revisi: false, ai: { aktif: false } });
      if (url.endsWith("/admin/backup-runs")) return json({ terakhir: [], konfigurasi_api_siap: true, perlu_tindakan: false, tujuan: "uji", jadwal: "harian", retensi: { harian: 7, bulanan: 3, wal_hari: 7, bukti_tanpa_batas: true } });
      return json({});
    }));

    const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/pengaturan"] }) });
    render(<RouterProvider router={router} />);

    expect(await screen.findByRole("heading", { name: "Pengaturan" })).toBeDefined();
    expect(screen.queryByText("Tutorial portal cabang")).toBeNull();
    expect(screen.queryByRole("button", { name: "Simpan tutorial" })).toBeNull();
    expect(panggilan.some((url) => url.endsWith("/admin/tutorial"))).toBe(false);
  });
});
