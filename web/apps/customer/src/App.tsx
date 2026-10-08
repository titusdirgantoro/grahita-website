import { useCallback, useEffect, useState, type FormEvent } from "react";

import { ApiClientError, api } from "@grahita/api-client";

interface Meta {
  app: string;
  version: string;
  env: string;
  customer_host: string;
  admin_host: string;
  waktu_wib: string;
}

interface Pengguna {
  id: string;
  kind: string;
  role: string;
  nama: string;
  email: string;
  branch_id?: string;
}

interface Produk {
  id: string;
  nama: string;
  jenis: string;
  satuan: string;
  deskripsi?: string;
  harga: number;
}

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

/**
 * App adalah shell minimal Customer Portal untuk F1-01.
 *
 * Tujuannya membuktikan rantai penuh berjalan: SPA same-origin → API /api/v1 →
 * sesi cookie host-only → katalog dengan harga khusus cabang. Sistem desain
 * lengkap (sidebar, bottom-nav, komponen) dikerjakan pada kartu F1-03.
 */
export default function App() {
  const [meta, setMeta] = useState<Meta | null>(null);
  const [pengguna, setPengguna] = useState<Pengguna | null>(null);
  const [produk, setProduk] = useState<Produk[]>([]);
  const [pesan, setPesan] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState(false);

  useEffect(() => {
    let aktif = true;
    api
      .get<Meta>("/meta")
      .then((m) => {
        if (aktif) setMeta(m);
      })
      .catch(() => {
        if (aktif) setPesan("Tidak bisa menghubungi API.");
      });
    api
      .get<{ user: Pengguna }>("/auth/me")
      .then((r) => {
        if (aktif) setPengguna(r.user);
      })
      .catch(() => {
        if (aktif) setPengguna(null);
      });
    return () => {
      aktif = false;
    };
  }, []);

  useEffect(() => {
    if (pengguna?.kind !== "branch") {
      setProduk([]);
      return;
    }
    let aktif = true;
    api
      .get<Produk[]>("/catalog")
      .then((list) => {
        if (aktif) setProduk(list);
      })
      .catch(() => {
        if (aktif) setProduk([]);
      });
    return () => {
      aktif = false;
    };
  }, [pengguna]);

  const masuk = useCallback(async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setSibuk(true);
    setPesan(null);
    try {
      const hasil = await api.post<{ user: Pengguna }>("/auth/login", {
        email: String(data.get("email") ?? ""),
        password: String(data.get("password") ?? ""),
      });
      setPengguna(hasil.user);
    } catch (err) {
      setPesan(err instanceof ApiClientError ? err.message : "Terjadi kesalahan tak terduga.");
    } finally {
      setSibuk(false);
    }
  }, []);

  const keluar = useCallback(async () => {
    await api.post("/auth/logout").catch(() => undefined);
    setPengguna(null);
    setProduk([]);
  }, []);

  return (
    <div className="shell">
      <header className="topbar">
        <div className="brand">
          <span className="logo-mark" aria-hidden="true">
            G
          </span>
          Grahita
        </div>
        {pengguna ? (
          <button type="button" className="btn btn-primary" onClick={() => void keluar()}>
            Keluar
          </button>
        ) : null}
      </header>

      {pesan ? <div className="notice-danger" role="alert">{pesan}</div> : null}

      {!pengguna ? (
        <form className="card" onSubmit={(e) => void masuk(e)}>
          <h1 className="card-title">Masuk ke Portal Cabang</h1>
          <div className="field">
            <label htmlFor="email">Email cabang</label>
            <input id="email" name="email" className="input" type="email" autoComplete="username" required />
          </div>
          <div className="field">
            <label htmlFor="password">Kata sandi</label>
            <input
              id="password"
              name="password"
              className="input"
              type="password"
              autoComplete="current-password"
              minLength={8}
              required
            />
          </div>
          <button type="submit" className="btn btn-primary" disabled={sibuk}>
            {sibuk ? "Memproses…" : "Masuk"}
          </button>
        </form>
      ) : (
        <>
          <section className="card">
            <h1 className="card-title">Selamat datang, {pengguna.nama}</h1>
            <p className="muted">
              {pengguna.email} · peran {pengguna.role}
            </p>
          </section>

          <section className="card">
            <h2 className="card-title">Katalog cabang</h2>
            {produk.length === 0 ? (
              <p className="muted">Belum ada produk yang tersedia untuk cabang ini.</p>
            ) : (
              <table>
                <thead>
                  <tr>
                    <th scope="col">Produk</th>
                    <th scope="col">Satuan</th>
                    <th scope="col" className="num">
                      Harga
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {produk.map((p) => (
                    <tr key={p.id}>
                      <td>{p.nama}</td>
                      <td>{p.satuan}</td>
                      <td className="num">{rupiah.format(p.harga)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>
        </>
      )}

      <p className="caption">
        {meta
          ? `${meta.app} ${meta.version} · lingkungan ${meta.env} · host ${meta.customer_host}`
          : "Memuat informasi aplikasi…"}
      </p>
    </div>
  );
}
