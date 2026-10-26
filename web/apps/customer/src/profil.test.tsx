import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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

const penggunaCabang = {
  id: "u1",
  kind: "branch",
  role: "branch",
  nama: "Grahita Bandung",
  email: "bandung@example.test",
  branch_id: "b1",
};

const cabangBandung = {
  id: "b1",
  nama: "Grahita Bandung",
  nama_kepala: "Sri Wahyuni",
  email: "bandung@example.test",
  telepon: "081234567890",
  is_cnc: true,
  is_tempo: false,
  pajak_in: false,
  is_active: true,
};

interface Panggilan {
  url: string;
  init?: RequestInit;
}

/**
 * fetchPortal memasang fetch palsu untuk portal cabang: sesi, profil cabang
 * (/me/branch), dan seluruh endpoint kata sandi. Setiap panggilan dicatat agar
 * tes dapat memeriksa URL, metode, dan body yang benar-benar dikirim UI.
 */
function fetchPortal(opsi: { galatChange?: Response } = {}) {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      panggilan.push({ url: input, init });
      if (input.endsWith("/auth/me")) return json({ data: { user: penggunaCabang } });
      if (input.endsWith("/me/branch")) return json({ data: cabangBandung });
      if (input.endsWith("/auth/password/change")) {
        if (opsi.galatChange) return opsi.galatChange;
        return new Response(null, { status: 204 });
      }
      if (input.endsWith("/auth/password/forgot")) {
        return json({ data: { pesan: "Jika email terdaftar, tautan sudah dikirim." } });
      }
      if (input.endsWith("/auth/password/reset")) return new Response(null, { status: 204 });
      return json({ error: { code: "NOT_FOUND", message: "tidak ada" } }, 404);
    }),
  );
  return panggilan;
}

function isiGantiPassword(lama: string, baru: string, konfirmasi: string) {
  fireEvent.change(screen.getByLabelText("Kata sandi lama"), { target: { value: lama } });
  fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: baru } });
  fireEvent.change(screen.getByLabelText("Konfirmasi kata sandi baru"), {
    target: { value: konfirmasi },
  });
  fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));
}

describe("Customer Portal — Profil cabang (F1-10)", () => {
  it("memuat data cabang dari /me/branch dan tidak ada field yang bisa diedit", async () => {
    const panggilan = fetchPortal();

    render(<RouterProvider router={buatRouter("/profil")} />);

    const nama = (await screen.findByLabelText("Nama cabang")) as HTMLInputElement;
    expect(nama.value).toBe("Grahita Bandung");
    expect(nama.readOnly).toBe(true);

    const kepala = screen.getByLabelText("Nama kepala cabang") as HTMLInputElement;
    expect(kepala.value).toBe("Sri Wahyuni");
    expect(kepala.readOnly).toBe(true);

    const email = screen.getByLabelText("Email cabang") as HTMLInputElement;
    expect(email.value).toBe("bandung@example.test");
    expect(email.readOnly).toBe(true);

    const telepon = screen.getByLabelText("Telepon") as HTMLInputElement;
    expect(telepon.value).toBe("081234567890");
    expect(telepon.readOnly).toBe(true);

    // Data cabang dibaca dari /me/branch (halaman + menu shell); tidak ada
    // request tulis.
    expect(panggilan.some((p) => p.url.endsWith("/me/branch"))).toBe(true);
    expect(panggilan.every((p) => !p.init || (p.init.method ?? "GET") === "GET")).toBe(true);
    // Semua panggilan RELATIF (/api/v1/...) supaya cookie sesi tetap host-only
    // pada host portal yang sedang dibuka — tidak ada URL absolut antar portal.
    expect(panggilan.every((p) => p.url.startsWith("/api/v1"))).toBe(true);
  });

  it("mengirim ganti password dan melaporkan sesi lain diakhiri", async () => {
    const panggilan = fetchPortal();

    render(<RouterProvider router={buatRouter("/profil")} />);
    await screen.findByLabelText("Nama cabang");

    isiGantiPassword("rahasia123", "sandibaru123", "sandibaru123");

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.url.endsWith("/auth/password/change"));
      expect(kirim).toBeDefined();
      expect(kirim?.init?.method).toBe("POST");
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

  it("menampilkan galat field dari server saat kata sandi lama salah", async () => {
    fetchPortal({
      galatChange: json(
        {
          error: {
            code: "VALIDATION_FAILED",
            message: "Kata sandi lama tidak cocok.",
            fields: { password_lama: "kata sandi lama salah" },
          },
        },
        400,
      ),
    });

    render(<RouterProvider router={buatRouter("/profil")} />);
    await screen.findByLabelText("Nama cabang");

    isiGantiPassword("salahsekali", "sandibaru123", "sandibaru123");

    expect(await screen.findByText("kata sandi lama salah")).toBeDefined();
  });
});

describe("Customer Portal — /lupa (F1-10)", () => {
  it("tanpa token: mengirim permintaan tautan ke API", async () => {
    const panggilan = fetchPortal();

    render(<RouterProvider router={buatRouter("/lupa")} />);

    fireEvent.change(await screen.findByLabelText("Email"), {
      target: { value: "bandung@example.test" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Kirim tautan atur ulang" }));

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.url.endsWith("/auth/password/forgot"));
      expect(JSON.parse(String(kirim?.init?.body))).toEqual({ email: "bandung@example.test" });
    });
    expect(
      await screen.findByText(
        "Bila email terdaftar, tautan atur ulang sudah dikirim. Periksa kotak masuk Anda.",
      ),
    ).toBeDefined();
  });

  it("dengan ?token= dari tautan email: menyimpan kata sandi baru", async () => {
    const panggilan = fetchPortal();

    render(<RouterProvider router={buatRouter("/lupa?token=GRH-TOKEN-UJI")} />);

    expect(await screen.findByText("Buat kata sandi baru")).toBeDefined();
    fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: "sandibaru123" } });
    fireEvent.change(screen.getByLabelText("Konfirmasi kata sandi baru"), {
      target: { value: "sandibaru123" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));

    await waitFor(() => {
      const kirim = panggilan.find((p) => p.url.endsWith("/auth/password/reset"));
      expect(JSON.parse(String(kirim?.init?.body))).toEqual({
        token: "GRH-TOKEN-UJI",
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
