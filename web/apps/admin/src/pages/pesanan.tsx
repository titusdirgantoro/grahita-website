import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";

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
  type Role,
} from "@grahita/ui";

import { useApi } from "../useApi";
import { useSesi } from "../session";
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
  branch_allowlist_version?: number;
  destination_results?: Array<{
    payment_proof_id: string;
    status: string;
    bank_snapshot?: string;
    account_owner_snapshot?: string;
    account_last_digits_snapshot?: string;
  }>;
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

/**
 * BarisRevisi adalah satu baris pada daftar baris FINAL revisi (F2-25).
 *
 * `line_no > 0` menandai baris lama yang dipertahankan (serial/jenis/produknya
 * tetap; hanya qty/harga yang boleh berubah). `line_no === 0` adalah baris
 * produk BARU yang dikirim dengan `product_id` tanpa `line_no`. qty/harga
 * disimpan sebagai teks mentah supaya admin bisa mengetik bebas; angka baru
 * diurai saat dikirim.
 */
interface BarisRevisi {
  kunci: string;
  line_no: number;
  product_id?: string;
  nama: string;
  serial_number?: string;
  qty: string;
  harga: string;
}

/** Produk pada matriks harga (GET /admin/prices) untuk baris baru revisi. */
interface ProdukMatriks {
  id: string;
  nama: string;
  is_active: boolean;
}

interface SelMatriks {
  branch_id: string;
  product_id: string;
  harga: number;
}

interface MatriksHarga {
  produk: ProdukMatriks[];
  cabang: { id: string; nama: string }[];
  sel: SelMatriks[];
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
  ocr_nominal?: number;
  ocr_tanggal?: string;
  ocr_bank?: string;
  ocr_rek_akhir?: string;
  ocr_confidence?: number;
  ocr_at?: string;
}

interface RekeningEfektif {
  id: string;
  bank: string;
  nama_pemilik: string;
  display_nomor: string;
}

interface RekeningOrder {
  accounts: RekeningEfektif[];
  allowlist_version: number;
  ai_active: boolean;
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

const LABEL_STATUS_REKENING: Record<string, string> = {
  allowed: "Sesuai",
  not_allowed: "Tidak diizinkan",
  inactive: "Nonaktif",
  ambiguous: "Ambigu",
  unreadable: "Tidak terbaca",
};

export function labelStatusRekening(status?: string): string {
  return status ? (LABEL_STATUS_REKENING[status] ?? "Tidak terbaca") : "Tidak terbaca";
}

/**
 * PILIHAN_ALASAN adalah daftar tertutup alasan review (PRD §4.2 AP-ORD-07,
 * §7.1). Urutannya sama dengan enum review_reason di server; klien tidak
 * pernah mengirim alasan di luar daftar ini.
 */
const PILIHAN_ALASAN = ["kurang_bayar", "lebih_bayar", "tidak_terbaca", "rekening_beda", "lainnya"] as const;

/** Keputusan verifikasi: `cocok` menyetujui (→ Done), `perlu_review` menandai. */
type KeputusanVerifikasi = "cocok" | "perlu_review";

/**
 * bolehVerifikasi meniru peta kapabilitas server (api/internal/auth/actor.go):
 * `admin.verify` dimiliki Manager maupun Superadmin. Portal admin memang hanya
 * dihuni kedua peran itu, tetapi penjaga ini tetap ada supaya tombol verifikasi
 * tidak pernah muncul untuk peran yang tidak berhak — API tetap otoritas akhir.
 */
export function bolehVerifikasi(peran: Role): boolean {
  return peran === "manager" || peran === "superadmin";
}

/**
 * bolehRevisi meniru peta kapabilitas server (api/internal/auth/actor.go):
 * `admin.revise` hanya dimiliki staf Pusat — Superadmin selalu, Manager hanya
 * bila setelan `manager_boleh_revisi` aktif (PRD §4.9). Setelan itu TIDAK
 * dibaca UI; karena itu penjaga ini hanya menyembunyikan aksi dari peran yang
 * memang bukan staf Pusat, dan API tetap otoritas akhir atas izin Manager.
 */
export function bolehRevisi(peran: Role): boolean {
  return peran === "manager" || peran === "superadmin";
}

/**
 * uraiBilanganBulat membaca qty/harga revisi yang diketik admin dengan KETAT:
 * hanya digit 0-9 (bilangan bulat non-negatif). Teks kosong, negatif, desimal,
 * atau bersimbol lain dikembalikan `null` supaya ditolak dan dilaporkan ke
 * operator — bukan diam-diam dibersihkan menjadi angka lain. Tanpa ini
 * penyaringan digit membuat "-5" menjadi 5 dan "1.5" menjadi 15 sehingga
 * penjaga qty<1/harga<0 praktis mati dan salah ketik tersamarkan.
 */
export function uraiBilanganBulat(nilai: string): number | null {
  const teks = nilai.trim();
  if (!/^\d+$/.test(teks)) return null;
  const n = Number.parseInt(teks, 10);
  return Number.isFinite(n) ? n : null;
}

/**
 * parseNominal membaca nominal yang diketik admin (boleh memakai titik/spasi
 * pemisah ribuan). Kosong berarti tidak ada nominal terbaca (dikirim sebagai
 * null), bukan nol — selisih lalu dihitung server dari 0 − total.
 */
export function parseNominal(nilai: string): number | null {
  const digit = nilai.replace(/[^\d]/g, "");
  if (digit === "") return null;
  const n = Number(digit);
  return Number.isFinite(n) ? n : null;
}

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
 * pesanVerifikasi menerjemahkan galat POST /verify (F1-18) ke bahasa manusia.
 * Kode 409 bermakna pesanannya sudah berpindah keadaan di sisi server —
 * bukan kesalahan admin — sehingga jawabannya menjelaskan, bukan menuduh.
 */
function pesanVerifikasi(err: unknown): string {
  if (err instanceof ApiClientError) {
    switch (err.code) {
      case "ORDER_ALREADY_DONE":
        return "Pesanan ini sudah selesai diverifikasi. Tampilan dimuat ulang.";
      case "ORDER_NOT_SUBMITTED":
        return "Verifikasi hanya untuk pesanan yang sudah dikirim.";
      default:
        return err.message;
    }
  }
  return "Terjadi kesalahan. Coba lagi.";
}

/**
 * pesanRevisi menerjemahkan galat POST /api/v1/admin/orders/{id}/revise (F2-25)
 * ke bahasa manusia. Kode 409 adalah fail-closed yang memang disengaja server:
 * tidak ada versi/baris yang berubah, jadi UI tidak pernah boleh mengklaim
 * berhasil, menaikkan versi lokal, atau mengulang sendiri.
 */
function pesanRevisi(err: unknown): string {
  if (err instanceof ApiClientError) {
    if (err.status === 409 && err.code === "INVOICE_NOT_READY") {
      return "Invoice versi berjalan belum selesai dirender, jadi revisi belum bisa menaikkan versi. Tunggu proses dokumen selesai, lalu kirim ulang manual. Detail & dokumen dimuat ulang.";
    }
    if (err.status === 409 && err.code === "ORDER_NOT_DONE") {
      return "Revisi hanya untuk pesanan yang sudah selesai (Done). Selesaikan verifikasi pembayaran lebih dulu.";
    }
    if (err.status === 403) {
      return `Anda tidak diizinkan merevisi invoice ini. ${err.message}`;
    }
    if (err.status === 503) {
      return `Layanan revisi belum siap. ${err.message} Coba lagi beberapa saat lagi.`;
    }
    return err.message;
  }
  return "Terjadi kesalahan. Coba lagi.";
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
  const { data: rekeningOrder, muatUlang: muatRekeningOrder } = useApi<RekeningOrder>(
    `/admin/orders/${id}/bank-accounts`,
    data?.status === "submitted",
  );
  const { data: emails, muatUlang: muatEmail } = useApi<EmailPesanan[]>(`/admin/orders/${id}/emails`);

  const [catatan, setCatatan] = useState("");
  const [tersentuh, setTersentuh] = useState(false);
  const [sibukCatatan, setSibukCatatan] = useState(false);
  const [galatCatatan, setGalatCatatan] = useState<string | null>(null);
  const [zoom, setZoom] = useState<{ url: string; nama: string } | null>(null);
  const [sibukEmail, setSibukEmail] = useState<string | null>(null);
  const [sibukDokumen, setSibukDokumen] = useState(false);
  const { tampilkan } = useToast();
  const { peran } = useSesi();
  const bolehVerif = bolehVerifikasi(peran);
  const bolehRev = bolehRevisi(peran);

  // Keadaan modal verifikasi pembayaran manual (F1-18, PRD §4.2 AP-ORD-07).
  const [verifBuka, setVerifBuka] = useState(false);
  const [keputusanVerif, setKeputusanVerif] = useState<KeputusanVerifikasi>("cocok");
  const [nominalVerif, setNominalVerif] = useState("");
  const [alasanVerif, setAlasanVerif] = useState<string>(PILIHAN_ALASAN[0]);
  const [catatanVerif, setCatatanVerif] = useState("");
  const [sibukVerif, setSibukVerif] = useState(false);
  const [galatVerif, setGalatVerif] = useState<string | null>(null);
  const [tujuanBukti, setTujuanBukti] = useState<Record<string, string>>({});
  const [sibukNilaiUlang, setSibukNilaiUlang] = useState(false);

  useEffect(() => {
    if (!verifBuka || rekeningOrder?.accounts?.length !== 1) return;
    const accountID = rekeningOrder.accounts[0]?.id;
    if (!accountID) return;
    setTujuanBukti((lama) => {
      const baru = { ...lama };
      for (const proof of bukti ?? []) {
        if (!baru[proof.id]) baru[proof.id] = accountID;
      }
      return baru;
    });
  }, [verifBuka, rekeningOrder, bukti]);

  // Keadaan modal revisi invoice Done (F2-25, PRD §4.3 AP-REV-01..05).
  const [revisiBuka, setRevisiBuka] = useState(false);
  const [alasanRevisi, setAlasanRevisi] = useState("");
  const [catatanRevisi, setCatatanRevisi] = useState("");
  const [barisRevisi, setBarisRevisi] = useState<BarisRevisi[]>([]);
  const [produkPilih, setProdukPilih] = useState("");
  const [sibukRevisi, setSibukRevisi] = useState(false);
  const [galatRevisi, setGalatRevisi] = useState<string | null>(null);
  const [galatField, setGalatField] = useState<Record<string, string>>({});

  // Daftar produk + harga khusus cabang hanya diambil saat modal revisi dibuka
  // (matriks harga: sel tanpa harga = produk tidak dijual ke cabang itu).
  const { data: matriks } = useApi<MatriksHarga>(
    `/admin/prices?branch_id=${data?.branch_id ?? ""}&aktif=true`,
    revisiBuka && Boolean(data?.branch_id),
  );

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

  /**
   * unduhDokumen mengambil tautan presigned berumur pendek untuk satu dokumen
   * (invoice/surat jalan) lalu membukanya di tab baru. Kunci object storage
   * tidak pernah sampai ke browser (F1-17, SystemDesign.md §8); mencetak
   * dilakukan dari PDF yang terbuka.
   */
  const unduhDokumen = useCallback(
    async (d: DokumenPesanan) => {
      try {
        const dl = await api.get<{ url: string; nama_file: string }>(`/documents/${d.id}/download`);
        window.open(dl.url, "_blank", "noopener");
        tampilkan(`Unduhan ${dl.nama_file} disiapkan.`);
      } catch (err) {
        tampilkan(pesanGalat(err));
      }
    },
    [tampilkan],
  );

  /**
   * buatDokumen memicu render dokumen versi berjalan. Endpointnya idempoten:
   * dokumen yang sudah ada tidak dibuat ulang dan berkas versi lama tidak
   * tersentuh.
   */
  const buatDokumen = useCallback(async () => {
    setSibukDokumen(true);
    try {
      await api.post(`/admin/orders/${id}/documents`, {});
      muatUlang();
      tampilkan("Dokumen dibuat.");
    } catch (err) {
      tampilkan(pesanGalat(err));
    } finally {
      setSibukDokumen(false);
    }
  }, [id, muatUlang, tampilkan]);

  /** bukaVerifikasi menyiapkan modal verifikasi dari keadaan bersih. */
  const bukaVerifikasi = useCallback(() => {
    setKeputusanVerif("cocok");
    setNominalVerif("");
    setAlasanVerif(PILIHAN_ALASAN[0]);
    setCatatanVerif("");
    setGalatVerif(null);
    const defaultAccount = rekeningOrder?.accounts?.length === 1 ? rekeningOrder.accounts[0]?.id ?? "" : "";
    setTujuanBukti(Object.fromEntries((bukti ?? []).map((b) => [b.id, defaultAccount])));
    setVerifBuka(true);
  }, [bukti, rekeningOrder]);

  const nilaiUlangRekening = useCallback(async () => {
    setSibukNilaiUlang(true);
    setGalatVerif(null);
    try {
      await api.post(`/admin/orders/${id}/verification/reevaluate-destination`, {});
      tampilkan("Penilaian ulang rekening telah diantrekan dari hasil OCR tersimpan.");
      muatUlang();
    } catch (err) {
      setGalatVerif(pesanVerifikasi(err));
    } finally {
      setSibukNilaiUlang(false);
    }
  }, [id, muatUlang, tampilkan]);

  /**
   * kirimVerifikasi mengirim keputusan verifikasi MANUAL ke
   * POST /api/v1/admin/orders/{id}/verify (PRD §4.2 AP-ORD-07, §5).
   *
   * `cocok` menyetujui → pesanan menjadi Done; dokumen + email lahir dari
   * worker setelahnya, jadi halaman ini cukup menyegarkan detail. `perlu_review`
   * hanya menandai sub-status verifikasi dan WAJIB punya alasan tertutup;
   * pesanan tetap Submitted.
   */
  const kirimVerifikasi = useCallback(async () => {
    if (keputusanVerif === "perlu_review" && alasanVerif === "") {
      setGalatVerif("Pilih alasan review terlebih dahulu.");
      return;
    }
    const proofDestinations = (bukti ?? [])
      .map((b) => ({
        payment_proof_id: b.id,
        bank_account_id:
          tujuanBukti[b.id] ?? (rekeningOrder?.accounts?.length === 1 ? rekeningOrder.accounts[0]?.id ?? "" : ""),
      }))
      .filter((d) => d.bank_account_id !== "");
    if (keputusanVerif === "cocok" && proofDestinations.length !== (bukti?.length ?? 0)) {
      setGalatVerif("Pilih rekening tujuan efektif untuk setiap bukti sebelum menyetujui.");
      return;
    }
    setSibukVerif(true);
    setGalatVerif(null);
    const terbaca = parseNominal(nominalVerif);
    try {
      await api.post(`/admin/orders/${id}/verify`, {
        hasil: keputusanVerif,
        ...(terbaca !== null ? { total_terbaca: terbaca } : {}),
        ...(keputusanVerif === "perlu_review" ? { alasan: alasanVerif } : {}),
        ...(catatanVerif.trim() !== "" ? { catatan: catatanVerif.trim() } : {}),
        proof_destinations: proofDestinations,
      });
      setVerifBuka(false);
      muatUlang();
      tampilkan(
        keputusanVerif === "cocok"
          ? "Pembayaran diverifikasi. Invoice & surat jalan sedang dibuat."
          : "Pesanan ditandai perlu review.",
      );
    } catch (err) {
      setGalatVerif(pesanVerifikasi(err));
      // Verifikasi paralel sudah menyelesaikan pesanan: segarkan supaya log
      // dan dokumen terbaru tampil di halaman ini.
      if (err instanceof ApiClientError && err.code === "ORDER_ALREADY_DONE") muatUlang();
      if (err instanceof ApiClientError && err.code === "BANK_ACCOUNT_NOT_ALLOWED") muatRekeningOrder();
    } finally {
      setSibukVerif(false);
    }
  }, [id, keputusanVerif, alasanVerif, nominalVerif, catatanVerif, bukti, tujuanBukti, rekeningOrder, muatUlang, muatRekeningOrder, tampilkan]);

  /** Produk yang benar-benar dijual ke cabang pesanan ini (matriks harga cabang). */
  const produkRevisi = useMemo(() => {
    if (!matriks || !data) return [];
    const hargaPer = new Map<string, number>();
    for (const s of matriks.sel) {
      if (s.branch_id === data.branch_id) hargaPer.set(s.product_id, s.harga);
    }
    return matriks.produk
      .filter((p) => p.is_active && hargaPer.has(p.id))
      .map((p) => ({ id: p.id, nama: p.nama, harga: hargaPer.get(p.id) ?? 0 }));
  }, [matriks, data]);

  /**
   * bukaRevisi menyiapkan modal dari keadaan bersih: daftar baris FINAL dimulai
   * dari baris produk versi berjalan (baris biaya packaging tidak dapat diedit
   * klien dan ditambahkan server sendiri).
   */
  const bukaRevisi = useCallback(() => {
    setAlasanRevisi("");
    setCatatanRevisi("");
    setProdukPilih("");
    setGalatRevisi(null);
    setGalatField({});
    setBarisRevisi(
      (data?.lines ?? [])
        .filter((l) => l.jenis !== "packaging")
        .map((l, i) => ({
          kunci: `lama-${l.line_no}-${i}`,
          line_no: l.line_no,
          nama: l.nama,
          serial_number: l.serial_number,
          qty: String(l.qty),
          harga: String(l.harga_satuan),
        })),
    );
    setRevisiBuka(true);
  }, [data?.lines]);

  const ubahBaris = useCallback((kunci: string, patch: Partial<BarisRevisi>) => {
    setBarisRevisi((kini) => kini.map((b) => (b.kunci === kunci ? { ...b, ...patch } : b)));
  }, []);

  const hapusBaris = useCallback((kunci: string) => {
    setBarisRevisi((kini) => kini.filter((b) => b.kunci !== kunci));
  }, []);

  // Penghitung monoton untuk kunci baris baru. Kunci TIDAK boleh diturunkan
  // dari panjang daftar: setelah sebuah baris dihapus lalu produk yang sama
  // ditambahkan lagi, kunci lama akan terpakai ulang (duplikat) sehingga
  // ubahBaris/hapusBaris ikut mengenai baris kembarannya.
  const urutanKunciBaris = useRef(0);

  const tambahBaris = useCallback(() => {
    const p = produkRevisi.find((x) => x.id === produkPilih);
    if (!p) return;
    urutanKunciBaris.current += 1;
    const kunci = `baru-${p.id}-${urutanKunciBaris.current}`;
    setBarisRevisi((kini) => [
      ...kini,
      {
        kunci,
        line_no: 0,
        product_id: p.id,
        nama: p.nama,
        qty: "1",
        harga: String(p.harga),
      },
    ]);
    setProdukPilih("");
  }, [produkRevisi, produkPilih]);

  /**
   * kirimRevisi mengirim daftar baris FINAL ke
   * POST /api/v1/admin/orders/{id}/revise (PRD §4.3 AP-REV-01, SystemDesign.md
   * §4.5). Alasan wajib; baris lama dikirim dengan `line_no`, baris baru dengan
   * `product_id`; total dihitung ulang server, jadi klien tidak pernah mengirim
   * total maupun versi. Server tetap otoritas izin (Manager hanya bila
   * `manager_boleh_revisi` aktif).
   */
  const kirimRevisi = useCallback(async () => {
    setGalatField({});
    if (alasanRevisi.trim() === "") {
      setGalatRevisi("Alasan revisi wajib diisi.");
      setGalatField({ alasan: "alasan revisi wajib diisi" });
      return;
    }
    if (barisRevisi.length === 0) {
      setGalatRevisi("Revisi butuh minimal satu baris produk.");
      return;
    }
    const lines: Record<string, unknown>[] = [];
    for (const b of barisRevisi) {
      const qty = uraiBilanganBulat(b.qty);
      const harga = uraiBilanganBulat(b.harga);
      if (qty === null || qty < 1) {
        setGalatRevisi(`Qty baris "${b.nama}" harus bilangan bulat minimal 1.`);
        return;
      }
      if (harga === null) {
        setGalatRevisi(`Harga satuan baris "${b.nama}" harus bilangan bulat non-negatif.`);
        return;
      }
      lines.push(
        b.line_no > 0
          ? { line_no: b.line_no, qty, harga_satuan: harga }
          : { product_id: b.product_id, qty, harga_satuan: harga },
      );
    }
    setSibukRevisi(true);
    setGalatRevisi(null);
    try {
      await api.post(`/admin/orders/${id}/revise`, {
        alasan: alasanRevisi.trim(),
        ...(catatanRevisi.trim() !== "" ? { catatan: catatanRevisi.trim() } : {}),
        lines,
      });
      setRevisiBuka(false);
      muatUlang();
      muatEmail();
      tampilkan(
        "Revisi tersimpan. Nomor invoice tetap dan versi baru sedang dirender lalu dikirim ke cabang & admin ber-flag (invoice saja; surat jalan tidak dikirim ulang). Proses render & email berjalan asinkron.",
      );
    } catch (err) {
      setGalatRevisi(pesanRevisi(err));
      if (err instanceof ApiClientError) {
        setGalatField(err.fields ?? {});
        // Fail-closed 409 (dan 400 keadaan) berarti server berpindah keadaan:
        // muat ulang detail + dokumen supaya status terkini terlihat, TANPA
        // mengubah versi lokal atau mencoba ulang sendiri.
        if (err.status === 409) {
          muatUlang();
          muatEmail();
        }
      }
    } finally {
      setSibukRevisi(false);
    }
  }, [id, alasanRevisi, catatanRevisi, barisRevisi, muatUlang, muatEmail, tampilkan]);

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

  // Selisih ditampilkan live dari angka yang diketik admin; server tetap
  // menghitung ulang dari kolom generated (angka klien tidak dipercaya).
  const nominalTerbaca = parseNominal(nominalVerif);
  const selisihVerif = (nominalTerbaca ?? 0) - data.total;
  const semuaOCRTersedia = (bukti?.length ?? 0) > 0 && (bukti ?? []).every((b) => Boolean(b.ocr_at));
  const logTerbaru = [...data.verifikasi].sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
  const rekeningPerluDinilaiUlang =
    logTerbaru?.metode === "ai" &&
    logTerbaru.branch_allowlist_version !== undefined &&
    rekeningOrder !== null &&
    logTerbaru.branch_allowlist_version !== rekeningOrder.allowlist_version;
  const rekeningDefault = rekeningOrder?.accounts?.length === 1 ? rekeningOrder.accounts[0]?.id ?? "" : "";
  const semuaTujuanTerpilih =
    (bukti?.length ?? 0) > 0 && (bukti ?? []).every((b) => Boolean(tujuanBukti[b.id] ?? rekeningDefault));

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
            <CardHead
              title="Dokumen"
              tools={
                data.status === "done" ? (
                  <Button
                    variant="secondary"
                    icon="arrows-clockwise"
                    onClick={buatDokumen}
                    disabled={sibukDokumen}
                  >
                    {sibukDokumen ? "Memproses…" : "Buat dokumen"}
                  </Button>
                ) : undefined
              }
            />
            {data.dokumen.length === 0 ? (
              <p className="muted small">Belum ada dokumen. Invoice &amp; surat jalan lahir setelah verifikasi.</p>
            ) : (
              <ul className={styles.daftarPolos}>
                {data.dokumen.map((d) => (
                  <li key={d.id} className={styles.barisDokumen}>
                    <span>{LABEL_DOKUMEN[d.jenis] ?? d.jenis}</span>
                    <span className="muted small">{`v${d.versi} · ${formatWaktu(d.created_at)}`}</span>
                    <Button variant="secondary" icon="download-simple" onClick={() => unduhDokumen(d)}>
                      Unduh
                    </Button>
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
          {data.status === "submitted" && bolehVerif ? (
            <Card>
              <CardHead title="Verifikasi pembayaran" />
              {rekeningPerluDinilaiUlang ? (
                <Notice tone="warning">Rekening perlu dinilai ulang karena kebijakan cabang telah berubah.</Notice>
              ) : null}
              {(rekeningOrder?.accounts?.length ?? 0) === 0 ? (
                <Notice tone="danger">
                  Cabang tidak mempunyai rekening efektif. Approval ditolak; minta Superadmin memperbaiki assignment.
                </Notice>
              ) : null}
              <p className="muted small" style={{ marginBottom: 8 }}>
                Setelah disetujui, pesanan menjadi Selesai dan invoice &amp; surat jalan dibuat lalu
                dikirim otomatis.
              </p>
              <div className={styles.row} style={{ justifyContent: "space-between" }}>
                <span className="muted small">Total pesanan</span>
                <strong className="tabular">{rupiah.format(data.total)}</strong>
              </div>
              <Button
                variant="primary"
                icon="check-circle"
                block
                onClick={bukaVerifikasi}
                disabled={sibukVerif}
                data-uji="buka-verifikasi"
              >
                Verifikasi
              </Button>
              <Button
                variant="secondary"
                block
                onClick={() => void nilaiUlangRekening()}
                disabled={sibukNilaiUlang || rekeningOrder?.ai_active !== true || !semuaOCRTersedia}
              >
                {sibukNilaiUlang ? "Mengantrekan…" : "Nilai ulang rekening"}
              </Button>
              {rekeningOrder?.ai_active === false ? (
                <p className="muted small">Nilai ulang tidak tersedia karena AI/OCR sedang dinonaktifkan.</p>
              ) : !semuaOCRTersedia ? (
                <p className="muted small">Nilai ulang tersedia setelah seluruh bukti mempunyai hasil OCR tersimpan.</p>
              ) : null}
              {galatVerif && !verifBuka ? <Notice tone="danger">{galatVerif}</Notice> : null}
            </Card>
          ) : null}

          {data.status === "done" && bolehRev ? (
            <Card>
              <CardHead title="Revisi invoice" />
              <p className="muted small" style={{ marginBottom: 8 }}>
                Nomor invoice tetap; versi naik (R1, R2, …) dan PDF versi lama tetap tersimpan. Alasan wajib diisi dan
                total dihitung ulang server.
              </p>
              <Button
                variant="secondary"
                icon="pencil-simple"
                block
                onClick={bukaRevisi}
                data-uji="buka-revisi"
              >
                Revisi invoice
              </Button>
            </Card>
          ) : null}

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

      <Modal
        open={verifBuka}
        title="Verifikasi pembayaran"
        onClose={() => {
          if (!sibukVerif) setVerifBuka(false);
        }}
        footer={
          <>
            <Button variant="secondary" onClick={() => setVerifBuka(false)} disabled={sibukVerif}>
              Batal
            </Button>
            <Button
              variant={keputusanVerif === "cocok" ? "primary" : "secondary"}
              onClick={() => void kirimVerifikasi()}
              disabled={
                sibukVerif ||
                (keputusanVerif === "cocok" &&
                  (!semuaTujuanTerpilih || (rekeningOrder?.accounts?.length ?? 0) === 0))
              }
              data-uji="kirim-verifikasi"
            >
              {sibukVerif
                ? "Mengirim…"
                : keputusanVerif === "cocok"
                  ? "Setujui & selesaikan"
                  : "Tandai perlu review"}
            </Button>
          </>
        }
      >
        <p className="muted small">
          {data.nomor ? `Pesanan ${data.nomor} · ` : ""}
          {data.branch_nama}
        </p>
        <div className={styles.row} style={{ justifyContent: "space-between", margin: "8px 0" }}>
          <span className="muted small">Total pesanan</span>
          <strong className="tabular" data-uji="total-verifikasi">
            {rupiah.format(data.total)}
          </strong>
        </div>
        {(bukti?.length ?? 0) === 0 ? (
          <Notice tone="danger">Pesanan belum mempunyai bukti pembayaran untuk dipetakan.</Notice>
        ) : (
          <Table caption="Tujuan rekening per bukti">
            <thead>
              <tr>
                <th scope="col">Bukti / OCR</th>
                <th scope="col">Hasil allowlist terakhir</th>
                <th scope="col">Rekening efektif</th>
              </tr>
            </thead>
            <tbody>
              {(bukti ?? []).map((b) => {
                const hasil = logTerbaru?.destination_results?.find((d) => d.payment_proof_id === b.id);
                return (
                  <tr key={b.id}>
                    <td>
                      <b>{b.nama_file}</b>
                      <div className="muted small">
                        {b.ocr_at
                          ? `${b.ocr_bank || "Bank tak terbaca"} · digit akhir ${b.ocr_rek_akhir || "—"} · keyakinan ${Math.round((b.ocr_confidence ?? 0) * 100)}%`
                          : "Belum ada hasil OCR"}
                      </div>
                    </td>
                    <td>{labelStatusRekening(hasil?.status)}</td>
                    <td>
                      <Select
                        aria-label={`Rekening tujuan ${b.nama_file}`}
                        value={tujuanBukti[b.id] ?? rekeningDefault}
                        onChange={(e) => setTujuanBukti((lama) => ({ ...lama, [b.id]: e.target.value }))}
                      >
                        <option value="">Pilih rekening</option>
                        {(rekeningOrder?.accounts ?? []).map((r) => (
                          <option key={r.id} value={r.id}>
                            {`${r.bank} · ${r.display_nomor} · ${r.nama_pemilik}`}
                          </option>
                        ))}
                      </Select>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </Table>
        )}
        <Field
          label="Keputusan"
          htmlFor="verif-keputusan"
          hint="Setujui menyelesaikan pesanan dan mengirim invoice/surat jalan. Perlu review menandai sub-status; pesanan tetap Submitted."
        >
          <Select
            id="verif-keputusan"
            value={keputusanVerif}
            onChange={(e) => setKeputusanVerif(e.target.value as KeputusanVerifikasi)}
          >
            <option value="cocok">Setujui (pesanan selesai)</option>
            <option value="perlu_review">Tandai perlu review</option>
          </Select>
        </Field>
        <Field
          label="Nominal di bukti transfer"
          htmlFor="verif-nominal"
          hint="Kosongkan bila bukti tidak terbaca atau pembayaran tunai C&C. Selisih = nominal terbaca − total pesanan."
        >
          <Input
            id="verif-nominal"
            inputMode="numeric"
            placeholder="Contoh: 200000"
            value={nominalVerif}
            onChange={(e) => setNominalVerif(e.target.value)}
          />
        </Field>
        <div className={styles.row} style={{ justifyContent: "space-between", margin: "4px 0 12px" }}>
          <span className="muted small">Selisih</span>
          <span
            className="tabular"
            data-uji="selisih-verifikasi"
            style={selisihVerif === 0 ? undefined : { color: "var(--danger-fg)" }}
          >
            {nominalTerbaca === null ? "—" : rupiah.format(selisihVerif)}
          </span>
        </div>
        {keputusanVerif === "perlu_review" ? (
          <Field label="Alasan review" htmlFor="verif-alasan">
            <Select id="verif-alasan" value={alasanVerif} onChange={(e) => setAlasanVerif(e.target.value)}>
              {PILIHAN_ALASAN.map((a) => (
                <option key={a} value={a}>
                  {LABEL_ALASAN[a]}
                </option>
              ))}
            </Select>
          </Field>
        ) : null}
        <Field
          label="Catatan verifikasi"
          htmlFor="verif-catatan"
          hint="Opsional. Ikut tercatat di log verifikasi dan timeline pesanan."
        >
          <Textarea
            id="verif-catatan"
            maxLength={2000}
            placeholder="Contoh: selisih ditutup transfer susulan"
            value={catatanVerif}
            onChange={(e) => setCatatanVerif(e.target.value)}
          />
        </Field>
        {galatVerif ? <Notice tone="danger">{galatVerif}</Notice> : null}
      </Modal>

      <Modal
        open={revisiBuka}
        title="Revisi invoice"
        onClose={() => {
          if (!sibukRevisi) setRevisiBuka(false);
        }}
        wide
        footer={
          <>
            <Button variant="secondary" onClick={() => setRevisiBuka(false)} disabled={sibukRevisi}>
              Batal
            </Button>
            <Button
              variant="primary"
              onClick={() => void kirimRevisi()}
              disabled={sibukRevisi}
              data-uji="kirim-revisi"
            >
              {sibukRevisi ? "Menyimpan…" : "Simpan revisi"}
            </Button>
          </>
        }
      >
        <p className="muted small">
          {data.nomor ? `Pesanan ${data.nomor} · ` : ""}
          {`versi berjalan R${data.versi}; versi baru R${data.versi + 1}.`}
        </p>
        {galatRevisi ? <Notice tone="danger">{galatRevisi}</Notice> : null}
        <Field
          label="Alasan revisi"
          htmlFor="revisi-alasan"
          hint="Wajib. Tercatat di timeline dan audit log (AP-REV-05)."
          error={galatField.alasan}
        >
          <Textarea
            id="revisi-alasan"
            maxLength={500}
            placeholder="Contoh: harga satuan salah, cabang minta koreksi"
            value={alasanRevisi}
            onChange={(e) => setAlasanRevisi(e.target.value)}
          />
        </Field>
        <Field label="Catatan revisi (opsional)" htmlFor="revisi-catatan" hint="Catatan bebas; tidak wajib.">
          <Textarea
            id="revisi-catatan"
            maxLength={2000}
            value={catatanRevisi}
            onChange={(e) => setCatatanRevisi(e.target.value)}
          />
        </Field>

        <div className={styles.row} style={{ justifyContent: "space-between", margin: "8px 0" }}>
          <span className="muted small">Baris final (bukan diff)</span>
          <span className="muted small">{`${barisRevisi.length} baris`}</span>
        </div>
        {barisRevisi.length === 0 ? (
          <p className="muted small">Belum ada baris. Tambahkan minimal satu produk dari matriks harga cabang.</p>
        ) : (
          <Table caption="Baris final revisi invoice">
            <thead>
              <tr>
                <th scope="col">Produk</th>
                <th scope="col" className="num">
                  Qty
                </th>
                <th scope="col" className="num">
                  Harga satuan
                </th>
                <th scope="col">Aksi</th>
              </tr>
            </thead>
            <tbody>
              {barisRevisi.map((b) => (
                <tr key={b.kunci}>
                  <td>
                    {b.nama}
                    {b.line_no === 0 ? <Badge tone="rev">Baru</Badge> : null}
                    {b.serial_number ? <div className="muted small">{b.serial_number}</div> : null}
                  </td>
                  <td className="num">
                    <Input
                      aria-label={`Qty ${b.nama}`}
                      inputMode="numeric"
                      value={b.qty}
                      onChange={(e) => ubahBaris(b.kunci, { qty: e.target.value })}
                    />
                  </td>
                  <td className="num">
                    <Input
                      aria-label={`Harga satuan ${b.nama}`}
                      inputMode="numeric"
                      value={b.harga}
                      onChange={(e) => ubahBaris(b.kunci, { harga: e.target.value })}
                    />
                  </td>
                  <td>
                    <Button variant="ghost" aria-label={`Hapus ${b.nama}`} onClick={() => hapusBaris(b.kunci)}>
                      Hapus
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        {galatField.lines || galatField.pesanan ? (
          <Notice tone="danger">{galatField.lines ?? galatField.pesanan}</Notice>
        ) : null}

        <Field
          label="Tambah produk"
          htmlFor="revisi-produk"
          hint="Baris baru memakai serial baru bertanggal pesanan asli; baris lama mempertahankan serial/produknya."
        >
          <Select id="revisi-produk" value={produkPilih} onChange={(e) => setProdukPilih(e.target.value)}>
            <option value="">Pilih produk…</option>
            {produkRevisi.map((p) => (
              <option key={p.id} value={p.id}>{`${p.nama} · ${rupiah.format(p.harga)}`}</option>
            ))}
          </Select>
        </Field>
        <Button variant="secondary" icon="plus" onClick={tambahBaris} disabled={produkPilih === ""}>
          Tambah baris
        </Button>
        <p className="muted small" style={{ marginTop: 8 }}>
          Total versi baru dihitung server dari baris di atas. Baris biaya packaging (pesanan Sertifikat) ditambahkan
          otomatis dan tidak perlu dimasukkan di sini.
        </p>
      </Modal>
    </>
  );
}
