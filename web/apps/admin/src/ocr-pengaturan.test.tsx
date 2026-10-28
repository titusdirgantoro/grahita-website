import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function renderPengaturan() {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/pengaturan"] }),
  });
  render(<RouterProvider router={router} />);
}

function stubFetch(providerReady: boolean | null, aiActive = false) {
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      calls.push({ url: input, init });
      if (input.endsWith("/auth/me")) {
        return json({
          data: {
            user: { id: "a1", kind: "admin", role: "superadmin", nama: "Rina", email: "rina@example.test" },
          },
        });
      }
      if (input.endsWith("/admin/settings/ocr-status")) {
        if (providerReady === null) return json({ error: { code: "UNAVAILABLE", message: "gagal" } }, 503);
        return json({ data: { ai_active: aiActive, provider_ready: providerReady } });
      }
      if (input.endsWith("/admin/settings") && (init?.method ?? "GET") === "GET") {
        return json({
          data: {
            biaya_packaging: 3000,
            token_hari: 14,
            reset_menit: 60,
            max_mb: 5,
            max_file: 5,
            digit_invoice: 2,
            manager_boleh_revisi: false,
            ai: { aktif: aiActive, min_keyakinan: 0.9, tol_kurang: 0, tol_lebih: 0, digit_rek: 4, mundur: 1 },
          },
        });
      }
      if (input.endsWith("/admin/settings") && init?.method === "PUT") {
        return json({ data: { values: {} } });
      }
      if (input.endsWith("/admin/backup-runs")) {
        return json({
          data: {
            terakhir: [], konfigurasi_api_siap: false, perlu_tindakan: true,
            tujuan: "", jadwal: "setiap hari 02:00 WIB",
            retensi: { harian: 30, bulanan: 12, wal_hari: 7, bukti_tanpa_batas: true },
          },
        });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return calls;
}

beforeEach(() => {
  document.cookie = "grahita_csrf=token-uji";
});

describe("Admin Portal — pilihan verifikasi OCR", () => {
  it("provider belum siap: manual tetap tersedia, AI tidak dapat diaktifkan", async () => {
    const calls = stubFetch(false);
    renderPengaturan();

    const toggle = await screen.findByRole("switch", { name: "Verifikasi otomatis dengan AI" });
    expect((toggle as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/AI belum tersedia karena provider belum dikonfigurasi/)).toBeDefined();
    expect(screen.queryByLabelText("Ambang keyakinan OCR (0–1)")).toBeNull();
    expect(calls.some((c) => c.url.endsWith("/admin/settings") && c.init?.method === "PUT")).toBe(false);
  });

  it("status provider gagal dibaca: pengaktifan AI ditahan sementara", async () => {
    stubFetch(null);
    renderPengaturan();

    const toggle = await screen.findByRole("switch", { name: "Verifikasi otomatis dengan AI" });
    expect((toggle as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Status provider AI belum dapat diperiksa/)).toBeDefined();
  });

  it("provider siap: Superadmin dapat memilih AI dan menyimpan parameter", async () => {
    const calls = stubFetch(true);
    renderPengaturan();

    const toggle = await screen.findByRole("switch", { name: "Verifikasi otomatis dengan AI" });
    expect((toggle as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(toggle);
    expect(screen.getByLabelText("Ambang keyakinan OCR (0–1)")).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "Simpan pengaturan" }));

    await waitFor(() => {
      const put = calls.find((c) => c.url.endsWith("/admin/settings") && c.init?.method === "PUT");
      expect(put).toBeDefined();
      expect(JSON.parse(String(put?.init?.body))).toEqual({
        values: {
          ai: { aktif: true, min_keyakinan: 0.9, tol_kurang: 0, tol_lebih: 0, digit_rek: 4, mundur: 1 },
        },
      });
    });
  });

  it("AI aktif tetapi provider hilang: Superadmin masih dapat mematikan AI", async () => {
    const calls = stubFetch(false, true);
    renderPengaturan();

    const toggle = await screen.findByRole("switch", { name: "Verifikasi otomatis dengan AI" });
    expect((toggle as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(toggle);
    fireEvent.click(screen.getByRole("button", { name: "Simpan pengaturan" }));

    await waitFor(() => {
      const put = calls.find((c) => c.url.endsWith("/admin/settings") && c.init?.method === "PUT");
      expect(JSON.parse(String(put?.init?.body)).values.ai.aktif).toBe(false);
    });
  });
});
