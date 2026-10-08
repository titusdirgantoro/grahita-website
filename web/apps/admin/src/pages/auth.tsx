import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "@tanstack/react-router";

import { ApiClientError, api } from "@grahita/api-client";
import { AuthLayout, AuthLinks, Button, Field, Form, Input, Notice, PasswordResetPage } from "@grahita/ui";

import { useSesi } from "../session";

/** Masuk admin — form email + password (Superadmin / Manager). */
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
    <AuthLayout hint="Portal admin Grahita Indonesia. Akses hanya untuk staf Pusat.">
      <Form onSubmit={(e) => void kirim(e)}>
        <div>
          <h1>Masuk ke admin portal</h1>
          <p className="muted">Kelola cabang, katalog, pesanan, dan laporan.</p>
        </div>
        {galat ? <Notice tone="danger">{galat}</Notice> : null}
        <Field label="Email" htmlFor="em">
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
        </AuthLinks>
      </Form>
    </AuthLayout>
  );
}

/**
 * pLupa admin — /lupa: rute yang sama dengan portal cabang, tetapi dipakai
 * akun admin. Satu rute melayani dua keadaan (PRD §3.2 CP-AUT-03/04, §4.10):
 *
 *  - tanpa `?token=`: minta tautan atur ulang lewat email (respons seragam);
 *  - dengan `?token=…`: buat kata sandi baru dari tautan email.
 *
 * Tautan email diarahkan ke host admin (`PASSWORD_RESET_PATH` + ADMIN_HOST,
 * lihat auth.NewResetURLBuilder), jadi tidak ada cookie yang dibagi antar
 * subdomain.
 */
export function HalamanLupa({ token }: { token?: string }) {
  return (
    <PasswordResetPage
      token={token}
      namaPortal="admin portal"
      onKirimTautan={async (email) => {
        await api.post("/auth/password/forgot", { email });
      }}
      onSimpanKataSandi={async (tokenReset, password) => {
        await api.post("/auth/password/reset", { token: tokenReset, password });
      }}
      tautanMasuk={<Link to="/masuk">Kembali ke halaman masuk</Link>}
    />
  );
}
