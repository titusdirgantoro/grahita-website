import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import App from "./App";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("Admin Portal", () => {
  it("menampilkan formulir masuk saat belum login", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        json({ error: { code: "UNAUTHENTICATED", message: "belum masuk" } }, 401),
      ),
    );

    render(<App />);

    await waitFor(() => {
      expect(screen.getByText("Masuk ke Admin Portal")).toBeDefined();
    });
    expect(screen.getByLabelText("Email")).toBeDefined();
  });

  it("Superadmin melihat master cabang dan pengaturan sistem", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/auth/me")) {
          return json({
            data: { user: { id: "a1", kind: "admin", role: "superadmin", nama: "Rina", email: "rina@example.test" } },
          });
        }
        if (input.endsWith("/admin/branches")) {
          return json({
            data: [
              { id: "b1", nama: "Grahita Bandung", nama_kepala: "Sri", email: "bandung@example.test", is_cnc: true, is_active: true },
            ],
          });
        }
        if (input.endsWith("/admin/settings")) {
          return json({ data: { biaya_packaging: 3000, cnc_hari: 7 } });
        }
        return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
      }),
    );

    render(<App />);

    await waitFor(() => {
      expect(screen.getByText("Grahita Bandung")).toBeDefined();
    });
    expect(screen.getByText(/· Superadmin/)).toBeDefined();
    expect(screen.getByText("Pengaturan sistem (Superadmin)")).toBeDefined();
    await waitFor(() => {
      expect(screen.getByText(/2 kunci pengaturan dimuat/)).toBeDefined();
    });
  });

  it("Manager tidak melihat blok pengaturan Superadmin", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/auth/me")) {
          return json({
            data: { user: { id: "a2", kind: "admin", role: "manager", nama: "Budi", email: "budi@example.test" } },
          });
        }
        if (input.endsWith("/admin/branches")) {
          return json({ data: [] });
        }
        return json({ error: { code: "FORBIDDEN", message: "tidak berhak" } }, 403);
      }),
    );

    render(<App />);

    await waitFor(() => {
      expect(screen.getByText(/Belum ada data cabang/)).toBeDefined();
    });
    expect(screen.queryByText(/Pengaturan sistem \(Superadmin\)/)).toBeNull();
  });
});
