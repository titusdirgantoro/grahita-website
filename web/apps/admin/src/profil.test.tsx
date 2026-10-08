import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function buatRouter(path: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
}

interface Panggilan {
  url: string;
  init?: RequestInit;
}

/**
 * fetchAdminProfil memasang fetch palsu untuk portal admin: sesi, pembaruan
 * profil (PUT /auth/me), dan endpoint kata sandi. Semua panggilan dicatat.
 */
function fetchAdminProfil() {
  const panggilan: Panggilan[] = [];
  const pengguna = {
    id: "adm-mgr",
    kind: "admin",
    role: "manager",
    nama: "Budi",
    email: "budi@example.test",
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      if (input.endsWith("/auth/me")) {
        if (init?.method === "PUT") {
          const body = JSON.parse(String(init.body)) as { nama: string };
          pengguna.nama = body.nama;
          return json({ data: { user: pengguna } });
        }
        return json({ data: { user: pengguna } });
      }
      if (input.endsWith("/auth/password/change")) return new Response(null, { status: 204 });
      if (input.endsWith("/auth/password/forgot")) {
        return json({ data: { pesan: "Jika email terdaftar, tautan sudah dikirim." } });
      }
      if (input.endsWith("/auth/password/reset")) return new Response(null, { status: 204 });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

describe("Admin Portal — Profil (F1-10)", () => {
  it("nama bisa diubah sendiri; email dan role read-only", async () => {
    const panggilan = fetchAdminProfil();

    render(<RouterProvider router={buatRouter("/profil")} />);

    const nama = (await screen.findByLabelText("Nama")) as HTMLInputElement;
    expect(nama.value).toBe("Budi");
    expect(nama.readOnly).toBe(false);

    const email = screen.getByLabelText("Email (read-only)") as HTMLInputElement;
    expect(email.value).toBe("budi@example.test");
    expect(email.readOnly).toBe(true);

    const peran = screen.getByLabelText("Role (read-only)") as HTMLInputElement;
    expect(peran.value).toBe("Manager");
    expect(peran.readOnly).toBe(true);

    fireEvent.change(nama, { target: { value: "Budi Santoso" } });
    fireEvent.click(screen.getByRole("button", { name: "Simpan" }));

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.url.endsWith("/auth/me") && p.init?.method === "PUT");
      expect(kirim).toBeDefined();
      expect(JSON.parse(String(kirim?.init?.body))).toEqual({ nama: "Budi Santoso" });
    });
    expect(await screen.findByText("Nama profil tersimpan.")).toBeDefined();
    // Shell memakai nama baru tanpa memuat ulang halaman.
    const sidebar = screen.getByRole("complementary");
    expect(within(sidebar).getByText("Budi Santoso")).toBeDefined();
  });

  it("ganti password admin memakai endpoint yang sama dengan portal cabang", async () => {
    const panggilan = fetchAdminProfil();

    render(<RouterProvider router={buatRouter("/profil")} />);
    await screen.findByLabelText("Nama");

    fireEvent.change(screen.getByLabelText("Kata sandi lama"), { target: { value: "rahasia123" } });
    fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: "sandibaru123" } });
    fireEvent.change(screen.getByLabelText("Konfirmasi kata sandi baru"), {
      target: { value: "sandibaru123" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.url.endsWith("/auth/password/change"));
      expect(JSON.parse(String(kirim?.init?.body))).toEqual({
        password_lama: "rahasia123",
        password_baru: "sandibaru123",
      });
    });
    expect(
      await screen.findByText(
        "Kata sandi berhasil diganti. Sesi di perangkat lain sudah diakhiri.",
      ),
    ).toBeDefined();
  });
});

describe("Admin Portal — /lupa (F1-10)", () => {
  it("tanpa token: mengirim permintaan tautan dari host admin", async () => {
    const panggilan = fetchAdminProfil();

    render(<RouterProvider router={buatRouter("/lupa")} />);

    fireEvent.change(await screen.findByLabelText("Email"), {
      target: { value: "budi@example.test" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Kirim tautan atur ulang" }));

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.url.endsWith("/auth/password/forgot"));
      expect(JSON.parse(String(kirim?.init?.body))).toEqual({ email: "budi@example.test" });
    });
  });

  it("dengan ?token= dari tautan email: menyimpan kata sandi baru", async () => {
    const panggilan = fetchAdminProfil();

    render(<RouterProvider router={buatRouter("/lupa?token=GRH-ADMIN-TOKEN")} />);

    expect(await screen.findByText("Buat kata sandi baru")).toBeDefined();
    fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: "sandibaru123" } });
    fireEvent.change(screen.getByLabelText("Konfirmasi kata sandi baru"), {
      target: { value: "sandibaru123" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.url.endsWith("/auth/password/reset"));
      expect(JSON.parse(String(kirim?.init?.body))).toEqual({
        token: "GRH-ADMIN-TOKEN",
        password: "sandibaru123",
      });
    });
    expect(
      await screen.findByText(
        "Kata sandi baru tersimpan. Sesi lama sudah diakhiri — silakan masuk kembali.",
      ),
    ).toBeDefined();
  });
});
