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

const CABANG = [
  {
    branch_id: "br-a",
    user_id: "usr-a",
    nama: "Grahita Bandung",
    nama_kepala: "Sri Wahyuni",
    email: "bandung@example.test",
    telepon: "+628110000001",
    has_account: true,
    is_active: true,
    branch_active: true,
    status: "aktif",
    last_login_at: "2026-10-07T03:04:05Z",
  },
  {
    branch_id: "br-b",
    nama: "Grahita Bekasi",
    nama_kepala: "Agus Pratama",
    email: "bekasi@example.test",
    has_account: false,
    is_active: false,
    branch_active: true,
    status: "belum_registrasi",
    last_login_at: null,
    token_prefix: "GRH-7K4Q",
  },
  {
    branch_id: "br-c",
    user_id: "usr-c",
    nama: "Grahita Cimahi",
    nama_kepala: "Hendra",
    email: "cimahi@example.test",
    has_account: true,
    is_active: false,
    branch_active: true,
    status: "nonaktif",
    last_login_at: null,
  },
];

const ADMINS = [
  {
    id: "adm-sa",
    nama: "Rina Kartika",
    email: "rina@example.test",
    role: "superadmin",
    notif_invoice: true,
    notif_surat_jalan: true,
    is_active: true,
    status: "aktif",
  },
  {
    id: "adm-undang",
    nama: "Dewi Lestari",
    email: "dewi@example.test",
    role: "manager",
    notif_invoice: false,
    notif_surat_jalan: false,
    is_active: true,
    status: "undangan",
  },
];

interface Panggilan {
  url: string;
  init?: RequestInit;
}

function buatRouter(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

/**
 * stubFetch memasang fetch tiruan: sesi admin + daftar akun cabang/admin.
 * `tambahan` bisa menimpa respons tertentu (mis. menirukan 409).
 */
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
      if (input.endsWith("/admin/users/branch")) return json({ data: CABANG });
      if (input.endsWith("/admin/users/admin")) return json({ data: ADMINS });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

beforeEach(() => {
  // Cookie CSRF double-submit dibaca klien untuk setiap mutasi.
  document.cookie = "grahita_csrf=token-uji";
});

describe("Admin Portal — /pengguna (F1-05)", () => {
  it("Manager melihat akun cabang beserta status dan login terakhir, tanpa tab User admin", async () => {
    const panggilan = stubFetch("manager");

    render(<RouterProvider router={buatRouter("/pengguna")} />);

    expect(await screen.findByText("Grahita Bandung")).toBeDefined();
    expect(screen.getByText("Belum registrasi")).toBeDefined();
    expect(screen.getByText("Nonaktif")).toBeDefined();
    expect(screen.getByText("+628110000001")).toBeDefined();
    // Login terakhir ditampilkan dalam zona WIB (2026-10-07T03:04Z → 10:04 WIB).
    expect(screen.getByText("7 Okt 2026, 10.04")).toBeDefined();

    // Manager tidak mendapat tab maupun tombol khusus Superadmin.
    expect(screen.queryByRole("button", { name: /User admin/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Undang admin/ })).toBeNull();
    expect(panggilan.some((p) => p.url.endsWith("/admin/users/admin"))).toBe(false);
  });

  it("Superadmin dapat berpindah ke tab User admin dan mengundang admin baru", async () => {
    const panggilan = stubFetch("superadmin");

    render(<RouterProvider router={buatRouter("/pengguna")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "User admin" }));
    expect(await screen.findByText("Rina Kartika")).toBeDefined();
    expect(screen.getByText("Undangan terkirim")).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: /Undang admin/ }));
    expect(await screen.findByLabelText("Nama pengguna")).toBeDefined();

    fireEvent.change(screen.getByLabelText("Nama pengguna"), { target: { value: "Dewi Lestari" } });
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "dewi@example.test" } });
    fireEvent.click(screen.getByRole("button", { name: "Kirim undangan" }));

    await waitFor(() => {
      const kirim = panggilan.find(
        (p) => p.url.endsWith("/admin/users/admin") && p.init?.method === "POST",
      );
      expect(kirim).toBeDefined();
      const body = JSON.parse(String(kirim?.init?.body));
      expect(body.email).toBe("dewi@example.test");
      expect(body.role).toBe("manager");
      const header = kirim?.init?.headers as Record<string, string>;
      expect(header["X-CSRF-Token"]).toBe("token-uji");
    });

    // Daftar dimuat ulang setelah undangan terkirim.
    await waitFor(() =>
      expect(panggilan.filter((p) => p.url.endsWith("/admin/users/admin")).length).toBeGreaterThan(1),
    );
  });

  it("Menonaktifkan akun cabang mengirim PUT is_active=false", async () => {
    const panggilan = stubFetch("manager");

    render(<RouterProvider router={buatRouter("/pengguna")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();

    fireEvent.click(screen.getAllByRole("button", { name: "Nonaktifkan" })[0]);

    await waitFor(() => {
      const put = panggilan.find(
        (p) => p.url.endsWith("/admin/users/branch/usr-a") && p.init?.method === "PUT",
      );
      expect(put).toBeDefined();
      expect(JSON.parse(String(put?.init?.body))).toEqual({ is_active: false });
    });
  });

  it("Galat LAST_SUPERADMIN dari server ditampilkan di modal ubah admin", async () => {
    stubFetch("superadmin", (url, init) => {
      if (url.endsWith("/admin/users/admin/adm-sa") && init?.method === "PUT") {
        return json(
          {
            error: {
              code: "LAST_SUPERADMIN",
              message:
                "Superadmin aktif terakhir tidak boleh dinonaktifkan atau diturunkan menjadi Manager.",
            },
          },
          409,
        );
      }
      return undefined;
    });

    render(<RouterProvider router={buatRouter("/pengguna")} />);
    expect(await screen.findByText("Grahita Bandung")).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "User admin" }));
    expect(await screen.findByText("Rina Kartika")).toBeDefined();

    fireEvent.click(screen.getAllByRole("button", { name: "Ubah" })[0]);
    expect(await screen.findByText("Ubah admin")).toBeDefined();

    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "manager" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    expect(await screen.findByText(/Superadmin aktif terakhir tidak boleh/)).toBeDefined();
  });
});
