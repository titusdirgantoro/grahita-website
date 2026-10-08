import { useCallback, useEffect, useState } from "react";

import { ApiClientError, api } from "@grahita/api-client";
import { Badge, Button, Card, EmptyState, Field, Form, Input, Modal, Notice, PageHead, Segmented, Select, SkeletonList, SwitchRow, Table, useToast, type BadgeTone } from "@grahita/ui";

import { useSesi } from "../session";
import styles from "./admin-pages.module.css";

/** BranchUser adalah satu baris tab "User cabang" (satu baris per cabang). */
export interface BranchUser {
  branch_id: string;
  user_id?: string;
  nama: string;
  nama_kepala: string;
  email: string;
  telepon?: string;
  has_account: boolean;
  is_active: boolean;
  branch_active: boolean;
  status: "aktif" | "belum_registrasi" | "nonaktif";
  last_login_at: string | null;
  token_prefix?: string;
  token_expires_at?: string;
}

/** AdminUser adalah satu baris tab "User admin". */
export interface AdminUser {
  id: string;
  nama: string;
  email: string;
  role: "manager" | "superadmin";
  notif_invoice: boolean;
  notif_surat_jalan: boolean;
  is_active: boolean;
  status: "undangan" | "aktif" | "nonaktif";
}

const TONE_STATUS_CABANG: Record<BranchUser["status"], BadgeTone> = {
  aktif: "success",
  belum_registrasi: "draft",
  nonaktif: "neutral",
};

const LABEL_STATUS_CABANG: Record<BranchUser["status"], string> = {
  aktif: "Aktif",
  belum_registrasi: "Belum registrasi",
  nonaktif: "Nonaktif",
};

const TONE_STATUS_ADMIN: Record<AdminUser["status"], BadgeTone> = {
  aktif: "success",
  undangan: "warning",
  nonaktif: "neutral",
};

const LABEL_STATUS_ADMIN: Record<AdminUser["status"], string> = {
  aktif: "Aktif",
  undangan: "Undangan terkirim",
  nonaktif: "Nonaktif",
};

const waktuWIB = new Intl.DateTimeFormat("id-ID", {
  dateStyle: "medium",
  timeStyle: "short",
  timeZone: "Asia/Jakarta",
});

/** formatWaktu menampilkan tanggal WIB; null/invalid menjadi "—". */
export function formatWaktu(nilai: string | null | undefined): string {
  if (!nilai) return "—";
  const t = new Date(nilai);
  if (Number.isNaN(t.getTime())) return "—";
  return waktuWIB.format(t);
}

function pesanGalat(err: unknown): string {
  if (err instanceof ApiClientError) return err.message;
  return "Terjadi kesalahan. Coba lagi.";
}

interface AdminForm {
  id?: string;
  nama: string;
  email: string;
  role: "manager" | "superadmin";
  notif_invoice: boolean;
  notif_surat_jalan: boolean;
  is_active: boolean;
}

const ADMIN_BARU: AdminForm = {
  nama: "",
  email: "",
  role: "manager",
  notif_invoice: false,
  notif_surat_jalan: false,
  is_active: true,
};

/**
 * PenggunaPage — /pengguna: user cabang & user admin (PRD §4.8).
 *
 * Tab "User admin" hanya diberikan kepada Superadmin, sesuai matriks hak akses
 * PRD §2.1; API-nya juga tetap menolak Manager. Semua aksi menunggu respons
 * server (bukan optimistic), dan galat server ditampilkan apa adanya.
 */
export function PenggunaPage() {
  const { peran } = useSesi();
  const { tampilkan } = useToast();
  const superadmin = peran === "superadmin";

  const [tab, setTab] = useState<"cabang" | "admin">("cabang");
  const [cabang, setCabang] = useState<BranchUser[] | null>(null);
  const [admins, setAdmins] = useState<AdminUser[] | null>(null);
  const [memuat, setMemuat] = useState(false);
  const [galat, setGalat] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState("");
  const [form, setForm] = useState<AdminForm | null>(null);
  const [galatForm, setGalatForm] = useState<string | null>(null);

  const muatCabang = useCallback(async () => {
    setMemuat(true);
    try {
      setCabang(await api.get<BranchUser[]>("/admin/users/branch"));
      setGalat(null);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setMemuat(false);
    }
  }, []);

  const muatAdmin = useCallback(async () => {
    setMemuat(true);
    try {
      setAdmins(await api.get<AdminUser[]>("/admin/users/admin"));
      setGalat(null);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setMemuat(false);
    }
  }, []);

  useEffect(() => {
    if (tab === "admin") {
      if (superadmin) void muatAdmin();
      return;
    }
    void muatCabang();
  }, [tab, superadmin, muatCabang, muatAdmin]);

  async function kirimUlangReset(row: BranchUser) {
    if (!row.user_id) return;
    setSibuk(row.user_id);
    try {
      await api.post(`/admin/users/branch/${row.user_id}/reset`);
      tampilkan(`Tautan atur ulang dikirim ke ${row.email}`);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setSibuk("");
    }
  }

  async function ubahAktifCabang(row: BranchUser) {
    if (!row.user_id) return;
    setSibuk(row.user_id);
    try {
      const hasil = await api.put<BranchUser>(`/admin/users/branch/${row.user_id}`, {
        is_active: !row.is_active,
      });
      setCabang((lama) => (lama ?? []).map((r) => (r.user_id === hasil.user_id ? hasil : r)));
      tampilkan(`Akun ${row.nama} ${hasil.is_active ? "diaktifkan" : "dinonaktifkan"}`);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setSibuk("");
    }
  }

  async function simpanAdmin() {
    if (!form) return;
    setSibuk("admin");
    setGalatForm(null);
    const muatan = {
      nama: form.nama,
      email: form.email,
      role: form.role,
      notif_invoice: form.notif_invoice,
      notif_surat_jalan: form.notif_surat_jalan,
    };
    try {
      if (form.id) {
        await api.put(`/admin/users/admin/${form.id}`, { ...muatan, is_active: form.is_active });
        tampilkan(`Perubahan admin ${form.nama} disimpan`);
      } else {
        await api.post("/admin/users/admin", muatan);
        tampilkan(`Undangan dikirim ke ${form.email}`);
      }
      setForm(null);
      await muatAdmin();
    } catch (err) {
      setGalatForm(pesanGalat(err));
    } finally {
      setSibuk("");
    }
  }

  return (
    <>
      <PageHead
        title="User management"
        sub={tab === "admin" ? "Admin Pusat dan penerima email dokumen" : "Satu akun per cabang"}
        actions={
          <div className={styles.row}>
            {superadmin ? (
              <Segmented
                label="Jenis pengguna"
                options={[
                  { value: "cabang", label: "User cabang" },
                  { value: "admin", label: "User admin" },
                ]}
                value={tab}
                onChange={setTab}
              />
            ) : null}
            {superadmin && tab === "admin" ? (
              <Button variant="primary" icon="plus" onClick={() => { setGalatForm(null); setForm({ ...ADMIN_BARU }); }}>
                Undang admin
              </Button>
            ) : null}
          </div>
        }
      />

      <Card>
        {galat ? (
          <div style={{ marginBottom: 12 }}>
            <Notice tone="danger">{galat}</Notice>
          </div>
        ) : null}

        {memuat ? (
          <SkeletonList rows={4} />
        ) : tab === "cabang" ? (
          <TabelCabang
            baris={cabang ?? []}
            sibuk={sibuk}
            onReset={kirimUlangReset}
            onUbahAktif={ubahAktifCabang}
          />
        ) : (
          <TabelAdmin
            baris={admins ?? []}
            onUbah={(row) => {
              setGalatForm(null);
              setForm({
                id: row.id,
                nama: row.nama,
                email: row.email,
                role: row.role,
                notif_invoice: row.notif_invoice,
                notif_surat_jalan: row.notif_surat_jalan,
                is_active: row.is_active,
              });
            }}
          />
        )}
      </Card>

      <Modal
        open={form !== null}
        title={form?.id ? "Ubah admin" : "Undang admin"}
        onClose={() => setForm(null)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setForm(null)}>
              Batal
            </Button>
            <Button
              variant="primary"
              disabled={sibuk === "admin" || !form?.nama || !form?.email}
              onClick={() => void simpanAdmin()}
            >
              {form?.id ? "Simpan" : "Kirim undangan"}
            </Button>
          </>
        }
      >
        {form ? (
          <Form>
            {galatForm ? <Notice tone="danger">{galatForm}</Notice> : null}
            <Field label="Nama pengguna" htmlFor="af-nama">
              <Input
                id="af-nama"
                value={form.nama}
                onChange={(e) => setForm({ ...form, nama: e.target.value })}
              />
            </Field>
            <Field
              label="Email"
              htmlFor="af-email"
              hint={form.id ? undefined : "Link untuk membuat kata sandi dikirim ke email ini."}
            >
              <Input
                id="af-email"
                type="email"
                readOnly={Boolean(form.id)}
                value={form.email}
                onChange={(e) => setForm({ ...form, email: e.target.value })}
              />
            </Field>
            <Field label="Role" htmlFor="af-role">
              <Select
                id="af-role"
                value={form.role}
                onChange={(e) => setForm({ ...form, role: e.target.value as AdminForm["role"] })}
              >
                <option value="manager">Manager</option>
                <option value="superadmin">Superadmin</option>
              </Select>
            </Field>
            <SwitchRow
              checked={form.notif_invoice}
              onChange={(v) => setForm({ ...form, notif_invoice: v })}
              label="Terima email invoice"
              hint="Salinan invoice setiap pesanan selesai atau direvisi."
            />
            <SwitchRow
              checked={form.notif_surat_jalan}
              onChange={(v) => setForm({ ...form, notif_surat_jalan: v })}
              label="Terima email surat jalan"
              hint="Surat jalan (Excel & PDF) untuk pengiriman."
            />
            {form.id ? (
              <SwitchRow
                checked={form.is_active}
                onChange={(v) => setForm({ ...form, is_active: v })}
                label="Aktif"
                hint="Admin nonaktif tidak bisa masuk."
              />
            ) : null}
          </Form>
        ) : null}
      </Modal>
    </>
  );
}

function TabelCabang({
  baris,
  sibuk,
  onReset,
  onUbahAktif,
}: {
  baris: BranchUser[];
  sibuk: string;
  onReset: (row: BranchUser) => void;
  onUbahAktif: (row: BranchUser) => void;
}) {
  if (baris.length === 0) {
    return <EmptyState icon="users-three">Belum ada cabang terdaftar.</EmptyState>;
  }
  return (
    <Table mobileCards caption="Akun cabang">
      <thead>
        <tr>
          <th scope="col">Cabang</th>
          <th scope="col">Kepala cabang</th>
          <th scope="col">Email login</th>
          <th scope="col">Telepon</th>
          <th scope="col">Login terakhir</th>
          <th scope="col">Status</th>
          <th scope="col" />
        </tr>
      </thead>
      <tbody>
        {baris.map((row) => (
          <tr key={row.branch_id}>
            <td>{row.nama}</td>
            <td>{row.nama_kepala}</td>
            <td className="muted">{row.email}</td>
            <td className="tabular">{row.telepon || "—"}</td>
            <td className="muted">{formatWaktu(row.last_login_at)}</td>
            <td>
              <Badge tone={TONE_STATUS_CABANG[row.status]}>{LABEL_STATUS_CABANG[row.status]}</Badge>
            </td>
            <td className="num">
              {row.has_account && row.user_id ? (
                <div className={styles.row}>
                  <Button
                    variant="ghost"
                    disabled={sibuk === row.user_id}
                    onClick={() => onReset(row)}
                  >
                    Kirim link reset
                  </Button>
                  <Button
                    variant="secondary"
                    disabled={sibuk === row.user_id}
                    onClick={() => onUbahAktif(row)}
                  >
                    {row.is_active ? "Nonaktifkan" : "Aktifkan"}
                  </Button>
                </div>
              ) : (
                <span className="muted small">
                  Buat token registrasi di halaman Cabang
                  {row.token_prefix ? ` (token ${row.token_prefix}…)` : ""}
                </span>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}

function TabelAdmin({ baris, onUbah }: { baris: AdminUser[]; onUbah: (row: AdminUser) => void }) {
  if (baris.length === 0) {
    return <EmptyState icon="users-three">Belum ada admin terdaftar.</EmptyState>;
  }
  return (
    <Table mobileCards caption="Akun admin">
      <thead>
        <tr>
          <th scope="col">Nama pengguna</th>
          <th scope="col">Email</th>
          <th scope="col">Role</th>
          <th scope="col">Terima email invoice</th>
          <th scope="col">Terima email surat jalan</th>
          <th scope="col">Status</th>
          <th scope="col" />
        </tr>
      </thead>
      <tbody>
        {baris.map((row) => (
          <tr key={row.id}>
            <td>{row.nama}</td>
            <td className="muted">{row.email}</td>
            <td>{row.role === "superadmin" ? "Superadmin" : "Manager"}</td>
            <td>{row.notif_invoice ? "Ya" : <span className="muted">Tidak</span>}</td>
            <td>{row.notif_surat_jalan ? "Ya" : <span className="muted">Tidak</span>}</td>
            <td>
              <Badge tone={TONE_STATUS_ADMIN[row.status]}>{LABEL_STATUS_ADMIN[row.status]}</Badge>
            </td>
            <td className="num">
              <Button variant="ghost" onClick={() => onUbah(row)}>
                Ubah
              </Button>
            </td>
          </tr>
        ))}
      </tbody>
    </Table>
  );
}
