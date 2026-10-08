import { useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";

import { ApiClientError } from "@grahita/api-client";
import { AuthLayout, Button, Field, Form, Input, Notice } from "@grahita/ui";

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
      </Form>
    </AuthLayout>
  );
}
