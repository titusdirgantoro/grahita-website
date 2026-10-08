import { useState, type FormEvent, type ReactNode } from "react";

import { Button } from "./Button";
import { Field, Form, Input } from "./Field";
import { Notice } from "./Notice";
import { fieldGalat, periksaKataSandiBaru, pesanGalatSingkat } from "../password";

export interface ChangePasswordFormProps {
  /**
   * Kirim permintaan ganti kata sandi (POST /auth/password/change). Lempar
   * error bila gagal — pesan/`fields` dari API dipakai untuk mengisi galat.
   */
  onSubmit: (passwordLama: string, passwordBaru: string) => Promise<void>;
  /** Dipanggil setelah berhasil (mis. menutup modal). */
  onSukses?: () => void;
  /** Label tombol simpan. */
  labelSimpan?: string;
  /** Catatan tambahan di bawah tombol. */
  catatan?: ReactNode;
  /** Pesan sukses; bawaan menyebut sesi lain diakhiri (PRD CP-AUT-07). */
  pesanSukses?: string;
  idPrefix?: string;
}

/**
 * ChangePasswordForm adalah form "Ganti password" yang dipakai portal cabang
 * (Profil) maupun portal admin (Profil) — dua-duanya memakai mekanisme yang
 * sama (PRD §3.2 CP-AUT-06, §4.10): verifikasi kata sandi lama lewat
 * POST /auth/password/change, lalu SELURUH sesi lain dicabut (CP-AUT-07).
 *
 * Aturan kata sandi diperiksa di klien lebih dulu (umpan balik cepat), tetapi
 * pesan dari server selalu menimpa lewat peta `fields`.
 */
export function ChangePasswordForm({
  onSubmit,
  onSukses,
  labelSimpan = "Simpan kata sandi",
  catatan,
  pesanSukses = "Kata sandi berhasil diganti. Sesi di perangkat lain sudah diakhiri.",
  idPrefix = "pw",
}: ChangePasswordFormProps) {
  const [galat, setGalat] = useState<Record<string, string>>({});
  const [galatUmum, setGalatUmum] = useState<string | null>(null);
  const [berhasil, setBerhasil] = useState(false);
  const [sibuk, setSibuk] = useState(false);
  // Setelah berhasil, form dirender ulang dari kosong (key berubah) sehingga
  // kata sandi tidak tertinggal di layar tanpa perlu mengelola tiap input.
  const [generasi, setGenerasi] = useState(0);

  async function kirim(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const lama = String(data.get("password_lama") ?? "");
    const baru = String(data.get("password_baru") ?? "");
    const konfirmasi = String(data.get("password_konfirmasi") ?? "");

    const lokal = periksaKataSandiBaru({ lama, baru, konfirmasi });
    if (lama === "") lokal.password_lama = "Kata sandi lama wajib diisi.";
    if (Object.keys(lokal).length > 0) {
      setGalat(lokal);
      setGalatUmum(null);
      setBerhasil(false);
      return;
    }

    setSibuk(true);
    setGalat({});
    setGalatUmum(null);
    setBerhasil(false);
    try {
      await onSubmit(lama, baru);
      setGenerasi((n) => n + 1);
      setBerhasil(true);
      onSukses?.();
    } catch (err) {
      const perField: Record<string, string> = {};
      for (const nama of ["password_lama", "password_baru"]) {
        const pesan = fieldGalat(err, nama);
        if (pesan) perField[nama] = pesan;
      }
      setGalat(perField);
      if (Object.keys(perField).length === 0) setGalatUmum(pesanGalatSingkat(err));
    } finally {
      setSibuk(false);
    }
  }

  return (
    <>
      {berhasil ? <Notice tone="success">{pesanSukses}</Notice> : null}
      {galatUmum ? <Notice tone="danger">{galatUmum}</Notice> : null}
      <Form key={generasi} onSubmit={(e) => void kirim(e)}>
        <Field label="Kata sandi lama" htmlFor={`${idPrefix}-lama`} error={galat.password_lama}>
          <Input
            id={`${idPrefix}-lama`}
            name="password_lama"
            type="password"
            autoComplete="current-password"
            required
          />
        </Field>
        <Field
          label="Kata sandi baru"
          htmlFor={`${idPrefix}-baru`}
          hint="Minimal 8 karakter, memuat huruf dan angka."
          error={galat.password_baru}
        >
          <Input
            id={`${idPrefix}-baru`}
            name="password_baru"
            type="password"
            autoComplete="new-password"
            required
          />
        </Field>
        <Field
          label="Konfirmasi kata sandi baru"
          htmlFor={`${idPrefix}-konfirmasi`}
          error={galat.password_konfirmasi}
        >
          <Input
            id={`${idPrefix}-konfirmasi`}
            name="password_konfirmasi"
            type="password"
            autoComplete="new-password"
            required
          />
        </Field>
        <Button type="submit" variant="primary" disabled={sibuk}>
          {sibuk ? "Menyimpan…" : labelSimpan}
        </Button>
        {catatan ? <p className="muted small">{catatan}</p> : null}
      </Form>
    </>
  );
}
