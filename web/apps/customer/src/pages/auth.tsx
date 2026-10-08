import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "@tanstack/react-router";

import { ApiClientError } from "@grahita/api-client";
import { AuthLayout, AuthLinks, Button, Field, Form, Input, Notice } from "@grahita/ui";

import { useSesi } from "../session";

/** pMasuk — /masuk (customer_v2.html: email cabang + password). */
export function HalamanMasuk() {
  const { masuk } = useSesi();
  const navigate = useNavigate();
  const [galat, setGalat] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState(false);

  async function kirim(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setSibuk(true);
    setGalat(null);
    try {
      await masuk(String(data.get("email") ?? ""), String(data.get("password") ?? ""));
      await navigate({ to: "/" });
    } catch (err) {
      setGalat(err instanceof ApiClientError ? err.message : "Terjadi kesalahan tak terduga.");
    } finally {
      setSibuk(false);
    }
  }

  return (
    <AuthLayout hint="Portal cabang Grahita Indonesia. Hubungi admin Pusat bila tidak bisa masuk.">
      <Form onSubmit={(e) => void kirim(e)}>
        <div>
          <h1>Masuk ke portal cabang</h1>
          <p className="muted">Pesan produk Grahita dan pantau invoice cabang Anda.</p>
        </div>
        {galat ? <Notice tone="danger">{galat}</Notice> : null}
        <Field label="Email cabang" htmlFor="em">
          <Input id="em" name="email" type="email" autoComplete="username" required />
        </Field>
        <Field label="Password" htmlFor="pw">
          <Input id="pw" name="password" type="password" autoComplete="current-password" required />
        </Field>
        <Button type="submit" variant="primary" block disabled={sibuk}>
          {sibuk ? "Memproses…" : "Masuk"}
        </Button>
        <AuthLinks>
          <Link to="/lupa">Lupa password</Link>
          <Link to="/daftar">Daftar dengan token</Link>
        </AuthLinks>
      </Form>
    </AuthLayout>
  );
}

function FieldBaca({ label, nilai, htmlFor }: { label: string; nilai: string; htmlFor: string }) {
  return (
    <Field label={label} htmlFor={htmlFor}>
      <Input id={htmlFor} readOnly defaultValue={nilai} />
    </Field>
  );
}

/** pDaftar — /daftar: langkah token lalu lengkapi akun (data cabang read-only). */
export function HalamanDaftar() {
  const [langkah, setLangkah] = useState(1);
  const [token, setToken] = useState("");
  const [galat, setGalat] = useState<Record<string, string>>({});

  function lanjut(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (token.trim().length < 4) {
      setGalat({ token: "Token tidak dikenali. Periksa kembali atau hubungi admin." });
      return;
    }
    setGalat({});
    setLangkah(2);
  }

  let isi: ReactNode;
  if (langkah === 1) {
    isi = (
      <Form onSubmit={lanjut}>
        <div>
          <h1>Daftar akun cabang</h1>
          <p className="muted">Masukkan token registrasi dari admin Pusat.</p>
        </div>
        <Field label="Token registrasi" htmlFor="tk" error={galat.token}>
          <Input
            id="tk"
            value={token}
            onChange={(e) => setToken(e.target.value.toUpperCase())}
            placeholder="GRH-XXXX-XXXX"
            autoComplete="off"
          />
        </Field>
        <Button type="submit" variant="primary" block>
          Lanjutkan
        </Button>
        <AuthLinks>
          <Link to="/masuk">Sudah punya akun? Masuk</Link>
        </AuthLinks>
      </Form>
    );
  } else {
    isi = (
      <Form>
        <div>
          <h1>Lengkapi akun</h1>
          <p className="muted">Data cabang diisi dari master data Pusat.</p>
        </div>
        <FieldBaca label="Nama cabang" nilai="Grahita Bandung" htmlFor="nc" />
        <FieldBaca label="Nama kepala cabang" nilai="Dra. Sri Wahyuni" htmlFor="nk" />
        <FieldBaca label="Email cabang" nilai="bandung@grahitaindonesia.com" htmlFor="ec" />
        <Field label="Nomor telepon" htmlFor="tel">
          <Input id="tel" inputMode="tel" autoComplete="tel" placeholder="08xxxxxxxxxx" />
        </Field>
        <Field label="Password" htmlFor="p1" hint="Minimal 8 karakter.">
          <Input id="p1" type="password" autoComplete="new-password" />
        </Field>
        <Field label="Konfirmasi password" htmlFor="p2">
          <Input id="p2" type="password" autoComplete="new-password" />
        </Field>
        <Button type="submit" variant="primary" block>
          Buat akun
        </Button>
      </Form>
    );
  }

  return (
    <AuthLayout
      steps={{ total: 2, aktif: langkah }}
      hint={langkah === 1 ? "Token registrasi berlaku 14 hari sejak dibuat admin." : undefined}
    >
      {isi}
    </AuthLayout>
  );
}

/** pLupa — /lupa: respons selalu sukses (tidak membocorkan keberadaan email). */
export function HalamanLupa() {
  const [terkirim, setTerkirim] = useState(false);

  function kirim(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setTerkirim(true);
  }

  return (
    <AuthLayout>
      <Form onSubmit={kirim}>
        <div>
          <h1>Lupa password</h1>
          <p className="muted">Kami kirim tautan membuat password baru ke email cabang.</p>
        </div>
        {terkirim ? (
          <Notice tone="success">
            Bila email terdaftar, tautan reset sudah dikirim. Periksa kotak masuk cabang.
          </Notice>
        ) : null}
        <Field label="Email cabang" htmlFor="le">
          <Input id="le" type="email" autoComplete="username" required />
        </Field>
        <Button type="submit" variant="primary" block>
          Kirim tautan reset
        </Button>
        <AuthLinks>
          <Link to="/masuk">Kembali ke halaman masuk</Link>
        </AuthLinks>
      </Form>
    </AuthLayout>
  );
}
