import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate } from "@tanstack/react-router";

import { ApiClientError, api } from "@grahita/api-client";
import {
  Badge,
  Button,
  Card,
  CardHead,
  EmptyState,
  Field,
  Input,
  Modal,
  Notice,
  PageHead,
  Segmented,
  Select,
  SkeletonList,
  SwitchRow,
  Table,
  Textarea,
} from "@grahita/ui";

import { useApi } from "../useApi";
import styles from "./pesanan.module.css";

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

/** Status pesanan (Database.md §3). Tidak ada status Batal di sistem. */
export type StatusPesanan = "draft" | "submitted" | "done";

export interface ProdukKatalog {
  id: string;
  nama: string;
  jenis: string;
  satuan: string;
  deskripsi?: string;
  harga: number;
  /** Hanya pada produk Sertifikat: biaya packaging per pesanan (PRD CP-KAT-04). */
  biaya_packaging?: number;
}

export interface BarisPesanan {
  line_no: number;
  jenis: string;
  product_id?: string;
  nama: string;
  satuan: string;
  qty: number;
  harga_satuan: number;
  subtotal: number;
  serial_number?: string;
}

export interface Pesanan {
  id: string;
  nomor?: string;
  status: StatusPesanan;
  verif_status?: string;
  versi?: number;
  is_cnc: boolean;
  is_sertifikat?: boolean;
  tanggal?: string;
  jatuh_tempo?: string;
  catatan?: string;
  total: number;
  jumlah_item: number;
  created_at: string;
  updated_at?: string;
  lines?: BarisPesanan[];
}

/**
 * BuktiTransfer adalah metadata satu bukti transfer (F1-13). Isi berkasnya
 * tidak pernah ada di sini: unduhan selalu lewat tautan berumur pendek dari
 * endpoint `.../download`.
 */
export interface BuktiTransfer {
  id: string;
  order_id: string;
  nama_file: string;
  mime_type: string;
  ukuran_byte: number;
  sha256: string;
  uploaded_by_kind: "branch" | "admin";
  uploaded_by_nama?: string;
  created_at: string;
}

/** TautanBukti adalah presigned URL berumur pendek untuk pratinjau/unduh. */
export interface TautanBukti {
  url: string;
  expires_in: number;
  nama_file: string;
  mime_type: string;
}

/** penandaUnggah menampilkan siapa yang mengunggah bukti (cabang atau admin). */
export function penandaUnggah(b: Pick<BuktiTransfer, "uploaded_by_kind" | "uploaded_by_nama">): string {
  if (b.uploaded_by_kind === "admin") {
    return b.uploaded_by_nama ? `Admin ${b.uploaded_by_nama}` : "Admin Pusat";
  }
  return "Cabang";
}

/** ukuranTampil menampilkan ukuran berkas secara ringkas. */
export function ukuranTampil(byte: number): string {
  if (byte >= 1024 * 1024) return `${(byte / (1024 * 1024)).toFixed(1)} MB`;
  return `${Math.max(1, Math.round(byte / 1024))} KB`;
}

const FILTER_STATUS = [
  { value: "semua", label: "Semua" },
  { value: "draft", label: "Draft" },
  { value: "submitted", label: "Menunggu" },
  { value: "done", label: "Selesai" },
] as const;

/** labelStatus adalah teks chip status pesanan. */
function labelStatus(status: StatusPesanan, verif?: string): string {
  if (status === "draft") return "Draft";
  if (status === "done") return "Selesai";
  return verif === "perlu_review" ? "Perlu review" : "Menunggu verifikasi";
}

function toneStatus(status: StatusPesanan, verif?: string): "draft" | "warning" | "success" | "danger" {
  if (status === "draft") return "draft";
  if (status === "done") return "success";
  return verif === "perlu_review" ? "danger" : "warning";
}

/** judulPesanan memakai nomor pesanan; draft belum punya nomor. */
export function judulPesanan(o: Pick<Pesanan, "nomor" | "status">): string {
  return o.nomor && o.nomor !== "" ? o.nomor : "Draft";
}

/** tanggalTampil memilih tanggal submit (bila ada) atau tanggal dibuat. */
function tanggalTampil(o: Pesanan): string {
  const sumber = o.tanggal ?? o.created_at;
  if (!sumber) return "—";
  const t = new Date(o.tanggal ? `${sumber}T00:00:00` : sumber);
  if (Number.isNaN(t.getTime())) return sumber;
  return t.toLocaleDateString("id-ID", { day: "2-digit", month: "short", year: "numeric" });
}

/**
 * tanggalWIB menampilkan tanggal bisnis (YYYY-MM-DD dari server) dalam format
 * Indonesia tanpa menggeser harinya — dipakai untuk jatuh tempo C&C, yang
 * sudah dihitung server memakai zona Asia/Jakarta (PRD CP-CNC-04).
 */
function tanggalWIB(tanggal: string): string {
  const t = new Date(`${tanggal}T00:00:00`);
  if (Number.isNaN(t.getTime())) return tanggal;
  return t.toLocaleDateString("id-ID", { day: "2-digit", month: "short", year: "numeric" });
}

/** galatPesan menerjemahkan error API menjadi pesan yang bisa dibaca pengguna. */
function galatPesan(err: unknown): string {
  if (err instanceof ApiClientError) return err.message;
  return "Tidak bisa menyelesaikan permintaan. Coba lagi.";
}

// ---------------------------------------------------------------- daftar ----

/**
 * PesananPage — /pesanan: daftar pesanan cabang dengan filter status.
 *
 * Daftar datang dari GET /api/v1/orders yang dibatasi branch_id SESI, jadi
 * pesanan cabang lain tidak pernah tampil di sini (PRD CP-ORD-01).
 */
export function PesananPage({ statusAwal }: { statusAwal?: string }) {
  const navigate = useNavigate();
  const [filter, setFilter] = useState<string>(
    FILTER_STATUS.some((f) => f.value === statusAwal) ? (statusAwal as string) : "semua",
  );
  const path = filter === "semua" ? "/orders" : `/orders?status=${filter}`;
  const { data, memuat, galat } = useApi<Pesanan[]>(path);

  return (
    <>
      <PageHead
        title="Pesanan"
        sub={data ? `${data.length} pesanan pada filter ini` : "Draft, pesanan menunggu verifikasi, dan yang sudah selesai"}
        actions={
          <Button variant="primary" icon="plus" onClick={() => void navigate({ to: "/pesanan/baru" })}>
            Buat pesanan
          </Button>
        }
      />
      <Card>
        <CardHead
          title="Daftar pesanan"
          tools={
            <Segmented
              label="Status pesanan"
              options={FILTER_STATUS}
              value={filter}
              onChange={(v) => {
                setFilter(v);
                // Filter disimpan di URL search param supaya bisa di-bookmark
                // (SystemDesign.md §7.2).
                void navigate({
                  to: "/pesanan",
                  search: v === "semua" ? {} : { status: v },
                });
              }}
            />
          }
        />
        <Table mobileCards caption="Daftar pesanan cabang">
          <thead>
            <tr>
              <th scope="col">Nomor pesanan</th>
              <th scope="col">Tanggal</th>
              <th scope="col" className="num">
                Item
              </th>
              <th scope="col" className="num">
                Total
              </th>
              <th scope="col">Status</th>
            </tr>
          </thead>
          <tbody>
            {memuat ? (
              <tr>
                <td colSpan={5}>
                  <SkeletonList rows={3} />
                </td>
              </tr>
            ) : galat ? (
              <tr>
                <td colSpan={5}>
                  <Notice tone="danger">{galat}</Notice>
                </td>
              </tr>
            ) : !data || data.length === 0 ? (
              <tr>
                <td colSpan={5}>
                  <EmptyState icon="receipt">
                    {filter === "draft"
                      ? "Tidak ada draft. Pesanan yang disimpan tanpa dikirim muncul di sini."
                      : "Belum ada pesanan di status ini."}
                  </EmptyState>
                </td>
              </tr>
            ) : (
              data.map((o) => (
                <tr key={o.id}>
                  <td>
                    <Link to="/pesanan/$id" params={{ id: o.id }} className={styles.nomor}>
                      {judulPesanan(o)}
                    </Link>
                    {o.is_cnc ? <Badge tone="cnc">C&amp;C</Badge> : null}
                  </td>
                  <td className={styles.muted}>
                    {o.status === "draft" ? `Dibuat ${tanggalTampil(o)}` : tanggalTampil(o)}
                  </td>
                  <td className="num">{o.jumlah_item}</td>
                  <td className="num">
                    <b>{rupiah.format(o.total)}</b>
                  </td>
                  <td>
                    <Badge tone={toneStatus(o.status, o.verif_status)}>
                      {labelStatus(o.status, o.verif_status)}
                    </Badge>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </Table>
      </Card>
    </>
  );
}

// --------------------------------------------------------------- detail ----

/**
 * PesananDetailPage — /pesanan/{id}: header, baris pesanan, total, dan aksi
 * draft (ubah / hapus). Pesanan cabang lain dijawab 404 oleh API.
 */
export function PesananDetailPage({ id }: { id: string }) {
  const navigate = useNavigate();
  const { data, memuat, galat, muatUlang } = useApi<Pesanan>(`/orders/${id}`);
  const [konfirmasi, setKonfirmasi] = useState(false);
  const [hapusGalat, setHapusGalat] = useState<string | null>(null);
  const [menghapus, setMenghapus] = useState(false);
  const [kirim, setKirim] = useState(false);
  const [pesanKirim, setPesanKirim] = useState<string | null>(null);

  async function hapusDraft() {
    setMenghapus(true);
    setHapusGalat(null);
    try {
      await api.delete(`/orders/${id}`);
      await navigate({ to: "/pesanan" });
    } catch (err) {
      setHapusGalat(galatPesan(err));
      setMenghapus(false);
      setKonfirmasi(false);
      muatUlang();
    }
  }

  if (memuat) {
    return (
      <>
        <PageHead title="Detail pesanan" />
        <Card>
          <SkeletonList rows={4} />
        </Card>
      </>
    );
  }
  if (galat || !data) {
    return (
      <>
        <PageHead title="Detail pesanan" />
        <Card>
          <Notice tone="danger">{galat ?? "Pesanan tidak ditemukan."}</Notice>
          <div className={styles.tools}>
            <Button variant="secondary" icon="arrow-left" onClick={() => void navigate({ to: "/pesanan" })}>
              Kembali ke pesanan
            </Button>
          </div>
        </Card>
      </>
    );
  }

  const draft = data.status === "draft";
  const tampilSerial = !draft;

  return (
    <>
      <Link to="/pesanan" className={styles.back}>
        <i className="ph ph-arrow-left" aria-hidden="true" /> Pesanan
      </Link>
      <PageHead
        title={judulPesanan(data)}
        sub={draft ? "Draft belum dikirim dan belum mendapat nomor pesanan" : `Tanggal pesanan ${tanggalTampil(data)}`}
        actions={
          draft ? (
            <div className={styles.tools}>
              <Button variant="secondary" icon="trash" onClick={() => setKonfirmasi(true)}>
                Hapus draft
              </Button>
              <Button
                variant="secondary"
                icon="pencil-simple"
                onClick={() => void navigate({ to: "/pesanan/$id/ubah", params: { id: data.id } })}
              >
                Ubah
              </Button>
              <Button variant="primary" icon="paper-plane-tilt" onClick={() => setKirim(true)}>
                Kirim pesanan
              </Button>
            </div>
          ) : undefined
        }
      />

      <div className={styles.detailGrid}>
        <div className={styles.stack}>
          {draft ? (
            <Notice tone="info">
              Draft belum mendapat nomor pesanan. Nomor dan serial number dibuat saat pesanan dikirim.
            </Notice>
          ) : (
            <Notice tone="warning">
              Pesanan sudah dikirim sehingga tidak dapat diubah atau dihapus dari portal cabang.
            </Notice>
          )}
          {hapusGalat ? <Notice tone="danger">{hapusGalat}</Notice> : null}
          {pesanKirim ? <Notice tone="success">{pesanKirim}</Notice> : null}

          <div className={styles.tools}>
            <Badge tone={toneStatus(data.status, data.verif_status)}>
              {labelStatus(data.status, data.verif_status)}
            </Badge>
            {data.is_cnc ? <Badge tone="cnc">Cash &amp; Carry</Badge> : null}
            {data.versi ? <Badge tone="rev">Revisi {data.versi}</Badge> : null}
            {data.is_sertifikat ? <Badge tone="info">Sertifikat</Badge> : null}
          </div>

          <Card>
            <dl className={styles.metaGrid}>
              <div>
                <dt>{draft ? "Dibuat" : "Tanggal pesanan"}</dt>
                <dd>{tanggalTampil(data)}</dd>
              </div>
              {data.jatuh_tempo ? (
                <div>
                  <dt>Jatuh tempo C&amp;C</dt>
                  <dd>{tanggalWIB(data.jatuh_tempo)}</dd>
                </div>
              ) : null}
              <div>
                <dt>Catatan</dt>
                <dd className={styles.muted}>{data.catatan || "—"}</dd>
              </div>
            </dl>
          </Card>

          <Card>
            <CardHead title="Item" />
            <Table caption="Baris pesanan">
              <thead>
                <tr>
                  <th scope="col">Produk</th>
                  {tampilSerial ? <th scope="col">Serial number</th> : null}
                  <th scope="col" className="num">
                    Qty
                  </th>
                  <th scope="col" className="num">
                    Harga
                  </th>
                  <th scope="col" className="num">
                    Subtotal
                  </th>
                </tr>
              </thead>
              <tbody>
                {(data.lines ?? []).map((l) => (
                  <tr key={l.line_no}>
                    <td>
                      {l.jenis === "packaging" ? (
                        <>
                          <i className="ph ph-lock-simple muted" aria-hidden="true" />{" "}
                        </>
                      ) : null}
                      {l.nama}
                    </td>
                    {tampilSerial ? <td className={styles.muted}>{l.serial_number || "—"}</td> : null}
                    <td className="num">{l.qty}</td>
                    <td className="num">{rupiah.format(l.harga_satuan)}</td>
                    <td className="num">{rupiah.format(l.subtotal)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot>
                <tr>
                  <td colSpan={tampilSerial ? 4 : 3}>Total</td>
                  <td className="num">
                    <b>{rupiah.format(data.total)}</b>
                  </td>
                </tr>
              </tfoot>
            </Table>
          </Card>

          {data.status !== "draft" ? (
            <KartuBukti id={data.id} bolehUnggah={data.status === "submitted"} />
          ) : null}
        </div>
      </div>

      <Modal
        open={konfirmasi}
        onClose={() => setKonfirmasi(false)}
        title="Hapus draft ini?"
        footer={
          <>
            <Button variant="secondary" onClick={() => setKonfirmasi(false)} disabled={menghapus}>
              Batal
            </Button>
            <Button variant="primary" onClick={() => void hapusDraft()} disabled={menghapus}>
              {menghapus ? "Menghapus…" : "Hapus draft"}
            </Button>
          </>
        }
      >
        <p className="muted small">
          Draft yang dihapus hilang permanen. Pesanan yang sudah dikirim tidak bisa dihapus dari portal
          cabang.
        </p>
      </Modal>

      <ModalKirim
        open={kirim}
        onClose={() => setKirim(false)}
        pesanan={data}
        onSukses={() => {
          setKirim(false);
          setPesanKirim(
            "Pesanan sudah dikirim. Nomor pesanan dan serial number sudah terbit dan pesanan terkunci.",
          );
          muatUlang();
        }}
      />
    </>
  );
}

// -------------------------------------------------------------- kirim ------

/** CabangRingkas adalah bagian data cabang yang dipakai modal kirim. */
interface CabangRingkas {
  is_cnc: boolean;
}

/**
 * ModalKirim mengirim draft menjadi pesanan resmi (F1-14): satu permintaan
 * multipart berisi berkas bukti transfer dan penanda Cash & Carry.
 *
 * Aturan yang ditampilkan di sini hanya membantu pengguna; yang MENGIKAT tetap
 * server: pesanan non-C&C wajib punya minimal satu bukti (kalau tidak, API
 * menjawab 400 dan pesanan tetap draft), dan pilihan C&C hanya sah untuk
 * cabang berflag `cash_and_carry` — karena itu centangnya hanya muncul bila
 * flag cabang (dibaca server lewat /me/branch) memang aktif.
 */
function ModalKirim({
  open,
  onClose,
  pesanan,
  onSukses,
}: {
  open: boolean;
  onClose: () => void;
  pesanan: Pesanan;
  onSukses: () => void;
}) {
  const { data: cabang } = useApi<CabangRingkas>("/me/branch", open);
  const [cnc, setCnc] = useState(false);
  const [berkas, setBerkas] = useState<File[]>([]);
  const [galat, setGalat] = useState<string | null>(null);
  const [mengirim, setMengirim] = useState(false);

  const bisaCNC = cabang?.is_cnc === true;
  const wajibBukti = !(bisaCNC && cnc);

  function tutup() {
    if (mengirim) return;
    setGalat(null);
    onClose();
  }

  async function kirim() {
    if (wajibBukti && berkas.length === 0) {
      setGalat("Pesanan wajib disertai minimal satu bukti transfer (JPG, PNG, atau PDF).");
      return;
    }
    setMengirim(true);
    setGalat(null);
    try {
      const form = new FormData();
      for (const f of berkas) form.append("file", f);
      form.append("is_cnc", bisaCNC && cnc ? "true" : "false");
      await api.postForm<Pesanan>(`/orders/${pesanan.id}/submit`, form);
      setBerkas([]);
      setCnc(false);
      onSukses();
    } catch (err) {
      setGalat(galatPesan(err));
    } finally {
      setMengirim(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={tutup}
      title="Kirim pesanan"
      footer={
        <>
          <Button variant="secondary" onClick={tutup} disabled={mengirim}>
            Batal
          </Button>
          <Button
            variant="primary"
            icon="paper-plane-tilt"
            onClick={() => void kirim()}
            disabled={mengirim}
          >
            {mengirim ? "Mengirim…" : "Kirim pesanan"}
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void kirim();
        }}
        data-testid="form-kirim"
      >
        <Notice tone="info">
          Setelah dikirim, nomor pesanan dan serial number dibuat, harga dikunci sesuai daftar harga
          cabang saat ini, dan pesanan tidak dapat diubah atau dihapus lagi dari portal cabang.
        </Notice>
        {galat ? <Notice tone="danger">{galat}</Notice> : null}

        {bisaCNC ? (
          <SwitchRow
            checked={cnc}
            onChange={setCnc}
            label="Cash & Carry"
            hint="Bukti transfer menjadi opsional. Jatuh tempo dihitung server setelah pesanan dikirim."
          />
        ) : null}

        <Field
          label={
            wajibBukti
              ? "Bukti transfer (JPG/PNG/PDF, minimal satu berkas)"
              : "Bukti transfer (opsional untuk Cash & Carry)"
          }
          htmlFor="berkas-kirim"
          hint={
            wajibBukti
              ? "Boleh beberapa berkas bila pembayaran dicicil."
              : "Boleh dikosongkan; bukti bisa ditambahkan setelah pesanan dikirim."
          }
        >
          <Input
            id="berkas-kirim"
            type="file"
            multiple
            accept=".jpg,.jpeg,.png,.pdf,image/jpeg,image/png,application/pdf"
            onChange={(e) => setBerkas(Array.from(e.target.files ?? []))}
          />
        </Field>
      </form>
    </Modal>
  );
}

// -------------------------------------------------------------- bukti ------

/**
 * KartuBukti menampilkan bukti transfer pesanan beserta pengunggahnya dan
 * memberi pratinjau/zoom lewat tautan unduh berumur pendek (F1-13).
 *
 * Berkas hanya boleh ditambah oleh cabang pada pesanan yang sudah dikirim
 * (pembayaran cicilan/C&C, PRD CP-ORD-07). Validasi ukuran/jumlah yang
 * mengikat tetap di server: batas `max_file`/`max_mb` dibaca dari setelan
 * server, dan UI hanya menampilkan pesan kesalahan yang dikembalikan API
 * (tidak ada angka batas yang ditulis ulang di klien).
 */
function KartuBukti({ id, bolehUnggah }: { id: string; bolehUnggah: boolean }) {
  const { data, memuat, galat, muatUlang } = useApi<BuktiTransfer[]>(`/orders/${id}/proofs`);
  const [pratinjau, setPratinjau] = useState<TautanBukti | null>(null);
  const [zoom, setZoom] = useState(false);
  const [galatLihat, setGalatLihat] = useState<string | null>(null);
  const [berkas, setBerkas] = useState<File[]>([]);
  const [galatUnggah, setGalatUnggah] = useState<string | null>(null);
  const [mengunggah, setMengunggah] = useState(false);

  const daftar = data ?? [];

  async function lihat(b: BuktiTransfer) {
    setGalatLihat(null);
    try {
      const tautan = await api.get<TautanBukti>(`/orders/${id}/proofs/${b.id}/download`);
      setZoom(false);
      setPratinjau(tautan);
    } catch (err) {
      setGalatLihat(galatPesan(err));
    }
  }

  async function unggah(e: FormEvent) {
    e.preventDefault();
    if (berkas.length === 0) {
      setGalatUnggah("Pilih minimal satu berkas bukti (JPG, PNG, atau PDF).");
      return;
    }
    setMengunggah(true);
    setGalatUnggah(null);
    try {
      const form = new FormData();
      for (const f of berkas) form.append("file", f);
      await api.postForm<BuktiTransfer[]>(`/orders/${id}/proofs`, form);
      setBerkas([]);
      muatUlang();
    } catch (err) {
      setGalatUnggah(galatPesan(err));
    } finally {
      setMengunggah(false);
    }
  }

  return (
    <Card>
      <CardHead title="Bukti transfer" />
      {memuat ? <SkeletonList rows={2} /> : null}
      {galat ? <Notice tone="danger">{galat}</Notice> : null}
      {galatLihat ? <Notice tone="danger">{galatLihat}</Notice> : null}
      {!memuat && !galat && daftar.length === 0 ? (
        <p className="muted small" data-testid="bukti-kosong">
          Belum ada bukti transfer pada pesanan ini.
        </p>
      ) : null}
      {daftar.length > 0 ? (
        <Table caption="Bukti transfer">
          <thead>
            <tr>
              <th scope="col">Berkas</th>
              <th scope="col">Pengunggah</th>
              <th scope="col" className="num">
                Ukuran
              </th>
              <th scope="col">Aksi</th>
            </tr>
          </thead>
          <tbody>
            {daftar.map((b) => (
              <tr key={b.id} data-testid="baris-bukti">
                <td>{b.nama_file}</td>
                <td className={styles.muted}>{penandaUnggah(b)}</td>
                <td className="num">{ukuranTampil(b.ukuran_byte)}</td>
                <td>
                  <Button variant="ghost" icon="magnifying-glass-plus" onClick={() => void lihat(b)}>
                    Lihat
                  </Button>
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      ) : null}

      {bolehUnggah ? (
        <form className={styles.unggahBukti} onSubmit={(e) => void unggah(e)}>
          <Field
            label="Tambah bukti (JPG/PNG/PDF, boleh beberapa)"
            htmlFor="berkas-bukti"
            error={galatUnggah}
          >
            <Input
              id="berkas-bukti"
              type="file"
              multiple
              accept=".jpg,.jpeg,.png,.pdf,image/jpeg,image/png,application/pdf"
              onChange={(e) => setBerkas(Array.from(e.target.files ?? []))}
            />
          </Field>
          <Button variant="primary" type="submit" icon="upload-simple" disabled={mengunggah}>
            {mengunggah ? "Mengunggah…" : "Unggah bukti"}
          </Button>
        </form>
      ) : null}

      <Modal
        open={pratinjau !== null}
        onClose={() => setPratinjau(null)}
        title={pratinjau?.nama_file ?? "Bukti transfer"}
        footer={
          <>
            <Button variant="secondary" onClick={() => setPratinjau(null)}>
              Tutup
            </Button>
            {pratinjau ? (
              <a className="btn btn-primary" href={pratinjau.url} target="_blank" rel="noreferrer">
                Unduh
              </a>
            ) : null}
          </>
        }
      >
        {pratinjau && pratinjau.mime_type === "application/pdf" ? (
          <p className="muted small">
            Berkas PDF dibuka di tab baru lewat tautan unduh berumur pendek (
            {Math.round(pratinjau.expires_in / 60)} menit).
          </p>
        ) : null}
        {pratinjau && pratinjau.mime_type !== "application/pdf" ? (
          <img
            src={pratinjau.url}
            alt={pratinjau.nama_file}
            className={zoom ? styles.buktiZoom : styles.buktiPratinjau}
            onClick={() => setZoom((z) => !z)}
            data-testid="pratinjau-bukti"
          />
        ) : null}
      </Modal>
    </Card>
  );
}

// --------------------------------------------------------------- editor ----

interface BarisEditor {
  kunci: number;
  productId: string;
  qty: number;
}

/**
 * PesananEditorPage — /pesanan/baru dan /pesanan/{id}/ubah.
 *
 * Editor mengirim HANYA product_id dan qty: harga satuan diambil server dari
 * daftar harga cabang, dan total dihitung ulang server saat menyimpan. Angka
 * total yang tampil di sini hanya pratinjau (PRD CP-ORD-02, BR-10).
 */
export function PesananEditorPage({ id }: { id?: string }) {
  const navigate = useNavigate();
  const katalog = useApi<ProdukKatalog[]>("/catalog");
  // Draft hanya dimuat saat mengubah pesanan yang sudah ada.
  const pesanan = useApi<Pesanan>(id ? `/orders/${id}` : "/orders", Boolean(id));

  const [baris, setBaris] = useState<BarisEditor[]>([{ kunci: 1, productId: "", qty: 1 }]);
  const [catatan, setCatatan] = useState("");
  const [galat, setGalat] = useState<string | null>(null);
  const [menyimpan, setMenyimpan] = useState(false);
  const [siap, setSiap] = useState(false);

  // Isi editor dari draft yang sedang diubah (hanya baris produk).
  useEffect(() => {
    if (!id || !pesanan.data || siap) return;
    const barisAwal = (pesanan.data.lines ?? [])
      .filter((l) => l.jenis === "product" && l.product_id)
      .map((l, i) => ({ kunci: i + 1, productId: l.product_id as string, qty: l.qty }));
    setBaris(barisAwal.length > 0 ? barisAwal : [{ kunci: 1, productId: "", qty: 1 }]);
    setCatatan(pesanan.data.catatan ?? "");
    setSiap(true);
  }, [id, pesanan.data, siap]);

  const produk = katalog.data ?? [];
  const perId = useMemo(() => new Map(produk.map((p) => [p.id, p])), [produk]);
  const adaSertifikat = baris.some((b) => perId.get(b.productId)?.jenis === "sertifikat");
  const adaReguler = baris.some((b) => {
    const p = perId.get(b.productId);
    return p !== undefined && p.jenis !== "sertifikat";
  });
  // Biaya packaging pesanan Sertifikat (CP-SER-02): nilainya datang dari
  // katalog server (setelan `biaya_packaging` yang berlaku), dan barisnya
  // dibuat server saat draft disimpan — editor hanya menampilkannya sebagai
  // baris terkunci agar totalnya sama dengan perhitungan server.
  const biayaPackaging = adaSertifikat
    ? produk.find((p) => p.jenis === "sertifikat")?.biaya_packaging ?? 0
    : 0;
  const total =
    baris.reduce((s, b) => {
      const p = perId.get(b.productId);
      return s + (p ? p.harga * (Number(b.qty) || 0) : 0);
    }, 0) + biayaPackaging;

  const bolehTambah = !adaSertifikat && baris.length < 100;

  const ubahBaris = useCallback((kunci: number, patch: Partial<BarisEditor>) => {
    setBaris((lama) => lama.map((b) => (b.kunci === kunci ? { ...b, ...patch } : b)));
  }, []);

  const tambahBaris = useCallback(() => {
    setBaris((lama) => [...lama, { kunci: Date.now(), productId: "", qty: 1 }]);
  }, []);

  const hapusBaris = useCallback((kunci: number) => {
    setBaris((lama) => (lama.length === 1 ? lama : lama.filter((b) => b.kunci !== kunci)));
  }, []);

  async function simpan() {
    setGalat(null);
    const dipakai = baris.filter((b) => b.productId !== "");
    if (dipakai.length === 0) {
      setGalat("Pilih minimal satu produk sebelum menyimpan draft.");
      return;
    }
    if (dipakai.some((b) => !(Number(b.qty) >= 1))) {
      setGalat("Qty setiap baris minimal 1.");
      return;
    }
    setMenyimpan(true);
    try {
      const body = {
        catatan,
        lines: dipakai.map((b) => ({ product_id: b.productId, qty: Number(b.qty) })),
      };
      const hasil = id
        ? await api.put<Pesanan>(`/orders/${id}`, body)
        : await api.post<Pesanan>("/orders", body);
      await navigate({ to: "/pesanan/$id", params: { id: hasil.id } });
    } catch (err) {
      setGalat(galatPesan(err));
      setMenyimpan(false);
    }
  }

  if (id && !pesanan.memuat && (pesanan.galat || (pesanan.data && pesanan.data.status !== "draft"))) {
    return (
      <>
        <Link to="/pesanan" className={styles.back}>
          <i className="ph ph-arrow-left" aria-hidden="true" /> Pesanan
        </Link>
        <PageHead title="Ubah draft" />
        <Card>
          <Notice tone="warning">
            {pesanan.galat ?? "Pesanan ini sudah dikirim sehingga tidak dapat diubah."}
          </Notice>
          <div className={styles.tools}>
            <Button variant="secondary" onClick={() => void navigate({ to: "/pesanan" })}>
              Kembali ke pesanan
            </Button>
          </div>
        </Card>
      </>
    );
  }

  const memuat = katalog.memuat || (id !== undefined && (pesanan.memuat || !siap));

  return (
    <>
      <Link
        to={id ? "/pesanan/$id" : "/pesanan"}
        params={id ? { id } : undefined}
        className={styles.back}
      >
        <i className="ph ph-arrow-left" aria-hidden="true" /> {id ? "Detail pesanan" : "Pesanan"}
      </Link>
      <PageHead
        title={id ? "Ubah draft" : "Buat pesanan"}
        sub="Harga mengikuti daftar harga cabang Anda dan dikunci saat pesanan dikirim."
      />

      <div className={styles.stack}>
        <Card>
          <CardHead title="Item pesanan" />
          {katalog.galat ? <Notice tone="danger">{katalog.galat}</Notice> : null}
          {adaSertifikat ? (
            <Notice tone="info">
              Sertifikat dipesan terpisah dan tidak boleh dicampur produk reguler. Untuk produk lain,
              buat pesanan baru.
              {biayaPackaging > 0
                ? ` Biaya packaging ${rupiah.format(biayaPackaging)} ditambahkan otomatis per pesanan.`
                : " Biaya packaging ditambahkan otomatis per pesanan."}
            </Notice>
          ) : null}

          {memuat ? (
            <SkeletonList rows={2} />
          ) : (
            <>
              <div className={styles.linesHead} aria-hidden="true">
                <span>Produk</span>
                <span>Qty</span>
                <span className="num">Harga satuan</span>
                <span className="num">Subtotal</span>
                <span />
              </div>
              {baris.map((b, i) => {
                const p = perId.get(b.productId);
                const qty = Number(b.qty) || 0;
                const terpakaiLain = new Set(baris.filter((x) => x.kunci !== b.kunci && x.productId).map((x) => x.productId));
                return (
                  <div className={styles.line} key={b.kunci}>
                    <div className={styles.lProd}>
                      <label className="sr-only" htmlFor={`produk-${b.kunci}`}>
                        Produk baris {i + 1}
                      </label>
                      <Select
                        id={`produk-${b.kunci}`}
                        value={b.productId}
                        onChange={(e) => ubahBaris(b.kunci, { productId: e.target.value })}
                      >
                        <option value="">Pilih produk</option>
                        {produk.map((opt) => {
                          const sertifikat = opt.jenis === "sertifikat";
                          // Produk yang sama tidak boleh dipesan dua kali, dan
                          // Sertifikat tidak boleh dicampur produk reguler.
                          const nonaktif =
                            terpakaiLain.has(opt.id) || (sertifikat ? adaReguler : adaSertifikat);
                          return (
                            <option key={opt.id} value={opt.id} disabled={nonaktif}>
                              {opt.nama}
                              {sertifikat ? " (dipesan terpisah)" : ""}
                            </option>
                          );
                        })}
                      </Select>
                    </div>
                    <div className={styles.lQty}>
                      <span className={styles.mLbl}>Qty</span>
                      <div className={styles.stepper}>
                        <Button
                          variant="secondary"
                          icon="minus"
                          iconOnly
                          aria-label={`Kurangi qty baris ${i + 1}`}
                          disabled={qty <= 1}
                          onClick={() => ubahBaris(b.kunci, { qty: Math.max(1, qty - 1) })}
                        />
                        <Input
                          inputMode="numeric"
                          aria-label={`Qty baris ${i + 1}`}
                          value={String(b.qty)}
                          onChange={(e) => {
                            const angka = Number(e.target.value.replace(/[^0-9]/g, ""));
                            ubahBaris(b.kunci, { qty: Number.isNaN(angka) ? 0 : angka });
                          }}
                        />
                        <Button
                          variant="secondary"
                          icon="plus"
                          iconOnly
                          aria-label={`Tambah qty baris ${i + 1}`}
                          onClick={() => ubahBaris(b.kunci, { qty: qty + 1 })}
                        />
                      </div>
                    </div>
                    <div className={`${styles.lHarga} num`}>
                      <span className={styles.mLbl}>Harga satuan</span>
                      {p ? rupiah.format(p.harga) : "—"}
                    </div>
                    <div className={`${styles.lSub} num`}>
                      <span className={styles.mLbl}>Subtotal</span>
                      <b>{p ? rupiah.format(p.harga * qty) : "—"}</b>
                    </div>
                    <div className={styles.lDel}>
                      <Button
                        variant="ghost"
                        icon="trash"
                        iconOnly
                        aria-label={`Hapus baris ${i + 1}`}
                        disabled={baris.length === 1}
                        onClick={() => hapusBaris(b.kunci)}
                      />
                    </div>
                  </div>
                );
              })}
              {adaSertifikat ? (
                <div className={`${styles.line} ${styles.linePack}`} data-testid="baris-packaging">
                  <div className={styles.lProd}>
                    <i className="ph ph-lock-simple" aria-hidden="true" />
                    Biaya packaging
                  </div>
                  <div className={styles.lQty}>
                    <span className={styles.mLbl}>Qty</span>1
                  </div>
                  <div className={`${styles.lHarga} num`}>
                    <span className={styles.mLbl}>Harga satuan</span>
                    {rupiah.format(biayaPackaging)}
                  </div>
                  <div className={`${styles.lSub} num`}>
                    <span className={styles.mLbl}>Subtotal</span>
                    <b>{rupiah.format(biayaPackaging)}</b>
                  </div>
                  <div className={styles.lDel} />
                </div>
              ) : null}
              <div className={styles.tools}>
                <Button variant="secondary" icon="plus" disabled={!bolehTambah} onClick={tambahBaris}>
                  Tambah produk
                </Button>
                {adaSertifikat ? (
                  <span className="muted small">Untuk produk lain, buat pesanan baru.</span>
                ) : adaReguler ? (
                  <span className="muted small">
                    Sertifikat dipesan terpisah. Buat pesanan baru untuk Sertifikat.
                  </span>
                ) : null}
              </div>
            </>
          )}
        </Card>

        <Card>
          <Field label="Catatan (opsional)" htmlFor="catatan-pesanan" hint="Maksimal 500 karakter.">
            <Textarea
              id="catatan-pesanan"
              maxLength={500}
              value={catatan}
              placeholder="Contoh: dikirim bersama pesanan sebelumnya"
              onChange={(e) => setCatatan(e.target.value)}
            />
          </Field>
        </Card>

        {galat ? <Notice tone="danger">{galat}</Notice> : null}

        <Card className={styles.stickyTotal}>
          <div className={styles.totalBar}>
            <div>
              <span className="muted small">Total</span>
              <b className={`${styles.totalNilai} num`} data-testid="total-draft">
                {rupiah.format(total)}
              </b>
              <span className="muted caption">
                Total final dihitung ulang server saat draft disimpan.
              </span>
            </div>
            <div className={styles.tools}>
              <Button
                variant="primary"
                icon="check-circle"
                disabled={menyimpan || memuat}
                onClick={() => void simpan()}
              >
                {menyimpan ? "Menyimpan…" : "Simpan draft"}
              </Button>
            </div>
          </div>
        </Card>
      </div>
    </>
  );
}
