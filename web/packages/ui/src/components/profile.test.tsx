import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Brand } from "./Brand";
import { ChangePasswordForm } from "./ChangePassword";
import { Logo } from "./Logo";
import { PasswordResetPage } from "./PasswordReset";
import { fieldGalat, periksaKataSandiBaru, pesanGalatSingkat } from "../password";

describe("Logo & Brand — aset resmi Grahita Indonesia", () => {
  it("Logo memuat berkas aset resmi dengan alt bermakna", () => {
    render(<Logo />);

    const gambar = screen.getByRole("img", { name: "Grahita Indonesia" });
    expect(gambar.getAttribute("src")).toContain("logo-grahita-mark");
  });

  it("varian lockup memakai berkas lockup yang berbeda", () => {
    const { rerender } = render(<Logo variant="lockup" />);
    expect(screen.getByRole("img", { name: "Grahita Indonesia" }).getAttribute("src")).toContain(
      "logo-grahita-lockup",
    );

    // alt kosong = dekoratif (dipakai bersama wordmark pada Brand).
    rerender(<Logo alt="" />);
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("Brand menampilkan lambang + wordmark tanpa menyebut nama dua kali", () => {
    render(<Brand />);

    // Lambang dekoratif (alt kosong), nama diucapkan teks di sebelahnya.
    expect(screen.queryByRole("img")).toBeNull();
    expect(screen.getByText("Grahita")).toBeDefined();
  });
});

describe("ChangePasswordForm", () => {
  function isi(lama: string, baru: string, konfirmasi: string) {
    fireEvent.change(screen.getByLabelText("Kata sandi lama"), { target: { value: lama } });
    fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: baru } });
    fireEvent.change(screen.getByLabelText("Konfirmasi kata sandi baru"), {
      target: { value: konfirmasi },
    });
  }

  it("menolak kata sandi lemah dan konfirmasi tidak sama tanpa memanggil API", async () => {
    const kirim = vi.fn(async () => undefined);
    render(<ChangePasswordForm onSubmit={kirim} />);

    isi("rahasia123", "pendek", "pendek");
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));
    expect(await screen.findByText("Minimal 8 karakter.")).toBeDefined();
    expect(kirim).not.toHaveBeenCalled();

    isi("rahasia123", "sandibaru123", "beda123456");
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));
    expect(await screen.findByText("Konfirmasi tidak sama dengan kata sandi baru.")).toBeDefined();

    // Kata sandi baru sama dengan yang lama juga ditolak.
    isi("rahasia123", "rahasia123", "rahasia123");
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));
    expect(
      await screen.findByText("Kata sandi baru harus berbeda dari kata sandi lama."),
    ).toBeDefined();
    expect(kirim).not.toHaveBeenCalled();
  });

  it("mengirim kata sandi lama & baru lalu menampilkan pesan sukses", async () => {
    const kirim = vi.fn(async () => undefined);
    render(<ChangePasswordForm onSubmit={kirim} />);

    isi("rahasia123", "sandibaru123", "sandibaru123");
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));

    await waitFor(() => expect(kirim).toHaveBeenCalledWith("rahasia123", "sandibaru123"));
    expect(
      await screen.findByText(
        "Kata sandi berhasil diganti. Sesi di perangkat lain sudah diakhiri.",
      ),
    ).toBeDefined();
  });

  it("menempelkan pesan galat per field dari respons API", async () => {
    const kirim = vi.fn(async () => {
      throw { message: "Kata sandi lama tidak cocok.", fields: { password_lama: "kata sandi lama salah" } };
    });
    render(<ChangePasswordForm onSubmit={kirim} />);

    isi("salahsekali", "sandibaru123", "sandibaru123");
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));

    expect(await screen.findByText("kata sandi lama salah")).toBeDefined();
  });

  it("menampilkan pesan umum bila API gagal tanpa field", async () => {
    const kirim = vi.fn(async () => {
      throw { message: "Terlalu banyak percobaan. Coba lagi beberapa saat lagi." };
    });
    render(<ChangePasswordForm onSubmit={kirim} />);

    isi("rahasia123", "sandibaru123", "sandibaru123");
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));

    expect(
      await screen.findByText("Terlalu banyak percobaan. Coba lagi beberapa saat lagi."),
    ).toBeDefined();
  });
});

describe("PasswordResetPage", () => {
  it("mode lupa: kirim email lalu tampilkan pesan seragam", async () => {
    const kirimTautan = vi.fn(async () => undefined);
    render(<PasswordResetPage onKirimTautan={kirimTautan} onSimpanKataSandi={vi.fn()} namaPortal="portal cabang" />);

    expect(screen.getByText("Lupa password")).toBeDefined();
    // Email tidak valid tidak boleh memanggil API.
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "bukan-email" } });
    fireEvent.click(screen.getByRole("button", { name: "Kirim tautan atur ulang" }));
    expect(await screen.findByText("Masukkan email yang benar.")).toBeDefined();
    expect(kirimTautan).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "a@example.test" } });
    fireEvent.click(screen.getByRole("button", { name: "Kirim tautan atur ulang" }));

    await waitFor(() => expect(kirimTautan).toHaveBeenCalledWith("a@example.test"));
    expect(
      await screen.findByText(
        "Bila email terdaftar, tautan atur ulang sudah dikirim. Periksa kotak masuk Anda.",
      ),
    ).toBeDefined();
  });

  it("mode atur ulang: memakai token dari query dan menyimpan kata sandi baru", async () => {
    const simpan = vi.fn(async () => undefined);
    render(
      <PasswordResetPage
        token="GRH-TOKEN-UJI"
        onKirimTautan={vi.fn()}
        onSimpanKataSandi={simpan}
        namaPortal="admin portal"
      />,
    );

    expect(screen.getByText("Buat kata sandi baru")).toBeDefined();
    fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: "sandibaru123" } });
    fireEvent.change(screen.getByLabelText("Konfirmasi kata sandi baru"), {
      target: { value: "sandibaru123" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));

    await waitFor(() => expect(simpan).toHaveBeenCalledWith("GRH-TOKEN-UJI", "sandibaru123"));
    expect(
      await screen.findByText(
        "Kata sandi baru tersimpan. Sesi lama sudah diakhiri — silakan masuk kembali.",
      ),
    ).toBeDefined();
  });

  it("mode atur ulang: menampilkan pesan token tidak valid dari API", async () => {
    const simpan = vi.fn(async () => {
      throw {
        message: "Tautan atur ulang tidak valid, sudah dipakai, atau kedaluwarsa. Minta tautan baru.",
      };
    });
    render(
      <PasswordResetPage token="TOKEN-MATI" onKirimTautan={vi.fn()} onSimpanKataSandi={simpan} namaPortal="portal cabang" />,
    );

    fireEvent.change(screen.getByLabelText("Kata sandi baru"), { target: { value: "sandibaru123" } });
    fireEvent.change(screen.getByLabelText("Konfirmasi kata sandi baru"), {
      target: { value: "sandibaru123" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Simpan kata sandi" }));

    expect(
      await screen.findByText(
        "Tautan atur ulang tidak valid, sudah dipakai, atau kedaluwarsa. Minta tautan baru.",
      ),
    ).toBeDefined();
  });
});

describe("periksaKataSandiBaru & pembantu galat", () => {
  it("mengikuti aturan backend: panjang, huruf+angka, spasi tepi, konfirmasi", () => {
    expect(periksaKataSandiBaru({ baru: "pendek" }).password_baru).toMatch(/Minimal 8/);
    expect(periksaKataSandiBaru({ baru: "hanyahurufsaja" }).password_baru).toMatch(/huruf dan angka/);
    expect(periksaKataSandiBaru({ baru: " sandi123 " }).password_baru).toMatch(/spasi/);
    expect(periksaKataSandiBaru({ baru: "sandi1234", konfirmasi: "lain12345" }).password_konfirmasi).toBeDefined();
    expect(periksaKataSandiBaru({ baru: "sandi1234", konfirmasi: "sandi1234" })).toEqual({});
  });

  it("membaca pesan dan field dari error bergaya ApiClientError", () => {
    const galat = { message: "Kata sandi belum memenuhi syarat.", fields: { password: "terlalu lemah" } };
    expect(pesanGalatSingkat(galat)).toBe("Kata sandi belum memenuhi syarat.");
    expect(fieldGalat(galat, "password")).toBe("terlalu lemah");
    expect(fieldGalat(galat, "tidak_ada")).toBeUndefined();
    expect(pesanGalatSingkat(undefined)).toBe("Terjadi kesalahan tak terduga.");
  });
});
