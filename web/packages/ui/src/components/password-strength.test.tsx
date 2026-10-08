import { useState } from "react";

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PasswordStrength } from "./PasswordStrength";
import { kekuatanKataSandi, periksaKataSandiBaru } from "../password";

describe("kekuatanKataSandi (indikator CP-REG-05)", () => {
  it("memberi skor 0 untuk kata sandi di bawah 8 karakter", () => {
    expect(kekuatanKataSandi("")).toEqual({ skor: 0, label: "Sangat lemah" });
    expect(kekuatanKataSandi("pen1")).toEqual({ skor: 0, label: "Sangat lemah" });
  });

  it("menaikkan skor seiring panjang dan variasi karakter", () => {
    // 8 karakter huruf saja: memenuhi panjang minimum, belum variasi.
    expect(kekuatanKataSandi("abcdefgh").skor).toBe(1);
    // huruf kecil + angka.
    expect(kekuatanKataSandi("abcdefg1").skor).toBe(2);
    // huruf besar + kecil + angka.
    expect(kekuatanKataSandi("Abcdefg1").skor).toBe(3);
    // ditambah simbol.
    expect(kekuatanKataSandi("Abcdefg1!").skor).toBe(4);
    expect(kekuatanKataSandi("Abcdefg1!").label).toBe("Sangat kuat");
  });

  it("menghargai kata sandi panjang tanpa simbol", () => {
    // Panjang >= 12 menambah satu tingkat; tanpa variasi huruf besar/simbol
    // skornya berhenti di "Kuat", bukan "Sangat kuat".
    const hasil = kekuatanKataSandi("katasandipanjangsekali1");
    expect(hasil.skor).toBe(3);
    expect(hasil.label).toBe("Kuat");
  });
});

describe("PasswordStrength", () => {
  it("tidak tampil sebelum pengguna mengetik", () => {
    render(<PasswordStrength password="" />);
    expect(screen.queryByRole("meter")).toBeNull();
  });

  it("menampilkan meter + label yang mengikuti kekuatan", () => {
    const { rerender } = render(<PasswordStrength password="pen1" />);

    const meter = screen.getByRole("meter", { name: "Kekuatan kata sandi" });
    expect(meter.getAttribute("aria-valuenow")).toBe("0");
    expect(meter.getAttribute("aria-valuemax")).toBe("4");
    expect(screen.getByText("Sangat lemah")).toBeDefined();

    rerender(<PasswordStrength password="Abcdefg1!" />);
    expect(screen.getByRole("meter").getAttribute("aria-valuenow")).toBe("4");
    expect(screen.getByText("Sangat kuat")).toBeDefined();
  });

  it("memperbarui label saat pengguna mengetik", () => {
    function Contoh() {
      const [nilai, setNilai] = useState("");
      return (
        <>
          <input aria-label="sandi" value={nilai} onChange={(e) => setNilai(e.target.value)} />
          <PasswordStrength password={nilai} />
        </>
      );
    }
    render(<Contoh />);

    fireEvent.change(screen.getByLabelText("sandi"), { target: { value: "abcdefg1" } });
    expect(screen.getByText("Cukup")).toBeDefined();

    fireEvent.change(screen.getByLabelText("sandi"), { target: { value: "Abcdefg1!" } });
    expect(screen.getByText("Sangat kuat")).toBeDefined();
  });
});

describe("periksaKataSandiBaru — pesan konfirmasi dapat disesuaikan", () => {
  it("memakai pesan bawaan bila tidak diberi pesanKonfirmasi", () => {
    const galat = periksaKataSandiBaru({ baru: "abcdefg1", konfirmasi: "beda1234" });
    expect(galat.password_konfirmasi).toBe("Konfirmasi tidak sama dengan kata sandi baru.");
  });

  it("memakai pesan khusus pendaftaran akun cabang", () => {
    const galat = periksaKataSandiBaru({
      baru: "abcdefg1",
      konfirmasi: "beda1234",
      fieldBaru: "password",
      pesanKonfirmasi: "Konfirmasi kata sandi belum sama.",
    });
    expect(galat.password_konfirmasi).toBe("Konfirmasi kata sandi belum sama.");
    expect(galat.password).toBeUndefined();
  });
});
