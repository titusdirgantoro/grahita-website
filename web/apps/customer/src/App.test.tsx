import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import App from "./App";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const meta = {
  app: "grahita-portal",
  version: "test",
  env: "local",
  customer_host: "grahita-branch.tiusclaw.com",
  admin_host: "grahita-admin.tiusclaw.com",
  waktu_wib: "2026-10-08T13:00:00+07:00",
};

describe("Customer Portal", () => {
  it("menampilkan host customer dari /api/v1/meta dan formulir masuk saat belum login", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/meta")) return json({ data: meta });
        return json({ error: { code: "UNAUTHENTICATED", message: "belum masuk" } }, 401);
      }),
    );

    render(<App />);

    await waitFor(() => {
      expect(screen.getByText(/grahita-branch\.tiusclaw\.com/)).toBeDefined();
    });
    expect(screen.getByRole("button", { name: "Masuk" })).toBeDefined();
    expect(screen.getByLabelText("Email cabang")).toBeDefined();
  });

  it("menampilkan katalog dengan harga cabang setelah login", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (input.endsWith("/meta")) return json({ data: meta });
        if (input.endsWith("/auth/me")) {
          return json({ data: { user: { id: "u1", kind: "branch", role: "branch", nama: "Grahita Bandung", email: "bandung@example.test" } } });
        }
        if (input.endsWith("/catalog")) {
          return json({ data: [{ id: "p1", nama: "Bio Magnetik", jenis: "reguler", satuan: "pcs", harga: 340000 }] });
        }
        return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
      }),
    );

    render(<App />);

    await waitFor(() => {
      expect(screen.getByText("Bio Magnetik")).toBeDefined();
    });
    expect(screen.getByText(/Grahita Bandung/)).toBeDefined();
    expect(screen.getByText(/Rp\s?340\.000/)).toBeDefined();
  });
});
