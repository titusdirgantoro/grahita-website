import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "./routes";

/**
 * Uji halaman /daftar (kartu F1-07b, PRD §3.1 CP-REG-02..07).
 *
 * Semua respons API di-stub sehingga kontrak yang diuji di sini adalah yang
 * memang dipakai backend F1-07: langkah 1 POST /auth/registration/validate,
 * langkah 2 POST /auth/register, dan bentuk error
 * `{error:{code,message,fields?}}`.
 */

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function galat(code: string, message: string, status: number, fields?: Record<string, string>): Response {
  return json({ error: { code, message, ...(fields ? { fields } : {}) } }, status);
}

function belumMasuk(): Response {
  return galat("UNAUTHENTICATED", "belum masuk", 401);
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

/** pasangFetch memasang fetch palsu yang mencatat setiap permintaan. */
function pasangFetch(tangani: (url: string, init?: RequestInit) => Response | Promise<Response>) {
  const panggilan: Panggilan[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input);
      panggilan.push({ url, init });
      if (url.endsWith("/auth/me")) return belumMasuk();
      return tangani(url, init);
    }),
  );
  return panggilan;
}

function badan(init?: RequestInit): Record<string, unknown> {
  return JSON.parse(String(init?.body)) as Record<string, unknown>;
}

function panggilanKe(panggilan: Panggilan[], akhir: string): Panggilan | undefined {
  return panggilan.find((p) => p.url.endsWith(akhir));
}

const DATA_CABANG = {
  nama_cabang: "Grahita Cimahi",
  nama_kepala: "Drs. Hendra Gunawan",
  email: "cimahi@grahita.test",
};

const TOKEN = "GRH-7K4Q-M2PX-W9TB-3HND";

function isiFormulir(telepon: string, sandi: string, konfirmasi: string) {
  fireEvent.change(screen.getByLabelText("Nomor telepon"), { target: { value: telepon } });
  fireEvent.change(screen.getByLabelText("Kata sandi"), { target: { value: sandi } });
  fireEvent.change(screen.getByLabelText("Konfirmasi kata sandi"), { target: { value: konfirmasi } });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("/daftar — alur dua langkah (CP-REG-02..05)", () => {
  it("memvalidasi token, menampilkan data cabang read-only dari backend, lalu mendaftar dan mengarahkan ke /masuk", async () => {
    const panggilan = pasangFetch((url) => {
      if (url.endsWith("/auth/registration/validate")) return json({ data: DATA_CABANG });
      if (url.endsWith("/auth/register")) return json({ data: { user: { id: "u1", email: DATA_CABANG.email } } }, 201);
      return galat("NOT_FOUND", "tidak ada", 404);
    });

    render(<RouterProvider router={buatRouter("/daftar")} />);

    // Langkah 1: token mentah dikirim apa adanya ke backend (normalisasi/hash
    // adalah urusan backend F1-07); endpoint publik tidak butuh header CSRF.
    await screen.findByLabelText("Token registrasi");
    fireEvent.change(screen.getByLabelText("Token registrasi"), { target: { value: ` ${TOKEN.toLowerCase()} ` } });
    fireEvent.click(screen.getByRole("button", { name: "Lanjutkan" }));

    const validasi = await waitFor(() => {
      const p = panggilanKe(panggilan, "/auth/registration/validate");
      expect(p).toBeDefined();
      return p as Panggilan;
    });
    expect(badan(validasi.init)).toEqual({ token: TOKEN });
    expect(validasi.init?.method).toBe("POST");
    expect((validasi.init?.headers as Record<string, string>)["X-CSRF-Token"]).toBeUndefined();

    // Langkah 2: nilai tampilan HANYA berasal dari respons backend.
    const nama = (await screen.findByLabelText("Nama cabang")) as HTMLInputElement;
    const kepala = screen.getByLabelText("Nama kepala cabang") as HTMLInputElement;
    const email = screen.getByLabelText("Email cabang") as HTMLInputElement;
    expect(nama.value).toBe(DATA_CABANG.nama_cabang);
    expect(kepala.value).toBe(DATA_CABANG.nama_kepala);
    expect(email.value).toBe(DATA_CABANG.email);
    for (const kontrol of [nama, kepala, email]) {
      expect(kontrol.readOnly).toBe(true);
      // Field read-only tidak diberi `name` sehingga tidak bisa ikut terkirim.
      expect(kontrol.getAttribute("name")).toBeNull();
    }

    // Indikator kekuatan (CP-REG-05): baru tampil setelah pengguna mengetik.
    expect(screen.queryByRole("meter")).toBeNull();
    fireEvent.change(screen.getByLabelText("Kata sandi"), { target: { value: "pen1" } });
    const meter = screen.getByRole("meter", { name: "Kekuatan kata sandi" });
    expect(meter.getAttribute("aria-valuenow")).toBe("0");
    expect(screen.getByText("Sangat lemah")).toBeDefined();

    fireEvent.change(screen.getByLabelText("Kata sandi"), { target: { value: "Abcdefg1!" } });
    expect(screen.getByRole("meter").getAttribute("aria-valuenow")).toBe("4");
    expect(screen.getByText("Sangat kuat")).toBeDefined();

    // Konfirmasi tidak sama: ditolak di klien, API tidak dipanggil.
    isiFormulir("08 12-3456-7890", "Abcdefg1!", "Beda123456");
    fireEvent.click(screen.getByRole("button", { name: "Buat akun" }));
    expect(await screen.findByText("Konfirmasi kata sandi belum sama.")).toBeDefined();
    expect(panggilanKe(panggilan, "/auth/register")).toBeUndefined();

    // Konfirmasi cocok -> register.
    fireEvent.change(screen.getByLabelText("Konfirmasi kata sandi"), { target: { value: "Abcdefg1!" } });
    fireEvent.click(screen.getByRole("button", { name: "Buat akun" }));

    const daftar = await waitFor(() => {
      const p = panggilanKe(panggilan, "/auth/register");
      expect(p).toBeDefined();
      return p as Panggilan;
    });
    // Payload UI tidak dapat mengubah identitas cabang/email: hanya empat field
    // yang diizinkan OpenAPI final; nama_cabang/email/branch_id tidak ada.
    expect(badan(daftar.init)).toEqual({
      token: TOKEN,
      telepon: "08 12-3456-7890",
      password: "Abcdefg1!",
      password_konfirmasi: "Abcdefg1!",
    });

    // Sukses -> diarahkan ke /masuk dengan pesan sukses.
    expect(
      await screen.findByText(
        "Akun cabang berhasil dibuat. Masuk dengan email cabang dan kata sandi yang baru saja Anda isi.",
      ),
    ).toBeDefined();
    expect(screen.getByLabelText("Email cabang")).toBeDefined();
    expect(screen.getByRole("button", { name: "Masuk" })).toBeDefined();

    // Token tidak pernah disimpan secara persisten (hanya state React).
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
  });

  it("tidak mengirim token kosong ke API", async () => {
    const panggilan = pasangFetch(() => galat("NOT_FOUND", "tidak ada", 404));

    render(<RouterProvider router={buatRouter("/daftar")} />);
    await screen.findByLabelText("Token registrasi");
    fireEvent.click(screen.getByRole("button", { name: "Lanjutkan" }));

    expect(await screen.findByText("Token registrasi wajib diisi.")).toBeDefined();
    expect(panggilanKe(panggilan, "/auth/registration/validate")).toBeUndefined();
  });
});

describe("/daftar — pesan spesifik per keadaan token (CP-REG-07)", () => {
  const KASUS: ReadonlyArray<{ kode: string; status: number; pesan: string }> = [
    {
      kode: "TOKEN_INVALID",
      status: 400,
      pesan: "Token registrasi tidak dikenali. Periksa kembali penulisannya, atau minta token baru ke admin Pusat.",
    },
    {
      kode: "TOKEN_EXPIRED",
      status: 400,
      pesan: "Token registrasi sudah kedaluwarsa. Minta token baru ke admin Pusat.",
    },
    {
      kode: "TOKEN_USED",
      status: 409,
      pesan:
        "Token registrasi sudah dipakai. Satu cabang hanya punya satu akun \u2014 gunakan lupa kata sandi bila perlu masuk kembali.",
    },
    {
      kode: "BRANCH_INACTIVE",
      status: 409,
      pesan: "Cabang ini sedang nonaktif. Hubungi admin Pusat untuk mengaktifkannya kembali.",
    },
    {
      kode: "BRANCH_HAS_ACCOUNT",
      status: 409,
      pesan: "Cabang ini sudah punya akun. Gunakan lupa kata sandi bila perlu masuk kembali.",
    },
  ];

  for (const { kode, status, pesan } of KASUS) {
    it(`menampilkan pesan spesifik untuk ${kode} dan tetap di langkah 1`, async () => {
      pasangFetch((url) =>
        url.endsWith("/auth/registration/validate")
          ? galat(kode, "pesan backend apa pun", status)
          : galat("NOT_FOUND", "tidak ada", 404),
      );

      render(<RouterProvider router={buatRouter("/daftar")} />);
      await screen.findByLabelText("Token registrasi");
      fireEvent.change(screen.getByLabelText("Token registrasi"), { target: { value: TOKEN } });
      fireEvent.click(screen.getByRole("button", { name: "Lanjutkan" }));

      expect(await screen.findByText(pesan)).toBeDefined();
      expect(screen.queryByLabelText("Nama cabang")).toBeNull();
      expect(screen.getByRole("button", { name: "Lanjutkan" })).toBeDefined();
    });
  }

  it("menangani pembatas laju 429", async () => {
    pasangFetch((url) =>
      url.endsWith("/auth/registration/validate")
        ? galat("RATE_LIMITED", "Terlalu banyak percobaan.", 429)
        : galat("NOT_FOUND", "tidak ada", 404),
    );

    render(<RouterProvider router={buatRouter("/daftar")} />);
    await screen.findByLabelText("Token registrasi");
    fireEvent.change(screen.getByLabelText("Token registrasi"), { target: { value: TOKEN } });
    fireEvent.click(screen.getByRole("button", { name: "Lanjutkan" }));

    expect(
      await screen.findByText("Terlalu banyak percobaan. Tunggu beberapa saat lalu coba lagi."),
    ).toBeDefined();
  });

  it("menangani kegagalan jaringan tanpa pesan teknis", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (String(input).endsWith("/auth/me")) return belumMasuk();
        throw new TypeError("Failed to fetch");
      }),
    );

    render(<RouterProvider router={buatRouter("/daftar")} />);
    await screen.findByLabelText("Token registrasi");
    fireEvent.change(screen.getByLabelText("Token registrasi"), { target: { value: TOKEN } });
    fireEvent.click(screen.getByRole("button", { name: "Lanjutkan" }));

    expect(
      await screen.findByText("Tidak dapat menghubungi server. Periksa koneksi internet lalu coba lagi."),
    ).toBeDefined();
  });

  it("tidak mengirim permintaan ganda saat tombol ditekan berulang", async () => {
    let selesai: (r: Response) => void = () => undefined;
    const validate = vi.fn(() => new Promise<Response>((resolve) => (selesai = resolve)));
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        if (String(input).endsWith("/auth/me")) return belumMasuk();
        return validate();
      }),
    );

    render(<RouterProvider router={buatRouter("/daftar")} />);
    await screen.findByLabelText("Token registrasi");
    fireEvent.change(screen.getByLabelText("Token registrasi"), { target: { value: TOKEN } });

    const tombol = screen.getByRole("button", { name: "Lanjutkan" });
    fireEvent.click(tombol);
    fireEvent.click(tombol);
    fireEvent.submit(tombol.closest("form") as HTMLFormElement);

    expect(validate).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Memeriksa token…" })).toBeDefined();

    selesai(json({ data: DATA_CABANG }));
    await screen.findByLabelText("Nama cabang");
  });
});

describe("/daftar — kegagalan di langkah 2", () => {
  it("menampilkan pesan server per field tanpa menimpa dengan field read-only", async () => {
    pasangFetch((url) => {
      if (url.endsWith("/auth/registration/validate")) return json({ data: DATA_CABANG });
      if (url.endsWith("/auth/register")) {
        return galat("VALIDATION_FAILED", "Konfirmasi kata sandi tidak sama.", 400, {
          password_konfirmasi: "konfirmasi kata sandi harus sama dengan kata sandi",
        });
      }
      return galat("NOT_FOUND", "tidak ada", 404);
    });

    render(<RouterProvider router={buatRouter("/daftar")} />);
    await screen.findByLabelText("Token registrasi");
    fireEvent.change(screen.getByLabelText("Token registrasi"), { target: { value: TOKEN } });
    fireEvent.click(screen.getByRole("button", { name: "Lanjutkan" }));

    await screen.findByLabelText("Nama cabang");
    isiFormulir("08123456789", "Abcdefg1!", "Abcdefg1!");
    fireEvent.click(screen.getByRole("button", { name: "Buat akun" }));

    expect(await screen.findByText("konfirmasi kata sandi harus sama dengan kata sandi")).toBeDefined();
    // Masih di langkah 2 dengan data cabang read-only utuh.
    expect((screen.getByLabelText("Email cabang") as HTMLInputElement).value).toBe(DATA_CABANG.email);
  });

  it("saat token ternyata sudah dipakai, menawarkan kembali ke langkah token", async () => {
    pasangFetch((url) => {
      if (url.endsWith("/auth/registration/validate")) return json({ data: DATA_CABANG });
      if (url.endsWith("/auth/register")) {
        return galat("TOKEN_USED", "Token registrasi sudah dipakai.", 409);
      }
      return galat("NOT_FOUND", "tidak ada", 404);
    });

    render(<RouterProvider router={buatRouter("/daftar")} />);
    await screen.findByLabelText("Token registrasi");
    fireEvent.change(screen.getByLabelText("Token registrasi"), { target: { value: TOKEN } });
    fireEvent.click(screen.getByRole("button", { name: "Lanjutkan" }));

    await screen.findByLabelText("Nama cabang");
    isiFormulir("08123456789", "Abcdefg1!", "Abcdefg1!");
    fireEvent.click(screen.getByRole("button", { name: "Buat akun" }));

    expect(
      await screen.findByText(
        "Token registrasi sudah dipakai. Satu cabang hanya punya satu akun \u2014 gunakan lupa kata sandi bila perlu masuk kembali.",
      ),
    ).toBeDefined();

    fireEvent.click(screen.getByRole("button", { name: "Gunakan token lain" }));
    expect(await screen.findByLabelText("Token registrasi")).toBeDefined();
    expect((screen.getByLabelText("Token registrasi") as HTMLInputElement).value).toBe("");
    expect(screen.queryByLabelText("Nama cabang")).toBeNull();
  });
});
