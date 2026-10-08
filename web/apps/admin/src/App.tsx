import { useCallback, useEffect, useState, type FormEvent } from "react";

import { ApiClientError, api } from "@grahita/api-client";

interface Pengguna {
  id: string;
  kind: string;
  role: string;
  nama: string;
  email: string;
}

interface Cabang {
  id: string;
  nama: string;
  nama_kepala: string;
  email: string;
  is_cnc: boolean;
  is_active: boolean;
}

const LABEL_PERAN: Record<string, string> = {
  manager: "Manager",
  superadmin: "Superadmin",
};

/**
 * App adalah shell minimal Admin Portal untuk F1-01.
 *
 * Menu dipisah berdasarkan peran, dan — yang lebih penting — API menolak
 * sendiri bila peran tidak berhak (otorisasi tidak bergantung pada UI).
 */
export default function App() {
  const [pengguna, setPengguna] = useState<Pengguna | null>(null);
  const [cabang, setCabang] = useState<Cabang[]>([]);
  const [pengaturan, setPengaturan] = useState<Record<string, unknown> | null>(null);
  const [pesan, setPesan] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState(false);

  const bolehLihatCabang = pengguna?.role === "manager" || pengguna?.role === "superadmin";
  const bolehLihatPengaturan = pengguna?.role === "superadmin";

  useEffect(() => {
    let aktif = true;
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
    if (!bolehLihatCabang) {
      setCabang([]);
      return;
    }
    let aktif = true;
    api
      .get<Cabang[]>("/admin/branches")
      .then((list) => {
        if (aktif) setCabang(list);
      })
      .catch(() => {
        if (aktif) setCabang([]);
      });
    return () => {
      aktif = false;
    };
  }, [bolehLihatCabang]);

  useEffect(() => {
    if (!bolehLihatPengaturan) {
      setPengaturan(null);
      return;
    }
    let aktif = true;
    api
      .get<Record<string, unknown>>("/admin/settings")
      .then((v) => {
        if (aktif) setPengaturan(v);
      })
      .catch(() => {
        if (aktif) setPengaturan(null);
      });
    return () => {
      aktif = false;
    };
  }, [bolehLihatPengaturan]);

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
  }, []);

  if (!pengguna) {
    return (
      <div className="shell">
        <header className="topbar">
          <div className="brand">
            <span className="logo-mark" aria-hidden="true">
              G
            </span>
            Grahita Admin
          </div>
        </header>
        {pesan ? (
          <div className="notice-danger" role="alert">
            {pesan}
          </div>
        ) : null}
        <form className="card" onSubmit={(e) => void masuk(e)}>
          <h1 className="card-title">Masuk ke Admin Portal</h1>
          <div className="field">
            <label htmlFor="email">Email</label>
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
      </div>
    );
  }

  return (
    <div className="shell">
      <header className="topbar">
        <div className="brand">
          <span className="logo-mark" aria-hidden="true">
            G
          </span>
          Grahita Admin
        </div>
        <div>
          <span className="caption">
            {pengguna.nama} · {LABEL_PERAN[pengguna.role] ?? pengguna.role}
          </span>{" "}
          <button type="button" className="btn btn-primary" onClick={() => void keluar()}>
            Keluar
          </button>
        </div>
      </header>

      {bolehLihatCabang ? (
        <section className="card">
          <h1 className="card-title">Master cabang</h1>
          {cabang.length === 0 ? (
            <p className="muted">Belum ada data cabang.</p>
          ) : (
            <table>
              <thead>
                <tr>
                  <th scope="col">Cabang</th>
                  <th scope="col">Kepala cabang</th>
                  <th scope="col">Email</th>
                  <th scope="col">C&amp;C</th>
                </tr>
              </thead>
              <tbody>
                {cabang.map((c) => (
                  <tr key={c.id}>
                    <td>{c.nama}</td>
                    <td>{c.nama_kepala}</td>
                    <td>{c.email}</td>
                    <td>{c.is_cnc ? "Ya" : "Tidak"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      ) : (
        <section className="card">
          <p className="muted">Menu master data hanya untuk Manager dan Superadmin.</p>
        </section>
      )}

      {bolehLihatPengaturan ? (
        <section className="card">
          <h2 className="card-title">Pengaturan sistem (Superadmin)</h2>
          <p className="muted">
            {pengaturan
              ? `${Object.keys(pengaturan).length} kunci pengaturan dimuat.`
              : "Memuat pengaturan…"}
          </p>
        </section>
      ) : null}
    </div>
  );
}
