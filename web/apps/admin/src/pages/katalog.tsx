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

/** Produk pada master katalog (PRD §4.5 AP-KAT-01). */
export interface Produk {
  id: string;
  kode?: string;
  nama: string;
  jenis: "reguler" | "sertifikat";
  satuan: string;
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

/** Satu sel matriks yang berisi harga (sel kosong = tidak dijual). */
interface SelHarga {
  branch_id: string;
  product_id: string;
  harga: number;
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
  satuan: string;
  harga_default: string;
  urutan: string;
  is_active: boolean;
}

const PRODUK_BARU: FormProduk = {
  kode: "",
  nama: "",
  jenis: "reguler",
  satuan: "pcs",
  harga_default: "0",
  urutan: "0",
  is_active: true,
};

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

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
      satuan: form.satuan.trim() || "pcs",
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
        <Table mobileCards caption="Daftar produk">
          <thead>
            <tr>
              <th scope="col">Produk</th>
              <th scope="col">Jenis</th>
              <th scope="col">Satuan</th>
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
                <td>{p.satuan}</td>
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
                          satuan: p.satuan,
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
            <Field label="Satuan" htmlFor="kat-satuan" error={errorFields.satuan}>
              <Input id="kat-satuan" value={form.satuan} onChange={(e) => setForm({ ...form, satuan: e.target.value })} />
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

/** TabMatriks menampilkan grid Produk × Cabang dengan edit inline. */
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

  /** simpanSel menerapkan satu sel lewat batch edit (harga kosong = tidak dijual). */
  async function simpanSel(product: Produk, cabang: KolomCabang, raw: string) {
    const nilai = angka(raw);
    if (nilai !== null && product.jenis === "sertifikat" && nilai !== 0) {
      setGalat("Produk Sertifikat selalu berharga 0.");
      return;
    }
    const sekarang = data?.sel.find((s) => s.branch_id === cabang.id && s.product_id === product.id)?.harga ?? null;
    if (nilai === sekarang) return;
    setSibuk(`${cabang.id}:${product.id}`);
    setGalat(null);
    try {
      await api.put("/admin/prices", { items: [{ branch_id: cabang.id, product_id: product.id, harga: nilai }] });
      setData((lama) => {
        if (!lama) return lama;
        const sel = lama.sel.filter((s) => !(s.branch_id === cabang.id && s.product_id === product.id));
        if (nilai !== null) sel.push({ branch_id: cabang.id, product_id: product.id, harga: nilai });
        return { ...lama, sel };
      });
      tampilkan(
        nilai === null
          ? `${product.nama} tidak dijual ke ${cabang.nama}`
          : `${product.nama} untuk ${cabang.nama} disimpan`,
      );
    } catch (err) {
      setGalat(pesanGalat(err));
      await muat();
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

  const hargaSel = (cabang: string, produk: string): number | null =>
    data?.sel.find((s) => s.branch_id === cabang && s.product_id === produk)?.harga ?? null;

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
                    const nilai = hargaSel(c.id, p.id);
                    return (
                      <td key={c.id}>
                        <Input
                          inputClassName={styles.cell}
                          inputMode="numeric"
                          aria-label={`Harga ${p.nama} untuk ${c.nama}`}
                          placeholder="tidak dijual"
                          defaultValue={nilai === null ? "" : String(nilai)}
                          key={`${c.id}:${nilai === null ? "kosong" : nilai}`}
                          disabled={sibuk === `${c.id}:${p.id}`}
                          onBlur={(e) => void simpanSel(p, c, e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === "Enter") e.currentTarget.blur();
                          }}
                        />
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
        Sel kosong berarti produk <strong>tidak dijual</strong> ke cabang itu: produk tidak tampil di
        portal cabang dan tidak bisa dipesan. Kosongkan isian untuk menghapus harganya.
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
        Salin harga <strong>menggantikan</strong> matriks cabang tujuan dengan isi cabang sumber.
      </p>
    </Card>
  );
}
