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

interface Panggilan {
  url: string;
  init?: RequestInit;
}

const REKENING = [
  { id: "bk-1", bank: "BCA", nama_pemilik: "PT Grahita Indonesia", nomor: "1234567890", is_active: true },
  { id: "bk-2", bank: "BRI", nama_pemilik: "PT Grahita Indonesia", nomor: "0987654321", is_active: false },
];

const PENGATURAN = {
  biaya_packaging: 3000,
  cnc_hari: 7,
  token_hari: 14,
  reset_menit: 60,
  max_mb: 5,
  max_file: 5,
  digit_invoice: 2,
  manager_boleh_revisi: true,
  ai: { aktif: false, min_keyakinan: 0.9, tol_kurang: 0, tol_lebih: 0, digit_rek: 4, mundur: 1 },
};

function buatRouter(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

function stubFetch(
  role: string,
  tambahan?: (url: string, init?: RequestInit) => Response | undefined,
): Panggilan[] {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      const timpa = tambahan?.(input, init);
      if (timpa) return timpa;
      if (input.endsWith("/auth/me")) {
        return json({
          data: { user: { id: "a1", kind: "admin", role, nama: "Rina Kartika", email: "rina@example.test" } },
        });
      }
      if (input.endsWith("/admin/bank-accounts")) return json({ data: REKENING });
      if (input.endsWith("/admin/settings") && (init?.method ?? "GET") === "GET") {
        return json({ data: PENGATURAN });
      }
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

beforeEach(() => {
  document.cookie = "grahita_csrf=token-uji";
});

describe("Admin Portal — /rekening (F1-09)", () => {
  it("Superadmin melihat rekening dan menonaktifkannya lewat PUT is_active", async () => {
    const panggilan = stubFetch("superadmin");

    render(<RouterProvider router={buatRouter("/rekening")} />);

    expect(await screen.findByText("1234567890")).toBeDefined();
    expect(screen.getByText("0987654321")).toBeDefined();
    expect(screen.getByText("Nonaktif")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "Nonaktifkan" }));

    await waitFor(() => {
      const put = panggilan.find((p) => p.url.endsWith("/admin/bank-accounts/bk-1") && p.init?.method === "PUT");
      expect(put).toBeDefined();
      expect(JSON.parse(String(put?.init?.body))).toEqual({ is_active: false });
      const header = put?.init?.headers as Record<string, string>;
      expect(header["X-CSRF-Token"]).toBe("token-uji");
    });
  });

  it("Menambah rekening mengirim POST dengan field lengkap", async () => {
    const panggilan = stubFetch("superadmin");

    render(<RouterProvider router={buatRouter("/rekening")} />);
    expect(await screen.findByText("1234567890")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: /Tambah rekening/ }));
    expect(await screen.findByLabelText("Bank")).toBeDefined();

    fireEvent.change(screen.getByLabelText("Bank"), { target: { value: "BRI" } });
    fireEvent.change(screen.getByLabelText("Nama di rekening"), { target: { value: "PT Grahita Baru" } });
    fireEvent.change(screen.getByLabelText("Nomor rekening"), { target: { value: "555666777" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    await waitFor(() => {
      const post = panggilan.find((p) => p.url.endsWith("/admin/bank-accounts") && p.init?.method === "POST");
      expect(post).toBeDefined();
      expect(JSON.parse(String(post?.init?.body))).toEqual({
        bank: "BRI",
        nama_pemilik: "PT Grahita Baru",
        nomor: "555666777",
        is_active: true,
      });
    });
  });

  it("Galat BANK_DUPLICATE dari server ditampilkan di modal", async () => {
    stubFetch("superadmin", (url, init) => {
      if (url.endsWith("/admin/bank-accounts") && init?.method === "POST") {
        return json(
          { error: { code: "BANK_DUPLICATE", message: "Rekening dengan bank dan nomor tersebut sudah terdaftar." } },
          409,
        );
      }
      return undefined;
    });

    render(<RouterProvider router={buatRouter("/rekening")} />);
    expect(await screen.findByText("1234567890")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: /Tambah rekening/ }));
    fireEvent.change(screen.getByLabelText("Nama di rekening"), { target: { value: "PT Grahita" } });
    fireEvent.change(screen.getByLabelText("Nomor rekening"), { target: { value: "1234567890" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    expect(await screen.findByText(/sudah terdaftar/)).toBeDefined();
  });

  it("Manager ditolak oleh penjaga rute dan tidak memanggil API rekening", async () => {
    const panggilan = stubFetch("manager");

    render(<RouterProvider router={buatRouter("/rekening")} />);

    expect(await screen.findByText("Tidak berwenang")).toBeDefined();
    expect(panggilan.some((p) => p.url.endsWith("/admin/bank-accounts"))).toBe(false);
  });
});

describe("Admin Portal — /pengaturan (F1-09)", () => {
  it("Superadmin melihat nilai pengaturan dan menyimpan perubahan biaya packaging", async () => {
    const panggilan = stubFetch("superadmin");

    render(<RouterProvider router={buatRouter("/pengaturan")} />);

    expect(await screen.findByText("Nilai tersimpan")).toBeDefined();
    expect(await screen.findByText("biaya_packaging")).toBeDefined();

    const input = screen.getByLabelText("Biaya packaging (Rp)") as HTMLInputElement;
    expect(input.value).toBe("3000");
    fireEvent.change(input, { target: { value: "4000" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan pengaturan" }));

    await waitFor(() => {
      const put = panggilan.find((p) => p.url.endsWith("/admin/settings") && p.init?.method === "PUT");
      expect(put).toBeDefined();
      expect(JSON.parse(String(put?.init?.body))).toEqual({ values: { biaya_packaging: 4000 } });
    });
  });

  it("Galat validasi rentang ditampilkan pada field yang bersangkutan", async () => {
    stubFetch("superadmin", (url, init) => {
      if (url.endsWith("/admin/settings") && init?.method === "PUT") {
        return json(
          {
            error: {
              code: "VALIDATION_FAILED",
              message: "Nilai pengaturan tidak valid.",
              fields: { biaya_packaging: "harus di antara 0 dan 100000000" },
            },
          },
          400,
        );
      }
      return undefined;
    });

    render(<RouterProvider router={buatRouter("/pengaturan")} />);

    const input = await screen.findByLabelText("Biaya packaging (Rp)");
    fireEvent.change(input, { target: { value: "4000" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan pengaturan" }));

    expect(await screen.findByText("harus di antara 0 dan 100000000")).toBeDefined();
  });

  it("Manager ditolak dan tidak membaca pengaturan lengkap", async () => {
    const panggilan = stubFetch("manager");

    render(<RouterProvider router={buatRouter("/pengaturan")} />);

    expect(await screen.findByText("Tidak berwenang")).toBeDefined();
    expect(panggilan.some((p) => p.url.endsWith("/admin/settings"))).toBe(false);
  });
});
