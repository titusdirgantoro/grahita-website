import { useState, type FormEvent, type ReactNode } from "react";

import { AuthLayout, AuthLinks } from "./AuthLayout";
import { Button } from "./Button";
import { Field, Form, Input } from "./Field";
import { Notice } from "./Notice";
import { fieldGalat, periksaKataSandiBaru, pesanGalatSingkat } from "../password";

export interface PasswordResetPageProps {
  /**
   * Token dari query `?token=…` pada tautan email. Kosong berarti pengguna
   * meminta tautan (mode "lupa password").
   */
  token?: string;
  /** Kirim permintaan tautan (POST /auth/password/forgot). */
  onKirimTautan: (email: string) => Promise<void>;
  /** Simpan kata sandi baru dengan token (POST /auth/password/reset). */
  onSimpanKataSandi: (token: string, password: string) => Promise<void>;
  /** Tautan kembali ke halaman masuk (ReactNode, biasanya <Link>). */
  tautanMasuk?: ReactNode;
  /** Nama portal untuk hint, mis. "portal cabang" / "portal admin". */
  namaPortal: string;
  /** Dipanggil setelah kata sandi tersimpan (mis. navigasi ke /masuk). */
  onSelesai?: () => void;
}

/**
 * PasswordResetPage melayani DUA keadaan dari satu rute (PRD §3.2 CP-AUT-03/04,
 * §4.10):
 *
 *  - tanpa `?token=`: form "Lupa password" — respons server selalu sama apa pun
 *    keadaan emailnya, dan UI juga menampilkan pesan yang sama supaya
 *    keberadaan akun tidak bocor;
 *  - dengan `?token=`: form "Buat kata sandi baru" — memakai token sekali pakai;
 *    sesudah berhasil seluruh sesi pengguna dicabut oleh backend.
 *
 * Dipakai kedua portal: tautan email diarahkan ke portal yang sesuai lewat
 * PASSWORD_RESET_PATH + host portal (auth.NewResetURLBuilder).
 */
export function PasswordResetPage({
  token,
  onKirimTautan,
  onSimpanKataSandi,
  tautanMasuk,
  namaPortal,
  onSelesai,
}: PasswordResetPageProps) {
  const [terkirim, setTerkirim] = useState(false);
  const [selesai, setSelesai] = useState(false);
  const [galat, setGalat] = useState<Record<string, string>>({});
  const [galatUmum, setGalatUmum] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState(false);

  async function kirimTautan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const email = String(new FormData(event.currentTarget).get("email") ?? "").trim();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      setGalat({ email: "Masukkan email yang benar." });
      setGalatUmum(null);
      return;
    }
    setSibuk(true);
    setGalat({});
    setGalatUmum(null);
    try {
      await onKirimTautan(email);
      setTerkirim(true);
    } catch (err) {
      setGalatUmum(pesanGalatSingkat(err));
    } finally {
      setSibuk(false);
    }
  }

  async function simpanKataSandi(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const password = String(data.get("password") ?? "");
    const konfirmasi = String(data.get("password_konfirmasi") ?? "");

    const lokal = periksaKataSandiBaru({ baru: password, konfirmasi, fieldBaru: "password" });
    if (Object.keys(lokal).length > 0) {
      setGalat(lokal);
      setGalatUmum(null);
      return;
    }

    setSibuk(true);
    setGalat({});
    setGalatUmum(null);
    try {
      await onSimpanKataSandi(token ?? "", password);
      setSelesai(true);
      onSelesai?.();
    } catch (err) {
      const perField: Record<string, string> = {};
      const pesanPassword = fieldGalat(err, "password");
      if (pesanPassword) perField.password = pesanPassword;
      setGalat(perField);
      if (Object.keys(perField).length === 0) setGalatUmum(pesanGalatSingkat(err));
    } finally {
      setSibuk(false);
    }
  }

  if (token) {
    return (
      <AuthLayout hint={`Tautan atur ulang hanya berlaku sekali pakai untuk ${namaPortal}.`}>
        <Form onSubmit={(e) => void simpanKataSandi(e)}>
          <div>
            <h1>Buat kata sandi baru</h1>
            <p className="muted">
              Tautan dari email Anda dipakai sekali saja. Setelah tersimpan, seluruh sesi lama
              diakhiri dan Anda dapat masuk lagi dengan kata sandi baru.
            </p>
          </div>
          {selesai ? (
            <Notice tone="success">
              Kata sandi baru tersimpan. Sesi lama sudah diakhiri — silakan masuk kembali.
            </Notice>
          ) : (
            <>
              {galatUmum ? <Notice tone="danger">{galatUmum}</Notice> : null}
              <Field
                label="Kata sandi baru"
                htmlFor="rp-baru"
                hint="Minimal 8 karakter, memuat huruf dan angka."
                error={galat.password}
              >
                <Input id="rp-baru" name="password" type="password" autoComplete="new-password" required />
              </Field>
              <Field
                label="Konfirmasi kata sandi baru"
                htmlFor="rp-konfirmasi"
                error={galat.password_konfirmasi}
              >
                <Input
                  id="rp-konfirmasi"
                  name="password_konfirmasi"
                  type="password"
                  autoComplete="new-password"
                  required
                />
              </Field>
              <Button type="submit" variant="primary" block disabled={sibuk}>
                {sibuk ? "Menyimpan…" : "Simpan kata sandi"}
              </Button>
            </>
          )}
          <AuthLinks>{tautanMasuk}</AuthLinks>
        </Form>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout hint={`Tautan berlaku terbatas dan hanya bisa dipakai sekali untuk ${namaPortal}.`}>
      <Form onSubmit={(e) => void kirimTautan(e)}>
        <div>
          <h1>Lupa password</h1>
          <p className="muted">Kami kirim tautan membuat kata sandi baru ke email akun Anda.</p>
        </div>
        {terkirim ? (
          <Notice tone="success">
            Bila email terdaftar, tautan atur ulang sudah dikirim. Periksa kotak masuk Anda.
          </Notice>
        ) : null}
        {galatUmum ? <Notice tone="danger">{galatUmum}</Notice> : null}
        <Field label="Email" htmlFor="rp-email" error={galat.email}>
          <Input id="rp-email" name="email" type="email" autoComplete="username" required />
        </Field>
        <Button type="submit" variant="primary" block disabled={sibuk}>
          {sibuk ? "Mengirim…" : "Kirim tautan atur ulang"}
        </Button>
        <AuthLinks>{tautanMasuk}</AuthLinks>
      </Form>
    </AuthLayout>
  );
}
