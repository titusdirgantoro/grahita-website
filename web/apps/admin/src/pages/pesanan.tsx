import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";

import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";

import { ApiClientError, api } from "@grahita/api-client";
import {
  Badge,
  Button,
  Card,
  CardHead,
  Chip,
  EmptyState,
  Field,
  Input,
  Modal,
  Notice,
  PageHead,
  Select,
  SkeletonList,
  Table,
  Textarea,
  useToast,
  type BadgeTone,
} from "@grahita/ui";

import { useApi } from "../useApi";
import { formatWaktu } from "./pengguna";
import styles from "./admin-pages.module.css";

/**
 * FilterPesanan adalah state filter riwayat pemesanan admin yang disimpan di
 * URL search params (SystemDesign.md §7.2) supaya bisa di-bookmark/di-share.
 * Nilainya dipetakan satu-satu ke query string GET /api/v1/admin/orders.
 */
export interface FilterPesanan {
  cari?: string;
  cabang?: string;
  status?: string;
  verif?: string;
  cnc?: boolean;
  terlambat?: boolean;
  email_gagal?: boolean;
  dari?: string;
  sampai?: string;
  grup?: string;
  halaman?: number;
}

/** PesananAdmin adalah satu baris GET /api/v1/admin/orders (F1-15). */
export interface PesananAdmin {
  id: string;
  nomor?: string;
  versi: number;
  branch_id: string;
  branch_nama: string;
  tanggal?: string;
  jatuh_tempo?: string;
  status: string;
  verif_status: string;
  is_cnc: boolean;
  is_sertifikat: boolean;
  total: number;
  jumlah_item: number;
  terlambat: boolean;
  email_gagal: boolean;
  submitted_at?: string;
  created_at: string;
  updated_at: string;
}

export interface PesananGrup {
  kunci: string;
  label: string;
  jumlah: number;
  total: number;
}

export interface PesananHalaman {
  items: PesananAdmin[];
  total: number;
  halaman: number;
  per_halaman: number;
  grup?: string;
  ringkasan?: PesananGrup[];
}

interface BarisPesanan {
  line_no: number;
  jenis: string;
  nama: string;
  satuan: string;
  qty: number;
  harga_satuan: number;
  subtotal: number;
  serial_number?: string;
}

interface EntriTimeline {
  at: string;
  icon?: string;
  teks: string;
  actor_kind: string;
}

interface DokumenPesanan {
  id: string;
  jenis: string;
  versi: number;
  ukuran_byte: number;
  created_at: string;
}

interface LogVerifikasi {
  id: string;
  metode: string;
  total_pesanan: number;
  total_terbaca: number;
  selisih: number;
  hasil: string;
  alasan?: string;
  catatan?: string;
  admin_nama?: string;
  created_at: string;
}

/** PesananDetail adalah respons GET /api/v1/admin/orders/{id} (F1-15). */
export interface PesananDetail {
  id: string;
  nomor?: string;
  versi: number;
  branch_id: string;
  branch_nama: string;
  branch_kepala: string;
  branch_email: string;
  branch_cnc: boolean;
  status: string;
  verif_status: string;
  alasan_review?: string;
  is_cnc: boolean;
  is_sertifikat: boolean;
  tanggal?: string;
  jatuh_tempo?: string;
  submitted_at?: string;
  done_at?: string;
  created_at: string;
  updated_at: string;
  catatan: string;
  note_admin: string;
  total: number;
  jumlah_item: number;
  terlambat: boolean;
  email_gagal: boolean;
  lines: BarisPesanan[];
  timeline: EntriTimeline[];
  dokumen: DokumenPesanan[];
  verifikasi: LogVerifikasi[];
}

/** Bukti transfer (endpoint F1-13; dipakai ulang oleh detail F1-15). */
interface BuktiTransfer {
  id: string;
  order_id: string;
  nama_file: string;
  mime_type: string;
  ukuran_byte: number;
  uploaded_by_kind: string;
  uploaded_by_nama?: string;
  created_at: string;
}

/** Riwayat email pesanan (endpoint F1-16). */
interface EmailPesanan {
  id: string;
  jenis: string;
  to_addr: string[];
  subject: string;
  status: string;
  attempts: number;
  last_error?: string;
  sent_at?: string;
  created_at: string;
}

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

const tanggalPanjang = new Intl.DateTimeFormat("id-ID", {
  day: "2-digit",
  month: "short",
  year: "numeric",
  timeZone: "Asia/Jakarta",
});

const LABEL_STATUS: Record<string, string> = {
  draft: "Draft",
  submitted: "Submitted",
  done: "Selesai",
};
const TONE_STATUS: Record<string, BadgeTone> = {
  draft: "neutral",
  submitted: "warning",
  done: "success",
};
const LABEL_VERIF: Record<string, string> = {
  belum: "Menunggu verifikasi",
  cocok: "Terverifikasi",
  perlu_review: "Perlu review",
};
const TONE_VERIF: Record<string, BadgeTone> = {
  belum: "warning",
  cocok: "success",
  perlu_review: "danger",
};
const LABEL_ALASAN: Record<string, string> = {
  kurang_bayar: "Kurang bayar",
  lebih_bayar: "Lebih bayar",
  tidak_terbaca: "Bukti tidak terbaca",
  rekening_beda: "Rekening tidak sesuai",
  lainnya: "Lainnya",
};
const LABEL_DOKUMEN: Record<string, string> = {
  invoice_pdf: "Invoice",
  surat_jalan_pdf: "Surat jalan (PDF)",
  surat_jalan_xlsx: "Surat jalan (Excel)",
  report_xlsx: "Laporan",
};
const LABEL_EMAIL: Record<string, string> = {
  invoice_cabang: "Invoice ke cabang",
  invoice_admin: "Invoice ke admin",
  surat_jalan_admin: "Surat jalan ke admin",
  reset_password: "Tautan reset kata sandi",
  undangan_admin: "Undangan admin",
  reminder_cnc: "Pengingat C&C",
};
const TONE_EMAIL: Record<string, BadgeTone> = {
  queued: "warning",
  sending: "warning",
  sent: "success",
  failed: "danger",
};

const GRUP = [
  { value: "tanpa", label: "Tanpa grup" },
  { value: "cabang", label: "Per cabang" },
  { value: "hari", label: "Per hari" },
  { value: "bulan", label: "Per bulan" },
  { value: "tahun", label: "Per tahun" },
] as const;

const PER_HALAMAN = 25;

function tanggalSingkat(nilai?: string): string {
  if (!nilai) return "—";
  const t = new Date(nilai);
  if (Number.isNaN(t.getTime())) return "—";
  return tanggalPanjang.format(t);
}

/** queryPesanan menerjemahkan filter menjadi query string API. */
export function queryPesanan(f: FilterPesanan): string {
  const p = new URLSearchParams();
  if (f.cari) p.set("cari", f.cari);
  if (f.cabang) p.set("cabang", f.cabang);
  if (f.status && f.status !== "semua") p.set("status", f.status);
  if (f.verif && f.verif !== "semua") p.set("verif", f.verif);
  if (f.cnc) p.set("cnc", "true");
  if (f.terlambat) p.set("terlambat", "true");
  if (f.email_gagal) p.set("email_gagal", "true");
  if (f.dari) p.set("dari", f.dari);
  if (f.sampai) p.set("sampai", f.sampai);
  if (f.grup && f.grup !== "tanpa") p.set("grup", f.grup);
  p.set("halaman", String(f.halaman && f.halaman > 0 ? f.halaman : 1));
  p.set("per_halaman", String(PER_HALAMAN));
  return p.toString();
}

function pesanGalat(err: unknown): string {
  return err instanceof ApiClientError ? err.message : "Terjadi kesalahan. Coba lagi.";
}

/**
 * pPesanan (admin) — /pesanan: riwayat seluruh cabang dengan pencarian, filter,
 * grouping, dan paginasi server (PRD AP-ORD-01..03).
 */
export function PesananAdminPage() {
  const search = useSearch({ strict: false }) as FilterPesanan;
  const navigate = useNavigate();
  const [cariKetik, setCariKetik] = useState(search.cari ?? "");

  // Nilai filter yang diketik dipertahankan saat URL berubah karena navigasi
  // lain (mis. tombol paginasi) — hanya disinkronkan bila search berubah.
  useEffect(() => {
    setCariKetik(search.cari ?? "");
  }, [search.cari]);

  const ubah = useCallback(
    (patch: FilterPesanan, resetHalaman = true) => {
      void navigate({
        to: "/pesanan",
        search: ((prev: FilterPesanan) => ({
          ...prev,
          ...patch,
          ...(resetHalaman ? { halaman: 1 } : {}),
        })) as never,
      });
    },
    [navigate],
  );

  const query = queryPesanan(search);
  const { data, memuat, galat } = useApi<PesananHalaman>(`/admin/orders?${query}`);
  const { data: cabang } = useApi<{ id: string; nama: string }[]>("/admin/branches");

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const halaman = data?.halaman ?? search.halaman ?? 1;
  const totalHalaman = Math.max(1, Math.ceil(total / (data?.per_halaman ?? PER_HALAMAN)));
  const grup = data?.grup ?? "";
  const ringkasan = data?.ringkasan ?? [];
  const adaFilter = Boolean(
    search.cari || search.cabang || search.status || search.verif || search.cnc || search.terlambat || search.email_gagal || search.dari || search.sampai,
  );

  return (
    <>
      <PageHead title="Riwayat pemesanan" sub="Seluruh cabang" />

      <Card>
        <CardHead title="Filter" />
        <form
          className={styles.row}
          style={{ alignItems: "flex-end", marginBottom: 12 }}
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            ubah({ cari: cariKetik.trim() || undefined });
          }}
        >
          <Field label="Cari nomor, cabang, atau serial" htmlFor="ord-cari">
            <Input
              id="ord-cari"
              value={cariKetik}
              placeholder="Contoh: 2026-10-0"
              onChange={(e) => setCariKetik(e.target.value)}
            />
          </Field>
          <Field label="Cabang" htmlFor="ord-cabang">
            <Select
              id="ord-cabang"
              value={search.cabang ?? ""}
              onChange={(e) => ubah({ cabang: e.target.value || undefined })}
            >
              <option value="">Semua cabang</option>
              {(cabang ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.nama}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Status" htmlFor="ord-status">
            <Select
              id="ord-status"
              value={search.status ?? "semua"}
              onChange={(e) => ubah({ status: e.target.value === "semua" ? undefined : e.target.value })}
            >
              <option value="semua">Semua status</option>
              <option value="draft">Draft</option>
              <option value="submitted">Submitted</option>
              <option value="done">Selesai</option>
            </Select>
          </Field>
          <Field label="Verifikasi" htmlFor="ord-verif">
            <Select
              id="ord-verif"
              value={search.verif ?? "semua"}
              onChange={(e) => ubah({ verif: e.target.value === "semua" ? undefined : e.target.value })}
            >
              <option value="semua">Semua</option>
              <option value="belum">Menunggu verifikasi</option>
              <option value="cocok">Terverifikasi</option>
              <option value="perlu_review">Perlu review</option>
            </Select>
          </Field>
          <Field label="Kelompokkan" htmlFor="ord-grup">
            <Select
              id="ord-grup"
              value={search.grup ?? "tanpa"}
              onChange={(e) => ubah({ grup: e.target.value === "tanpa" ? undefined : e.target.value })}
            >
              {GRUP.map((g) => (
                <option key={g.value} value={g.value}>
                  {g.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Dari tanggal" htmlFor="ord-dari">
            <Input
              id="ord-dari"
              type="date"
              value={search.dari ?? ""}
              onChange={(e) => ubah({ dari: e.target.value || undefined })}
            />
          </Field>
          <Field label="Sampai tanggal" htmlFor="ord-sampai">
            <Input
              id="ord-sampai"
              type="date"
              value={search.sampai ?? ""}
              onChange={(e) => ubah({ sampai: e.target.value || undefined })}
            />
          </Field>
          <Button type="submit" variant="secondary" icon="magnifying-glass">
            Cari
          </Button>
          {adaFilter ? (
            <Button type="button" variant="ghost" onClick={() => ubah({ cari: undefined, cabang: undefined, status: undefined, verif: undefined, cnc: undefined, terlambat: undefined, email_gagal: undefined, dari: undefined, sampai: undefined })}>
              Reset filter
            </Button>
          ) : null}
        </form>

        <div className={styles.chips}>
          <Chip
            pressed={Boolean(search.cnc)}
            onToggle={(v) => ubah({ cnc: v || undefined })}
            onRemove={() => ubah({ cnc: undefined })}
            removeLabel="Sembunyikan filter C&C"
          >
            C&amp;C
          </Chip>
          <Chip
            pressed={Boolean(search.terlambat)}
            onToggle={(v) => ubah({ terlambat: v || undefined })}
            onRemove={() => ubah({ terlambat: undefined })}
            removeLabel="Sembunyikan filter lewat jatuh tempo"
          >
            Lewat jatuh tempo
          </Chip>
          <Chip
            pressed={Boolean(search.email_gagal)}
            onToggle={(v) => ubah({ email_gagal: v || undefined })}
            onRemove={() => ubah({ email_gagal: undefined })}
            removeLabel="Sembunyikan filter email gagal"
          >
            Email gagal
          </Chip>
        </div>
      </Card>

      {grup ? (
        <Card>
          <CardHead title="Ringkasan grup" tools={<Badge tone="neutral">{`${ringkasan.length} grup`}</Badge>} />
          <div className={styles.chips}>
            {ringkasan.map((g) => (
              <span key={g.kunci} className={styles.grupRingkas}>
                <strong>{g.label || g.kunci}</strong>
                <span className="muted small">
                  {g.jumlah} pesanan · <span className="tabular">{rupiah.format(g.total)}</span>
                </span>
              </span>
            ))}
            {ringkasan.length === 0 ? <span className="muted small">Tidak ada data pada filter ini.</span> : null}
          </div>
        </Card>
      ) : null}

      <Card>
        <CardHead
          title="Daftar pesanan"
          tools={
            <span className="muted small">
              {`${total} pesanan · halaman ${halaman} dari ${totalHalaman}`}
            </span>
          }
        />
        {galat ? <Notice tone="danger">{galat}</Notice> : null}
        {memuat && items.length === 0 ? (
          <SkeletonList rows={4} />
        ) : (
          <Table mobileCards caption="Riwayat pemesanan seluruh cabang">
            <thead>
              <tr>
                <th scope="col">Nomor</th>
                <th scope="col">Cabang</th>
                <th scope="col">Tanggal</th>
                <th scope="col" className="num">
                  Item
                </th>
                <th scope="col" className="num">
                  Total
                </th>
                <th scope="col">Status</th>
                <th scope="col">Verifikasi</th>
              </tr>
            </thead>
            <tbody>
              {items.map((p) => (
                <tr key={p.id} data-uji="baris-pesanan">
                  <td>
                    <Link to="/pesanan/$id" params={{ id: p.id }} className={styles.tautanNomor}>
                      {p.nomor || "Draft"}
                      {p.versi > 0 ? <Badge tone="rev">{`R${p.versi}`}</Badge> : null}
                    </Link>
                    <div className={styles.chips} style={{ marginTop: 4 }}>
                      {p.is_cnc ? <Badge tone="cnc">C&amp;C</Badge> : null}
                      {p.terlambat ? <Badge tone="danger">Lewat tempo</Badge> : null}
                      {p.email_gagal ? <Badge tone="danger">Email gagal</Badge> : null}
                      {p.is_sertifikat ? <Badge tone="neutral">Sertifikat</Badge> : null}
                    </div>
                  </td>
                  <td>{p.branch_nama}</td>
                  <td className="tabular">{tanggalSingkat(p.tanggal ?? p.created_at)}</td>
                  <td className="num tabular">{p.jumlah_item}</td>
                  <td className="num tabular">{rupiah.format(p.total)}</td>
                  <td>
                    <Badge tone={TONE_STATUS[p.status] ?? "neutral"}>{LABEL_STATUS[p.status] ?? p.status}</Badge>
                  </td>
                  <td>
                    <Badge tone={TONE_VERIF[p.verif_status] ?? "neutral"}>
                      {LABEL_VERIF[p.verif_status] ?? p.verif_status}
                    </Badge>
                  </td>
                </tr>
              ))}
              {items.length === 0 && !memuat ? (
                <tr>
                  <td colSpan={7}>
                    <EmptyState icon="receipt">Belum ada pesanan untuk filter ini.</EmptyState>
                  </td>
                </tr>
              ) : null}
            </tbody>
          </Table>
        )}

        <div className={styles.pager}>
          <Button
            variant="secondary"
            icon="caret-left"
            disabled={halaman <= 1}
            onClick={() => ubah({ halaman: Math.max(1, halaman - 1) }, false)}
          >
            Sebelumnya
          </Button>
          <span className="muted small" data-uji="info-halaman">
            {`Halaman ${halaman} dari ${totalHalaman}`}
          </span>
          <Button
            variant="secondary"
            disabled={halaman >= totalHalaman}
            onClick={() => ubah({ halaman: halaman + 1 }, false)}
          >
            Berikutnya
          </Button>
        </div>
      </Card>
    </>
  );
}

/**
 * pDetail (admin) — /pesanan/{id}: header + data cabang, order line + serial,
 * bukti transfer (dengan zoom), timeline, catatan internal, dokumen, log
 * verifikasi, dan riwayat email (PRD AP-ORD-04..06, AP-ORD-08).
 *
 * Bukti dan email dibaca dari endpoint masing-masing (F1-13/F1-16) supaya satu
 * respons tetap kecil dan otorisasinya tetap di jalur yang sudah diuji.
 */
export function PesananDetailAdminPage() {
  const { id } = useParams({ strict: false }) as { id: string };
  const { data, memuat, galat, muatUlang } = useApi<PesananDetail>(`/admin/orders/${id}`);
  const { data: bukti } = useApi<BuktiTransfer[]>(`/admin/orders/${id}/proofs`);
  const { data: emails, muatUlang: muatEmail } = useApi<EmailPesanan[]>(`/admin/orders/${id}/emails`);

  const [catatan, setCatatan] = useState("");
  const [tersentuh, setTersentuh] = useState(false);
  const [sibukCatatan, setSibukCatatan] = useState(false);
  const [galatCatatan, setGalatCatatan] = useState<string | null>(null);
  const [zoom, setZoom] = useState<{ url: string; nama: string } | null>(null);
  const [sibukEmail, setSibukEmail] = useState<string | null>(null);
  const { tampilkan } = useToast();

  // Catatan awal dari server hanya dipakai sampai pengguna mulai mengetik,
  // supaya muat ulang tidak menimpa ketikan yang belum disimpan.
  const nilaiCatatan = tersentuh ? catatan : (data?.note_admin ?? "");
  useEffect(() => {
    if (!tersentuh) setCatatan(data?.note_admin ?? "");
  }, [data?.note_admin, tersentuh]);

  const simpanCatatan = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      setSibukCatatan(true);
      setGalatCatatan(null);
      try {
        await api.post(`/admin/orders/${id}/notes`, { catatan: nilaiCatatan.trim() });
        setTersentuh(false);
        muatUlang();
        tampilkan("Catatan internal tersimpan.");
      } catch (err) {
        setGalatCatatan(pesanGalat(err));
      } finally {
        setSibukCatatan(false);
      }
    },
    [id, nilaiCatatan, muatUlang, tampilkan],
  );

  const kirimUlang = useCallback(
    async (jenis: string) => {
      setSibukEmail(jenis);
      try {
        await api.post(`/admin/orders/${id}/emails/${jenis}/resend`, {});
        muatEmail();
        tampilkan("Email diantre ulang.");
      } catch (err) {
        tampilkan(pesanGalat(err));
      } finally {
        setSibukEmail(null);
      }
    },
    [id, muatEmail, tampilkan],
  );

  const bukaBukti = useCallback(async (b: BuktiTransfer) => {
    try {
      const dl = await api.get<{ url: string; nama_file: string }>(`/admin/orders/${id}/proofs/${b.id}/download`);
      setZoom({ url: dl.url, nama: dl.nama_file });
    } catch (err) {
      setZoom(null);
      throw err;
    }
  }, [id]);

  const timeline = useMemo(() => data?.timeline ?? [], [data?.timeline]);

  if (memuat && !data) {
    return (
      <>
        <PageHead title="Detail pesanan" sub="Memuat…" />
        <Card>
          <SkeletonList rows={5} />
        </Card>
      </>
    );
  }
  if (galat || !data) {
    return (
      <>
        <PageHead title="Detail pesanan" sub="Tidak ditemukan" />
        <Card>
          <Notice tone="danger">{galat ?? "Pesanan tidak ditemukan."}</Notice>
          <div className={styles.row} style={{ marginTop: 16 }}>
            <Link to="/pesanan" className={styles.tautanNomor}>
              Kembali ke riwayat pemesanan
            </Link>
          </div>
        </Card>
      </>
    );
  }

  return (
    <>
      <div className={styles.row} style={{ marginBottom: 8 }}>
        <Link to="/pesanan" className={styles.tautanNomor} data-uji="tautan-kembali">
          ← Riwayat pemesanan
        </Link>
      </div>
      <PageHead
        title={data.nomor || `Draft ${data.id.slice(0, 8)}`}
        sub={`${data.branch_nama} · ${data.branch_kepala}`}
        actions={
          <div className={styles.chips}>
            <Badge tone={TONE_STATUS[data.status] ?? "neutral"}>{LABEL_STATUS[data.status] ?? data.status}</Badge>
            {data.versi > 0 ? <Badge tone="rev">{`Revisi R${data.versi}`}</Badge> : null}
            {data.is_cnc ? <Badge tone="cnc">C&amp;C</Badge> : null}
            {data.is_sertifikat ? <Badge tone="neutral">Sertifikat</Badge> : null}
            {data.terlambat ? <Badge tone="danger">Lewat jatuh tempo</Badge> : null}
            {data.email_gagal ? <Badge tone="danger">Email gagal</Badge> : null}
          </div>
        }
      />

      <div className={styles.grid2}>
        <div className={styles.stack}>
          <Card>
            <CardHead title="Ringkasan pesanan" />
            <div className={styles.metaGrid}>
              <div>
                <div className="muted small">Tanggal pesanan</div>
                <div>{tanggalSingkat(data.tanggal ?? data.created_at)}</div>
              </div>
              <div>
                <div className="muted small">Cabang</div>
                <div>{data.branch_nama}</div>
                <div className="muted small">{data.branch_email}</div>
              </div>
              {data.is_cnc ? (
                <div>
                  <div className="muted small">Jatuh tempo C&amp;C</div>
                  <div style={data.terlambat ? { color: "var(--danger-fg)" } : undefined}>
                    {tanggalSingkat(data.jatuh_tempo)}
                  </div>
                </div>
              ) : null}
              <div>
                <div className="muted small">Status verifikasi</div>
                <div>
                  <Badge tone={TONE_VERIF[data.verif_status] ?? "neutral"}>
                    {LABEL_VERIF[data.verif_status] ?? data.verif_status}
                  </Badge>
                </div>
                {data.alasan_review ? (
                  <div className="small" style={{ color: "var(--danger-fg)" }}>
                    {LABEL_ALASAN[data.alasan_review] ?? data.alasan_review}
                  </div>
                ) : null}
              </div>
              <div>
                <div className="muted small">Total</div>
                <div className="tabular">{rupiah.format(data.total)}</div>
              </div>
              <div>
                <div className="muted small">Jumlah item</div>
                <div className="tabular">{data.jumlah_item}</div>
              </div>
            </div>
            <div style={{ marginTop: 12 }}>
              <div className="muted small">Catatan cabang</div>
              <p>{data.catatan || "—"}</p>
            </div>
          </Card>

          <Card>
            <CardHead title="Order line" tools={<span className="muted small">{`${data.lines.length} baris`}</span>} />
            <Table caption="Order line pesanan">
              <thead>
                <tr>
                  <th scope="col">Produk</th>
                  <th scope="col">Serial number</th>
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
                {data.lines.map((l) => (
                  <tr key={l.line_no}>
                    <td>{l.nama}</td>
                    <td className="tabular muted">{l.serial_number || "—"}</td>
                    <td className="num tabular">{l.qty}</td>
                    <td className="num tabular">{rupiah.format(l.harga_satuan)}</td>
                    <td className="num tabular">{rupiah.format(l.subtotal)}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </Card>

          <Card>
            <CardHead title="Timeline" tools={<span className="muted small">{`${timeline.length} peristiwa`}</span>} />
            {timeline.length === 0 ? (
              <p className="muted small">Belum ada peristiwa.</p>
            ) : (
              <ol className={styles.timeline}>
                {timeline.map((e, i) => (
                  <li key={`${e.at}-${i}`}>
                    <span className="muted small">{formatWaktu(e.at)}</span>
                    <div>{e.teks}</div>
                  </li>
                ))}
              </ol>
            )}
          </Card>

          <Card>
            <CardHead title="Dokumen" />
            {data.dokumen.length === 0 ? (
              <p className="muted small">Belum ada dokumen. Invoice &amp; surat jalan lahir setelah verifikasi.</p>
            ) : (
              <ul className={styles.daftarPolos}>
                {data.dokumen.map((d) => (
                  <li key={d.id}>
                    <span>{LABEL_DOKUMEN[d.jenis] ?? d.jenis}</span>
                    <span className="muted small">{`v${d.versi} · ${formatWaktu(d.created_at)}`}</span>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card>
            <CardHead title="Log verifikasi" />
            {data.verifikasi.length === 0 ? (
              <p className="muted small">Belum ada verifikasi.</p>
            ) : (
              <Table caption="Log verifikasi pesanan">
                <thead>
                  <tr>
                    <th scope="col">Metode</th>
                    <th scope="col" className="num">
                      Total pesanan
                    </th>
                    <th scope="col" className="num">
                      Terbaca
                    </th>
                    <th scope="col" className="num">
                      Selisih
                    </th>
                    <th scope="col">Hasil</th>
                    <th scope="col">Oleh</th>
                  </tr>
                </thead>
                <tbody>
                  {data.verifikasi.map((v) => (
                    <tr key={v.id}>
                      <td>{v.metode}</td>
                      <td className="num tabular">{rupiah.format(v.total_pesanan)}</td>
                      <td className="num tabular">{rupiah.format(v.total_terbaca)}</td>
                      <td className="num tabular">{rupiah.format(v.selisih)}</td>
                      <td>
                        <Badge tone={TONE_VERIF[v.hasil] ?? "neutral"}>{LABEL_VERIF[v.hasil] ?? v.hasil}</Badge>
                        {v.alasan ? <div className="small muted">{LABEL_ALASAN[v.alasan] ?? v.alasan}</div> : null}
                      </td>
                      <td>
                        {v.admin_nama || "AI"}
                        <div className="muted small">{formatWaktu(v.created_at)}</div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </div>

        <div className={styles.stack}>
          <Card>
            <CardHead title="Catatan internal" />
            <p className="muted small" style={{ marginBottom: 8 }}>
              Hanya terlihat admin Pusat; portal cabang tidak pernah menerimanya.
            </p>
            <form onSubmit={(e) => void simpanCatatan(e)}>
              {galatCatatan ? <Notice tone="danger">{galatCatatan}</Notice> : null}
              <Field label="Note admin" htmlFor="catatan-internal">
                <Textarea
                  id="catatan-internal"
                  value={nilaiCatatan}
                  maxLength={2000}
                  placeholder="Catatan internal untuk tim Pusat"
                  onChange={(e) => {
                    setTersentuh(true);
                    setCatatan(e.target.value);
                  }}
                />
              </Field>
              <Button type="submit" variant="secondary" disabled={sibukCatatan || nilaiCatatan.trim() === ""}>
                {sibukCatatan ? "Menyimpan…" : "Simpan catatan"}
              </Button>
            </form>
          </Card>

          <Card>
            <CardHead title="Bukti transfer" tools={<Badge tone="neutral">{`${(bukti ?? []).length} berkas`}</Badge>} />
            {(bukti ?? []).length === 0 ? (
              <p className="muted small">Belum ada bukti transfer.</p>
            ) : (
              <ul className={styles.daftarBukti}>
                {(bukti ?? []).map((b) => (
                  <li key={b.id}>
                    <div>
                      <div>{b.nama_file}</div>
                      <div className="muted small">
                        {b.uploaded_by_kind === "admin"
                          ? `Diunggah admin: ${b.uploaded_by_nama || "—"}`
                          : "Diunggah cabang"}
                      </div>
                    </div>
                    <Button variant="ghost" icon="magnifying-glass-plus" onClick={() => void bukaBukti(b)}>
                      Zoom
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card>
            <CardHead title="Riwayat email" />
            {(emails ?? []).length === 0 ? (
              <p className="muted small">Belum ada email untuk pesanan ini.</p>
            ) : (
              <ul className={styles.daftarPolos}>
                {(emails ?? []).map((e) => (
                  <li key={e.id}>
                    <div className={styles.row} style={{ justifyContent: "space-between" }}>
                      <span>{LABEL_EMAIL[e.jenis] ?? e.jenis}</span>
                      <Badge tone={TONE_EMAIL[e.status] ?? "neutral"}>{e.status}</Badge>
                    </div>
                    <div className="muted small">{`${e.to_addr.join(", ")} · percobaan ${e.attempts}`}</div>
                    {e.last_error ? <div className="small" style={{ color: "var(--danger-fg)" }}>{e.last_error}</div> : null}
                    {e.status === "failed" && e.jenis !== "reset_password" && e.jenis !== "undangan_admin" ? (
                      <Button
                        variant="ghost"
                        disabled={sibukEmail === e.jenis}
                        onClick={() => void kirimUlang(e.jenis)}
                      >
                        {sibukEmail === e.jenis ? "Mengirim…" : "Kirim ulang"}
                      </Button>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>
      </div>

      <Modal
        open={zoom !== null}
        title={zoom?.nama ?? "Bukti transfer"}
        onClose={() => setZoom(null)}
        wide
        footer={
          <Button variant="secondary" onClick={() => setZoom(null)}>
            Tutup
          </Button>
        }
      >
        {zoom ? (
          zoom.url && /\.(png|jpe?g)$/i.test(zoom.nama) ? (
            <img src={zoom.url} alt={`Pratinjau ${zoom.nama}`} style={{ maxWidth: "100%", borderRadius: 12 }} />
          ) : (
            <p className="small">
              Pratinjau PDF tidak ditampilkan di modal. Buka tautan berikut:
              <br />
              <a href={zoom.url} target="_blank" rel="noreferrer">
                {zoom.nama}
              </a>
            </p>
          )
        ) : null}
      </Modal>
    </>
  );
}
