import { useCallback, useRef, useState, type FormEvent } from "react";

import { ApiClientError, api, buatIdempotencyKey } from "@grahita/api-client";
import {
  Badge,
  Button,
  Card,
  CardHead,
  EmptyState,
  Field,
  Input,
  Notice,
  PageHead,
  Select,
  SkeletonList,
  Table,
} from "@grahita/ui";

import { useApi } from "../useApi";
import styles from "./pesanan.module.css";

/*
 * Deposit cabang (C4; D3 §7.1). Saldo hanya per cabang sesi; top-up selalu
 * pending sampai Superadmin memverifikasi, jadi nominal pending TIDAK pernah
 * dijumlahkan ke saldo. Semua mutasi membawa Idempotency-Key per niat dan
 * versi journal yang dilihat layar: bila saldo berubah, server menolak (409)
 * dan layar memuat ulang — tidak ada pengurangan diam-diam.
 */

const rupiah = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });
const waktu = new Intl.DateTimeFormat("id-ID", {
  day: "2-digit",
  month: "short",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  timeZone: "Asia/Jakarta",
});

export interface RingkasanDeposit {
  branch_id: string;
  available_balance: number;
  journal_version: number;
  pending_topup_count: number;
  pending_topup_amount_declared: number;
}

interface TopupDeposit {
  id: string;
  bank: string;
  display_nomor: string;
  status: string;
  declared_amount: number;
  verified_amount?: number;
  rejection_reason?: string;
  created_at: string;
}

interface TransaksiDeposit {
  id: string;
  kind: string;
  amount: number;
  order_nomor?: string;
  created_at: string;
}

interface RekeningPembayaran {
  id: string;
  bank: string;
  nama_pemilik: string;
  nomor: string;
}

export interface AlokasiDeposit {
  id: string;
  order_version: number;
  amount: number;
  reversed_amount: number;
  active_amount: number;
  created_at: string;
}

/** PembayaranPesanan adalah respons GET /orders/{id}/payment. */
export interface PembayaranPesanan {
  order_id: string;
  status: string;
  versi: number;
  current_total: number;
  revision_payment_state?: string;
  committed_deposit: number;
  remaining_due: number;
  is_lunas: boolean;
  settlement?: { order_version: number; deposit_amount: number; transfer_amount: number };
  allocations: AlokasiDeposit[];
  balance: RingkasanDeposit;
}

const LABEL_TOPUP: Record<string, string> = {
  pending_review: "Menunggu verifikasi",
  needs_review: "Perlu review",
  verified: "Masuk saldo",
  rejected: "Ditolak",
  reversed: "Dibatalkan",
};
const TONE_TOPUP: Record<string, "warning" | "danger" | "success" | "neutral"> = {
  pending_review: "warning",
  needs_review: "danger",
  verified: "success",
  rejected: "neutral",
  reversed: "neutral",
};
const LABEL_TRANSAKSI: Record<string, string> = {
  topup_credit: "Top-up",
  order_allocation_debit: "Dipakai untuk pesanan",
  overpay_credit: "Kelebihan bayar dikreditkan",
  allocation_reversal_credit: "Pemakaian dibatalkan",
  topup_reversal_debit: "Koreksi top-up",
  overpay_reversal_debit: "Koreksi kelebihan bayar",
};

/** pesanDeposit menerjemahkan galat API deposit ke kalimat yang terarah. */
export function pesanDeposit(err: unknown): string {
  if (!(err instanceof ApiClientError)) return "Terjadi kesalahan. Coba lagi.";
  switch (err.code) {
    case "DEPOSIT_VERSION_CONFLICT":
    case "ORDER_VERSION_CONFLICT":
      return "Saldo atau pesanan baru saja berubah. Data dimuat ulang — periksa lalu kirim lagi.";
    case "DEPOSIT_BALANCE_INSUFFICIENT":
      return "Saldo deposit tidak cukup untuk nominal itu. Data dimuat ulang.";
    case "DEPOSIT_ALLOCATION_EXCEEDS_ORDER":
      return "Nominal melebihi sisa tagihan pesanan.";
    case "BANK_ACCOUNT_NOT_ALLOWED":
    case "BRANCH_BANK_ACCOUNT_UNAVAILABLE":
      return "Rekening tujuan tidak berlaku untuk cabang Anda. Jangan transfer; hubungi admin.";
    case "PAYMENT_PROOF_ALREADY_USED":
      return "Bukti ini sudah dipakai untuk top-up lain.";
    default:
      return err.message;
  }
}

/** nominalDari membaca rupiah bulat positif dari ketikan; null bila tidak sah. */
export function nominalDari(teks: string): number | null {
  const bersih = teks.replace(/[.\s]/g, "");
  if (!/^\d+$/.test(bersih)) return null;
  const n = Number(bersih);
  return Number.isSafeInteger(n) && n > 0 ? n : null;
}

/** KartuSaldo menampilkan saldo tersedia terpisah dari top-up pending. */
function KartuSaldo({ data }: { data: RingkasanDeposit }) {
  return (
    <Card>
      <CardHead title="Saldo deposit cabang" />
      <dl className={styles.metaGrid}>
        <div>
          <dt>Saldo tersedia</dt>
          <dd className="tabular" data-uji="saldo-tersedia">
            <b>{rupiah.format(data.available_balance)}</b>
          </dd>
        </div>
        <div>
          <dt>Top-up menunggu verifikasi</dt>
          <dd className="tabular">
            {data.pending_topup_count > 0
              ? `${data.pending_topup_count} · ${rupiah.format(data.pending_topup_amount_declared)}`
              : "—"}
          </dd>
        </div>
      </dl>
      {data.pending_topup_count > 0 ? (
        <p className="muted small">Top-up pending belum menambah saldo sampai diverifikasi admin Pusat.</p>
      ) : null}
    </Card>
  );
}

/** FormTopup mengunggah bukti top-up ke rekening efektif cabang. */
function FormTopup({ onSukses }: { onSukses: () => void }) {
  const rekening = useApi<RekeningPembayaran[]>("/bank-accounts");
  const [bank, setBank] = useState("");
  const [nominal, setNominal] = useState("");
  const [berkas, setBerkas] = useState<File | null>(null);
  const [sibuk, setSibuk] = useState(false);
  const [galat, setGalat] = useState<string | null>(null);
  const kunci = useRef<string | null>(null);
  const daftar = rekening.data ?? [];
  const bankTerpilih = bank || (daftar.length === 1 ? (daftar[0]?.id ?? "") : "");

  const kirim = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      const n = nominalDari(nominal);
      if (!bankTerpilih) return setGalat("Pilih rekening tujuan transfer.");
      if (n === null) return setGalat("Isi nominal transfer dalam rupiah bulat.");
      if (!berkas) return setGalat("Unggah bukti transfer top-up.");
      kunci.current ??= buatIdempotencyKey();
      const form = new FormData();
      form.set("bank_account_id", bankTerpilih);
      form.set("declared_amount", String(n));
      form.set("proof", berkas);
      setSibuk(true);
      setGalat(null);
      try {
        await api.postForm("/deposit/topups", form, { idempotencyKey: kunci.current });
        kunci.current = null;
        setNominal("");
        setBerkas(null);
        onSukses();
      } catch (err) {
        setGalat(pesanDeposit(err));
        if (err instanceof ApiClientError && err.status === 409) {
          kunci.current = null;
          rekening.muatUlang();
        }
      } finally {
        setSibuk(false);
      }
    },
    [bankTerpilih, nominal, berkas, onSukses, rekening],
  );

  return (
    <Card>
      <CardHead title="Top-up deposit" />
      {rekening.memuat ? <SkeletonList rows={2} /> : null}
      {!rekening.memuat && daftar.length === 0 ? (
        <Notice tone="danger">Rekening tujuan cabang belum ditetapkan. Jangan transfer; hubungi admin.</Notice>
      ) : null}
      {daftar.length > 0 ? (
        <form onSubmit={(e) => void kirim(e)} className={styles.stack}>
          <Field label="Rekening tujuan" htmlFor="topup-bank">
            <Select id="topup-bank" value={bankTerpilih} onChange={(e) => setBank(e.target.value)} aria-label="Rekening tujuan">
              <option value="">Pilih rekening</option>
              {daftar.map((r) => (
                <option key={r.id} value={r.id}>{`${r.bank} · ${r.nomor} · ${r.nama_pemilik}`}</option>
              ))}
            </Select>
          </Field>
          <Field label="Nominal ditransfer" htmlFor="topup-nominal">
            <Input id="topup-nominal" inputMode="numeric" value={nominal} onChange={(e) => setNominal(e.target.value)} aria-label="Nominal top-up" />
          </Field>
          <Field label="Bukti transfer" htmlFor="topup-bukti">
            <input
              id="topup-bukti"
              type="file"
              accept="image/jpeg,image/png,application/pdf"
              aria-label="Bukti top-up"
              onChange={(e) => setBerkas(e.target.files?.[0] ?? null)}
            />
          </Field>
          {galat ? <Notice tone="danger">{galat}</Notice> : null}
          <Button type="submit" variant="primary" icon="upload-simple" disabled={sibuk}>
            {sibuk ? "Mengirim…" : "Kirim top-up"}
          </Button>
        </form>
      ) : null}
    </Card>
  );
}

/** DepositPage (/deposit): saldo, top-up, dan riwayat journal cabang sesi. */
export function DepositPage() {
  const ringkasan = useApi<RingkasanDeposit>("/deposit");
  const topup = useApi<TopupDeposit[]>("/deposit/topups?limit=50");
  const transaksi = useApi<TransaksiDeposit[]>("/deposit/transactions?limit=50");
  const [sukses, setSukses] = useState<string | null>(null);

  const setelahTopup = useCallback(() => {
    setSukses("Top-up terkirim dan menunggu verifikasi admin Pusat. Saldo belum bertambah.");
    ringkasan.muatUlang();
    topup.muatUlang();
  }, [ringkasan, topup]);

  return (
    <>
      <PageHead title="Deposit" sub="Saldo deposit milik cabang Anda" />
      <div className={styles.detailGrid}>
        <div className={styles.stack}>
          {ringkasan.memuat ? <SkeletonList rows={2} /> : null}
          {ringkasan.galat ? <Notice tone="danger">{ringkasan.galat}</Notice> : null}
          {ringkasan.data ? <KartuSaldo data={ringkasan.data} /> : null}
          {sukses ? <Notice tone="success">{sukses}</Notice> : null}
          <FormTopup onSukses={setelahTopup} />
          <Card>
            <CardHead title="Riwayat top-up" />
            {(topup.data ?? []).length === 0 && !topup.memuat ? (
              <EmptyState icon="wallet">Belum ada top-up.</EmptyState>
            ) : (
              <Table caption="Riwayat top-up deposit">
                <thead>
                  <tr>
                    <th scope="col">Tanggal</th>
                    <th scope="col">Rekening</th>
                    <th scope="col" className="num">Nominal</th>
                    <th scope="col">Status</th>
                  </tr>
                </thead>
                <tbody>
                  {(topup.data ?? []).map((t) => (
                    <tr key={t.id}>
                      <td>{waktu.format(new Date(t.created_at))}</td>
                      <td>{`${t.bank} ${t.display_nomor}`}</td>
                      <td className="num tabular">
                        {rupiah.format(t.verified_amount ?? t.declared_amount)}
                        {t.verified_amount !== undefined && t.verified_amount !== t.declared_amount ? (
                          <div className="muted small">{`diajukan ${rupiah.format(t.declared_amount)}`}</div>
                        ) : null}
                      </td>
                      <td>
                        <Badge tone={TONE_TOPUP[t.status] ?? "neutral"}>{LABEL_TOPUP[t.status] ?? t.status}</Badge>
                        {t.rejection_reason ? <div className="muted small">{t.rejection_reason}</div> : null}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
          <Card>
            <CardHead title="Mutasi saldo" />
            {(transaksi.data ?? []).length === 0 && !transaksi.memuat ? (
              <EmptyState icon="list">Belum ada mutasi saldo.</EmptyState>
            ) : (
              <Table caption="Mutasi saldo deposit">
                <thead>
                  <tr>
                    <th scope="col">Tanggal</th>
                    <th scope="col">Keterangan</th>
                    <th scope="col" className="num">Nominal</th>
                  </tr>
                </thead>
                <tbody>
                  {(transaksi.data ?? []).map((t) => (
                    <tr key={t.id}>
                      <td>{waktu.format(new Date(t.created_at))}</td>
                      <td>
                        {LABEL_TRANSAKSI[t.kind] ?? t.kind}
                        {t.order_nomor ? <span className="muted small">{` · ${t.order_nomor}`}</span> : null}
                      </td>
                      <td className="num tabular">{rupiah.format(t.amount)}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </div>
      </div>
    </>
  );
}

/** statusBayar merangkum keadaan pembayaran pesanan tanpa klaim LUNAS prematur. */
export function statusBayar(p: PembayaranPesanan): { label: string; tone: "success" | "warning" | "danger" } {
  if (p.is_lunas) return { label: "Lunas", tone: "success" };
  if (p.revision_payment_state === "revision_underpayment") return { label: "Revisi: kurang bayar", tone: "danger" };
  if (p.revision_payment_state === "revision_overpayment_pending") return { label: "Revisi: menunggu keputusan lebih bayar", tone: "warning" };
  if (p.remaining_due === 0) return { label: "Menunggu verifikasi", tone: "warning" };
  return { label: "Belum lunas", tone: "warning" };
}

/**
 * KartuPembayaranPesanan menampilkan rincian deposit/transfer satu pesanan dan,
 * selama pesanan Submitted, formulir "Gunakan deposit".
 */
export function KartuPembayaranPesanan({ id }: { id: string }) {
  const { data, memuat, galat, muatUlang } = useApi<PembayaranPesanan>(`/orders/${id}/payment`);
  const [nominal, setNominal] = useState("");
  const [sibuk, setSibuk] = useState(false);
  const [pesan, setPesan] = useState<{ tone: "danger" | "success"; teks: string } | null>(null);
  const kunci = useRef<string | null>(null);

  const gunakan = useCallback(
    async (e: FormEvent) => {
      e.preventDefault();
      if (!data) return;
      const n = nominalDari(nominal);
      if (n === null) return setPesan({ tone: "danger", teks: "Isi nominal deposit dalam rupiah bulat." });
      kunci.current ??= buatIdempotencyKey();
      setSibuk(true);
      setPesan(null);
      try {
        await api.post(
          `/orders/${id}/deposit-allocations`,
          { amount: n, expected_journal_version: data.balance.journal_version, expected_order_version: data.versi },
          { idempotencyKey: kunci.current },
        );
        kunci.current = null;
        setNominal("");
        setPesan({ tone: "success", teks: `Deposit ${rupiah.format(n)} dipakai untuk pesanan ini.` });
        muatUlang();
      } catch (err) {
        setPesan({ tone: "danger", teks: pesanDeposit(err) });
        if (err instanceof ApiClientError && err.status === 409) {
          kunci.current = null;
          muatUlang();
        }
      } finally {
        setSibuk(false);
      }
    },
    [data, nominal, id, muatUlang],
  );

  if (memuat && !data) return <SkeletonList rows={2} />;
  if (galat || !data) return galat ? <Notice tone="danger">{galat}</Notice> : null;
  const st = statusBayar(data);
  const bolehPakai = data.status === "submitted" && data.remaining_due > 0 && data.balance.available_balance > 0;
  const maks = Math.min(data.remaining_due, data.balance.available_balance);

  return (
    <Card>
      <CardHead title="Pembayaran" tools={<Badge tone={st.tone}>{st.label}</Badge>} />
      <dl className={styles.metaGrid}>
        <div>
          <dt>Total pesanan</dt>
          <dd className="tabular">{rupiah.format(data.current_total)}</dd>
        </div>
        {data.is_lunas && data.settlement ? (
          <>
            <div>
              <dt>Dibayar dari deposit</dt>
              <dd className="tabular">{rupiah.format(data.settlement.deposit_amount)}</dd>
            </div>
            <div>
              <dt>Dibayar dengan transfer</dt>
              <dd className="tabular">{rupiah.format(data.settlement.transfer_amount)}</dd>
            </div>
          </>
        ) : (
          <>
            <div>
              <dt>Deposit terpakai</dt>
              <dd className="tabular">{rupiah.format(data.committed_deposit)}</dd>
            </div>
            <div>
              <dt>Sisa tagihan</dt>
              <dd className="tabular" data-uji="sisa-tagihan">
                {rupiah.format(data.remaining_due)}
              </dd>
            </div>
          </>
        )}
      </dl>
      {!data.is_lunas ? (
        <p className="muted small">
          Bukti transfer yang belum diverifikasi admin tidak dihitung sebagai pembayaran.
        </p>
      ) : null}
      {bolehPakai ? (
        <form onSubmit={(e) => void gunakan(e)} className={styles.stack}>
          <Field label={`Gunakan deposit (saldo ${rupiah.format(data.balance.available_balance)})`} htmlFor={`deposit-${id}`}>
            <Input
              id={`deposit-${id}`}
              inputMode="numeric"
              value={nominal}
              placeholder={String(maks)}
              onChange={(e) => setNominal(e.target.value)}
              aria-label="Nominal deposit"
            />
          </Field>
          <Button type="submit" variant="secondary" icon="wallet" disabled={sibuk}>
            {sibuk ? "Memproses…" : "Gunakan deposit"}
          </Button>
        </form>
      ) : null}
      {pesan ? <Notice tone={pesan.tone}>{pesan.teks}</Notice> : null}
    </Card>
  );
}
