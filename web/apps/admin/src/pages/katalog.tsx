import { useCallback, useEffect, useMemo, useState } from "react";

import { ApiClientError, api } from "@grahita/api-client";
import {
  Badge,
  Button,
  Card,
  CardHead,
  EmptyState,
  Field,
  Form,
  Input,
  Modal,
  Notice,
  PageHead,
  Segmented,
  Select,
  SkeletonList,
  SwitchRow,
  Table,
  useToast,
} from "@grahita/ui";

import styles from "./admin-pages.module.css";
import { hitungTarif, persenKeRasio, rasioKePersen } from "./tarif";

/** Produk pada master katalog (PRD §4.5 AP-KAT-01). */
export interface Produk {
  id: string;
  kode?: string;
  nama: string;
  jenis: "reguler" | "sertifikat";
  deskripsi?: string;
  harga_default: number;
  urutan: number;
  is_active: boolean;
}

/** Kolom cabang pada matriks harga. */
interface KolomCabang {
  id: string;
  nama: string;
  is_active: boolean;
}

/**
 * Satu sel matriks yang berisi harga (sel kosong = tidak dijual). `harga`
 * adalah tarif final hitungan server dari HJM, P1 (rasio desimal), dan SPV;
 * sel harga lama tanpa rincian berkomponen null dengan sumber "legacy".
 */
interface SelHarga {
  branch_id: string;
  product_id: string;
  harga: number;
  hjm: number | null;
  p1: string | null;
  spv: number | null;
  sumber: "import" | "manual" | "legacy";
}

/** Sel yang sedang dibuka di editor tarif. */
interface EditSel {
  produk: Produk;
  cabang: KolomCabang;
  sel: SelHarga | null;
}

interface Matriks {
  produk: Produk[];
  cabang: KolomCabang[];
  sel: SelHarga[];
}

interface FormProduk {
  id?: string;
  kode: string;
  nama: string;
  jenis: "reguler" | "sertifikat";
  harga_default: string;
  urutan: string;
  is_active: boolean;
}

const PRODUK_BARU: FormProduk = {
  kode: "",
  nama: "",
  jenis: "reguler",
  harga_default: "0",
  urutan: "0",
  is_active: true,
};

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

const bilangan = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 0 });

function pesanGalat(err: unknown): string {
  if (err instanceof ApiClientError) return err.message;
  return "Terjadi kesalahan. Coba lagi.";
}

/** angka mengubah masukan pengguna menjadi bilangan bulat harga (null = kosong). */
function angka(raw: string): number | null {
  const bersih = raw.replace(/[^0-9]/g, "");
  if (bersih === "") return null;
  return Number.parseInt(bersih, 10);
}

/**
 * KatalogPage — /katalog (Manager & Superadmin): tab Produk untuk CRUD master
 * produk dan tab Matriks harga untuk grid Produk × Cabang (PRD §4.5).
 *
 * Aturan yang tampak di UI tetapi ditegakkan server:
 *  - sel kosong = produk tidak dijual ke cabang itu (tidak muncul di portal
 *    cabang dan tidak bisa dipesan),
 *  - harga sel selalu dihitung server dari HJM + HJM×P1 + SPV; admin hanya
 *    mengisi komponennya,
 *  - produk Sertifikat selalu berharga 0 (harga default maupun harga cabang),
 *  - harga default bukan cadangan katalog: ia hanya nilai awal saat mengisi
 *    sel matriks.
 */
export function KatalogPage() {
  const [tab, setTab] = useState<"produk" | "matriks">("produk");
  return (
    <>
      <PageHead
        title="Katalog product"
        sub="Produk dan matriks harga per cabang"
        actions={
          <Segmented
            label="Tampilan katalog"
            options={[
              { value: "produk", label: "Produk" },
              { value: "matriks", label: "Matriks harga" },
            ]}
            value={tab}
            onChange={setTab}
          />
        }
      />
      {tab === "produk" ? <TabProduk /> : <TabMatriks />}
    </>
  );
}

/** TabProduk menampilkan master produk beserta CRUD-nya. */
export function TabProduk() {
  const { tampilkan } = useToast();
  const [rows, setRows] = useState<Produk[] | null>(null);
  const [memuat, setMemuat] = useState(true);
  const [galat, setGalat] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState("");
  const [form, setForm] = useState<FormProduk | null>(null);
  const [galatForm, setGalatForm] = useState<string | null>(null);
  const [errorFields, setErrorFields] = useState<Record<string, string>>({});

  const [q, setQ] = useState("");
  const [jenis, setJenis] = useState("");
  const [aktif, setAktif] = useState("");

  const query = useMemo(() => {
    const p = new URLSearchParams();
    if (q.trim()) p.set("q", q.trim());
    if (jenis) p.set("jenis", jenis);
    if (aktif) p.set("aktif", aktif);
    const s = p.toString();
    return s ? `?${s}` : "";
  }, [q, jenis, aktif]);

  const muat = useCallback(async () => {
    setMemuat(true);
    try {
      setRows(await api.get<Produk[]>(`/admin/products${query}`));
      setGalat(null);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setMemuat(false);
    }
  }, [query]);

  useEffect(() => {
    void muat();
  }, [muat]);

  async function simpan() {
    if (!form) return;
    setSibuk("form");
    setGalatForm(null);
    setErrorFields({});
    const muatan = {
      kode: form.kode.trim(),
      nama: form.nama.trim(),
      jenis: form.jenis,
      harga_default: angka(form.harga_default) ?? 0,
      urutan: angka(form.urutan) ?? 0,
      is_active: form.is_active,
    };
    try {
      if (form.id) {
        await api.put(`/admin/products/${form.id}`, muatan);
        tampilkan(`Produk ${muatan.nama} disimpan`);
      } else {
        await api.post("/admin/products", muatan);
        tampilkan(`Produk ${muatan.nama} ditambahkan`);
      }
      setForm(null);
      await muat();
    } catch (err) {
      setGalatForm(pesanGalat(err));
      if (err instanceof ApiClientError) setErrorFields(err.mapFields);
    } finally {
      setSibuk("");
    }
  }

  async function ubahAktif(row: Produk) {
    setSibuk(row.id);
    try {
      const hasil = await api.put<Produk>(`/admin/products/${row.id}`, { is_active: !row.is_active });
      setRows((lama) => (lama ?? []).map((r) => (r.id === hasil.id ? hasil : r)));
      tampilkan(`Produk ${hasil.nama} ${hasil.is_active ? "diaktifkan" : "dinonaktifkan"}`);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setSibuk("");
    }
  }

  async function hapus(row: Produk) {
    setSibuk(row.id);
    try {
      await api.delete(`/admin/products/${row.id}`);
      tampilkan(`Produk ${row.nama} dihapus`);
      await muat();
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setSibuk("");
    }
  }

  return (
    <Card>
      <CardHead
        title="Daftar produk"
        tools={
          <div className={styles.row}>
            <Input
              aria-label="Cari produk"
              placeholder="Cari nama atau kode"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
            <Select aria-label="Jenis produk" value={jenis} onChange={(e) => setJenis(e.target.value)}>
              <option value="">Semua jenis</option>
              <option value="reguler">Reguler</option>
              <option value="sertifikat">Sertifikat</option>
            </Select>
            <Select aria-label="Status produk" value={aktif} onChange={(e) => setAktif(e.target.value)}>
              <option value="">Semua status</option>
              <option value="true">Aktif</option>
              <option value="false">Nonaktif</option>
            </Select>
            <Button
              variant="primary"
              icon="plus"
              onClick={() => {
                setGalatForm(null);
                setErrorFields({});
                setForm({ ...PRODUK_BARU });
              }}
            >
              Tambah produk
            </Button>
          </div>
        }
      />
      {galat ? (
        <div style={{ marginBottom: 12 }}>
          <Notice tone="danger">{galat}</Notice>
        </div>
      ) : null}
      {memuat ? (
        <SkeletonList rows={3} />
      ) : !rows || rows.length === 0 ? (
        <EmptyState icon="tag">Belum ada produk yang cocok dengan filter.</EmptyState>
      ) : (
        <Table stack="wide" caption="Daftar produk">
          <thead>
            <tr>
              <th scope="col">Produk</th>
              <th scope="col">Jenis</th>
              <th scope="col" className="num">
                Harga default
              </th>
              <th scope="col">Status</th>
              <th scope="col" />
            </tr>
          </thead>
          <tbody>
            {rows.map((p) => (
              <tr key={p.id}>
                <td>
                  <div className={styles.row}>
                    <span>{p.nama}</span>
                    {p.kode ? <span className="muted small">{p.kode}</span> : null}
                  </div>
                </td>
                <td>
                  {p.jenis === "sertifikat" ? (
                    <Badge tone="info">Sertifikat</Badge>
                  ) : (
                    <span className="muted">Reguler</span>
                  )}
                </td>
                <td className="num">{p.jenis === "sertifikat" ? "Rp0" : rupiah.format(p.harga_default)}</td>
                <td>
                  <Badge tone={p.is_active ? "success" : "draft"}>{p.is_active ? "Aktif" : "Nonaktif"}</Badge>
                </td>
                <td className="num">
                  <div className={styles.row}>
                    <Button
                      variant="ghost"
                      onClick={() => {
                        setGalatForm(null);
                        setErrorFields({});
                        setForm({
                          id: p.id,
                          kode: p.kode ?? "",
                          nama: p.nama,
                          jenis: p.jenis,
                          harga_default: String(p.harga_default),
                          urutan: String(p.urutan),
                          is_active: p.is_active,
                        });
                      }}
                    >
                      Ubah
                    </Button>
                    <Button variant="secondary" disabled={sibuk === p.id} onClick={() => void ubahAktif(p)}>
                      {p.is_active ? "Nonaktifkan" : "Aktifkan"}
                    </Button>
                    <Button variant="ghost" disabled={sibuk === p.id} onClick={() => void hapus(p)}>
                      Hapus
                    </Button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <p className="muted small" style={{ marginTop: 12 }}>
        Produk nonaktif tidak muncul di portal cabang. Produk yang sudah dipakai pesanan tidak bisa
        dihapus &mdash; nonaktifkan saja.
      </p>

      <Modal
        open={form !== null}
        title={form?.id ? "Ubah produk" : "Tambah produk"}
        onClose={() => setForm(null)}
        footer={
          <>
            <Button variant="secondary" onClick={() => setForm(null)}>
              Batal
            </Button>
            <Button
              variant="primary"
              disabled={sibuk === "form" || !form?.nama}
              onClick={() => void simpan()}
            >
              Simpan
            </Button>
          </>
        }
      >
        {form ? (
          <Form>
            {galatForm ? <Notice tone="danger">{galatForm}</Notice> : null}
            <Field label="Nama produk" htmlFor="kat-nama" error={errorFields.nama || errorFields.katalog}>
              <Input id="kat-nama" value={form.nama} onChange={(e) => setForm({ ...form, nama: e.target.value })} />
            </Field>
            <Field label="Kode" htmlFor="kat-kode" hint="Opsional, unik bila diisi." error={errorFields.kode}>
              <Input id="kat-kode" value={form.kode} onChange={(e) => setForm({ ...form, kode: e.target.value })} />
            </Field>
            <Field label="Jenis" htmlFor="kat-jenis">
              <Select
                id="kat-jenis"
                value={form.jenis}
                onChange={(e) => {
                  const j = e.target.value as FormProduk["jenis"];
                  setForm({ ...form, jenis: j, harga_default: j === "sertifikat" ? "0" : form.harga_default });
                }}
              >
                <option value="reguler">Reguler</option>
                <option value="sertifikat">Sertifikat</option>
              </Select>
            </Field>

            <Field
              label="Harga default"
              htmlFor="kat-harga"
              hint={
                form.jenis === "sertifikat"
                  ? "Produk Sertifikat selalu berharga 0."
                  : "Bukan cadangan katalog: harga jual ditentukan matriks per cabang."
              }
              error={errorFields.harga_default}
            >
              <Input
                id="kat-harga"
                inputMode="numeric"
                disabled={form.jenis === "sertifikat"}
                value={form.harga_default}
                onChange={(e) => setForm({ ...form, harga_default: e.target.value })}
              />
            </Field>
            <Field label="Urutan tampil" htmlFor="kat-urutan" error={errorFields.urutan}>
              <Input
                id="kat-urutan"
                inputMode="numeric"
                value={form.urutan}
                onChange={(e) => setForm({ ...form, urutan: e.target.value })}
              />
            </Field>
            <SwitchRow
              checked={form.is_active}
              onChange={(v) => setForm({ ...form, is_active: v })}
              label="Aktif"
              hint="Hanya produk aktif dengan harga cabang yang bisa dipesan."
            />
          </Form>
        ) : null}
      </Modal>
    </Card>
  );
}

/** rincianSel meringkas komponen sel sebagai "HJM · P1 · SPV". */
function rincianSel(sel: SelHarga): string {
  if (sel.hjm === null || sel.p1 === null || sel.spv === null) return "Tanpa rincian";
  return `${bilangan.format(sel.hjm)} · ${rasioKePersen(sel.p1)}% · ${bilangan.format(sel.spv)}`;
}

/** TabMatriks menampilkan grid Produk × Cabang; tiap sel dibuka di editor tarif. */
export function TabMatriks() {
  const { tampilkan } = useToast();
  const [data, setData] = useState<Matriks | null>(null);
  const [memuat, setMemuat] = useState(true);
  const [galat, setGalat] = useState<string | null>(null);
  const [sibuk, setSibuk] = useState("");
  const [q, setQ] = useState("");
  const [branchQ, setBranchQ] = useState("");
  const [sumber, setSumber] = useState("");
  const [tujuan, setTujuan] = useState("");
  const [edit, setEdit] = useState<EditSel | null>(null);

  const query = useMemo(() => {
    const p = new URLSearchParams({ aktif: "true" });
    if (q.trim()) p.set("q", q.trim());
    if (branchQ.trim()) p.set("branch_q", branchQ.trim());
    return `?${p.toString()}`;
  }, [q, branchQ]);

  const muat = useCallback(async () => {
    setMemuat(true);
    try {
      setData(await api.get<Matriks>(`/admin/prices${query}`));
      setGalat(null);
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setMemuat(false);
    }
  }, [query]);

  useEffect(() => {
    void muat();
  }, [muat]);

  /**
   * simpanSel mengirim komponen satu sel lewat batch edit (komponen null =
   * tidak dijual), lalu memuat ulang matriks agar sel menampilkan tarif dan
   * rincian yang dihitung server.
   */
  async function simpanSel(target: EditSel, komponen: { hjm: number; p1: string; spv: number } | null) {
    setSibuk(`${target.cabang.id}:${target.produk.id}`);
    try {
      await api.put("/admin/prices", {
        items: [
          komponen
            ? { branch_id: target.cabang.id, product_id: target.produk.id, ...komponen }
            : { branch_id: target.cabang.id, product_id: target.produk.id, hjm: null },
        ],
      });
      setEdit(null);
      tampilkan(
        komponen === null
          ? `${target.produk.nama} tidak dijual ke ${target.cabang.nama}`
          : `Tarif ${target.produk.nama} untuk ${target.cabang.nama} disimpan`,
      );
      await muat();
      return null;
    } catch (err) {
      return pesanGalat(err);
    } finally {
      setSibuk("");
    }
  }

  async function salin() {
    if (!sumber || !tujuan) return;
    setSibuk("salin");
    setGalat(null);
    try {
      const muatan =
        tujuan === "semua" ? { from_branch_id: sumber, semua: true } : { from_branch_id: sumber, to_branch_ids: [tujuan] };
      const hasil = await api.post<{ jumlah: number; salinan: { branch_id: string }[] }>("/admin/prices/copy", muatan);
      tampilkan(`Harga disalin ke ${hasil.salinan.length} cabang (${hasil.jumlah} produk)`);
      await muat();
    } catch (err) {
      setGalat(pesanGalat(err));
    } finally {
      setSibuk("");
    }
  }

  const selDari = (cabang: string, produk: string): SelHarga | null =>
    data?.sel.find((s) => s.branch_id === cabang && s.product_id === produk) ?? null;

  return (
    <Card>
      <CardHead
        title="Matriks harga"
        tools={
          <div className={styles.row}>
            <Input
              aria-label="Cari produk"
              placeholder="Cari produk"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
            <Input
              aria-label="Cari cabang"
              placeholder="Cari cabang"
              value={branchQ}
              onChange={(e) => setBranchQ(e.target.value)}
            />
          </div>
        }
      />
      {galat ? (
        <div style={{ marginBottom: 12 }}>
          <Notice tone="danger">{galat}</Notice>
        </div>
      ) : null}
      <p className="muted small" style={{ marginBottom: 12 }}>
        Setiap sel berisi tarif per item = <strong>HJM + (HJM × P1) + SPV</strong>, dihitung server dan
        dibulatkan ke rupiah terdekat. Baris kecil di bawah tarif: HJM · P1 · SPV. Klik sel untuk mengubah
        komponennya.
      </p>
      {memuat ? (
        <SkeletonList rows={4} />
      ) : !data || data.produk.length === 0 ? (
        <EmptyState icon="squares-four">Belum ada produk aktif yang cocok dengan filter.</EmptyState>
      ) : data.cabang.length === 0 ? (
        <EmptyState icon="squares-four">Belum ada cabang yang cocok dengan pencarian.</EmptyState>
      ) : (
        <div className={styles.matrixWrap}>
          <table className={styles.matrix} aria-label="Matriks harga produk per cabang">
            <thead>
              <tr>
                <th scope="col">Produk</th>
                {data.cabang.map((c) => (
                  <th scope="col" key={c.id} className={c.is_active ? undefined : styles.offCol}>
                    {c.nama}
                    {!c.is_active ? <span className="muted small"> (nonaktif)</span> : null}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.produk.map((p) => (
                <tr key={p.id}>
                  <th scope="row">
                    <div className={styles.row}>
                      <span>{p.nama}</span>
                      {p.jenis === "sertifikat" ? <Badge tone="info">Sertifikat</Badge> : null}
                    </div>
                  </th>
                  {data.cabang.map((c) => {
                    const sel = selDari(c.id, p.id);
                    return (
                      <td key={c.id}>
                        <button
                          type="button"
                          className={sel ? styles.selTarif : `${styles.selTarif} ${styles.selKosong}`}
                          aria-label={`Tarif ${p.nama} untuk ${c.nama}`}
                          title={sel ? `HJM · P1 · SPV: ${rincianSel(sel)}` : "Tidak dijual ke cabang ini"}
                          disabled={sibuk === `${c.id}:${p.id}`}
                          onClick={() => setEdit({ produk: p, cabang: c, sel })}
                        >
                          {sel ? (
                            <>
                              <span className={styles.selHarga}>{rupiah.format(sel.harga)}</span>
                              <span className={styles.selRincian}>{rincianSel(sel)}</span>
                            </>
                          ) : (
                            <span className={styles.selRincian}>Tidak dijual</span>
                          )}
                        </button>
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="muted small" style={{ marginTop: 12 }}>
        Sel <strong>Tidak dijual</strong> berarti produk tidak tampil di portal cabang dan tidak bisa
        dipesan. Setiap perubahan tersimpan sebagai versi tarif baru dan tercatat di audit log.
      </p>

      <div className={styles.salinBar}>
        <Field label="Salin harga dari" htmlFor="kat-sumber">
          <Select id="kat-sumber" value={sumber} onChange={(e) => setSumber(e.target.value)}>
            <option value="">Pilih cabang sumber</option>
            {(data?.cabang ?? []).map((c) => (
              <option key={c.id} value={c.id}>
                {c.nama}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Ke" htmlFor="kat-tujuan">
          <Select id="kat-tujuan" value={tujuan} onChange={(e) => setTujuan(e.target.value)}>
            <option value="">Pilih tujuan</option>
            <option value="semua">Semua cabang lain</option>
            {(data?.cabang ?? []).map((c) => (
              <option key={c.id} value={c.id}>
                {c.nama}
              </option>
            ))}
          </Select>
        </Field>
        <Button variant="primary" disabled={!sumber || !tujuan || sibuk === "salin"} onClick={() => void salin()}>
          Salin harga
        </Button>
      </div>
      <p className="muted small">
        Salin harga <strong>menggantikan</strong> matriks cabang tujuan dengan isi cabang sumber, termasuk HJM,
        P1, dan SPV-nya.
      </p>

      {edit ? (
        <EditorTarif
          key={`${edit.cabang.id}:${edit.produk.id}`}
          edit={edit}
          sibuk={sibuk === `${edit.cabang.id}:${edit.produk.id}`}
          onTutup={() => setEdit(null)}
          onSimpan={(komponen) => simpanSel(edit, komponen)}
        />
      ) : null}
    </Card>
  );
}

/**
 * EditorTarif mengubah komponen satu sel: HJM dan SPV dalam Rupiah, P1 dalam
 * persen. Tarif final di pratinjau dihitung dengan rumus yang sama seperti
 * server; yang tersimpan tetap hasil hitungan server.
 */
function EditorTarif({
  edit,
  sibuk,
  onTutup,
  onSimpan,
}: {
  edit: EditSel;
  sibuk: boolean;
  onTutup: () => void;
  onSimpan: (komponen: { hjm: number; p1: string; spv: number } | null) => Promise<string | null>;
}) {
  const { produk, cabang, sel } = edit;
  const sertifikat = produk.jenis === "sertifikat";
  const tanpaRincian = sel !== null && (sel.hjm === null || sel.p1 === null || sel.spv === null);
  const [hjm, setHjm] = useState(
    sertifikat ? "0" : sel ? String(sel.hjm ?? sel.harga) : produk.harga_default ? String(produk.harga_default) : "",
  );
  const [p1, setP1] = useState(sel?.p1 ? rasioKePersen(sel.p1) : "0");
  const [spv, setSpv] = useState(sel?.spv !== null && sel?.spv !== undefined ? String(sel.spv) : "0");
  const [galat, setGalat] = useState<string | null>(null);

  const nilaiHjm = angka(hjm);
  const nilaiSpv = angka(spv) ?? 0;
  const rasio = persenKeRasio(p1);
  const final = nilaiHjm !== null && rasio !== null ? hitungTarif(nilaiHjm, rasio, nilaiSpv) : null;

  async function simpan() {
    if (nilaiHjm === null) {
      setGalat("Isi HJM. Untuk berhenti menjual produk ke cabang ini, pakai tombol Tidak dijual.");
      return;
    }
    if (rasio === null) {
      setGalat("P1 harus persen 0–100, maksimal empat angka di belakang koma.");
      return;
    }
    setGalat(await onSimpan({ hjm: nilaiHjm, p1: rasio, spv: nilaiSpv }));
  }

  async function tidakDijual() {
    setGalat(await onSimpan(null));
  }

  return (
    <Modal
      open
      title={`Tarif ${produk.nama}`}
      onClose={onTutup}
      footer={
        <>
          {sel ? (
            <Button variant="danger-ghost" disabled={sibuk} onClick={() => void tidakDijual()}>
              Tidak dijual
            </Button>
          ) : null}
          <Button variant="secondary" onClick={onTutup}>
            Batal
          </Button>
          <Button variant="primary" disabled={sibuk || final === null} onClick={() => void simpan()}>
            {sibuk ? "Menyimpan…" : "Simpan"}
          </Button>
        </>
      }
    >
      <Form
        onSubmit={(e) => {
          e.preventDefault();
          void simpan();
        }}
      >
        <p className="muted small">{cabang.nama}</p>
        {galat ? <Notice tone="danger">{galat}</Notice> : null}
        {sertifikat ? (
          <Notice tone="info">Produk Sertifikat selalu berharga 0; biaya packaging ditambahkan per pesanan.</Notice>
        ) : null}
        {tanpaRincian ? (
          <Notice tone="info">
            Harga lama ini belum punya rincian. Isian awal memakai HJM = harga sekarang; sesuaikan P1 dan SPV
            lalu simpan.
          </Notice>
        ) : null}
        <div className={styles.tarifGrid}>
          <Field label="HJM (Rp)" htmlFor="tarif-hjm" hint="Harga jual dasar per item">
            <Input
              id="tarif-hjm"
              inputMode="numeric"
              value={hjm}
              disabled={sertifikat}
              onChange={(e) => setHjm(e.target.value)}
            />
          </Field>
          <Field label="P1 (%)" htmlFor="tarif-p1" hint="Persentase dari HJM">
            <Input
              id="tarif-p1"
              inputMode="decimal"
              value={p1}
              disabled={sertifikat}
              onChange={(e) => setP1(e.target.value)}
            />
          </Field>
          <Field label="SPV (Rp)" htmlFor="tarif-spv" hint="Biaya supervisi per item">
            <Input
              id="tarif-spv"
              inputMode="numeric"
              value={spv}
              disabled={sertifikat}
              onChange={(e) => setSpv(e.target.value)}
            />
          </Field>
        </div>
        <div className={styles.tarifPratinjau} aria-live="polite">
          <span className="muted small">Tarif per item</span>
          <strong className="tabular" data-testid="pratinjau-tarif">
            {final === null ? "—" : rupiah.format(final)}
          </strong>
          <span className="muted small">HJM + (HJM × P1) + SPV, dibulatkan ke rupiah terdekat</span>
        </div>
      </Form>
    </Modal>
  );
}
