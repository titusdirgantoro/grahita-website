import { useCallback, useMemo, useRef, useState, type FormEvent } from "react";

import { ApiClientError, api, buatIdempotencyKey } from "@grahita/api-client";
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
  SkeletonList,
  Table,
  Textarea,
  useToast,
  type BadgeTone,
} from "@grahita/ui";

import { useApi } from "../useApi";
import { useSesi } from "../session";
import styles from "./admin-pages.module.css";

/*
 * Deposit cabang di Admin Portal (C4; D3 §7.2, C0 §5). Manager meninjau,
 * menolak, dan mengalokasikan saldo yang SUDAH ada; hanya Superadmin yang
 * menciptakan kredit (verifikasi top-up dan kredit lebih bayar). Tombol hanya
 * membantu — API tetap otoritas akhir. Setiap mutasi uang membawa
 * Idempotency-Key per niat dan versi journal yang dilihat layar.
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

interface IkhtisarCabang extends RingkasanDeposit {
  branch_nama: string;
}

interface TopupAdmin {
  id: string;
  branch_id: string;
  branch_nama: string;
  bank_account_id: string;
  bank: string;
  display_nomor: string;
  status: string;
  declared_amount: number;
  verified_amount?: number;
  ocr_nominal?: number;
  rejection_reason?: string;
  proof_nama_file: string;
  created_at: string;
}

export interface AlokasiDeposit {
  id: string;
  order_version: number;
  amount: number;
  reversed_amount: number;
  active_amount: number;
  created_at: string;
}

/** PembayaranPesanan adalah respons GET /admin/orders/{id}/payment. */
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
  pending_review: "Menunggu",
  needs_review: "Perlu review",
  verified: "Terverifikasi",
  rejected: "Ditolak",
  reversed: "Dibatalkan",
};
const TONE_TOPUP: Record<string, BadgeTone> = {
  pending_review: "warning",
  needs_review: "danger",
  verified: "success",
  rejected: "neutral",
  reversed: "neutral",
};

/** pesanDepositAdmin menerjemahkan galat API deposit/settlement (D3 §6.3). */
export function pesanDepositAdmin(err: unknown): string {
  if (!(err instanceof ApiClientError)) return "Terjadi kesalahan. Coba lagi.";
  switch (err.code) {
    case "DEPOSIT_VERSION_CONFLICT":
    case "ORDER_VERSION_CONFLICT":
      return "Saldo atau pesanan baru saja berubah. Data dimuat ulang — periksa lalu kirim lagi.";
    case "OVERPAY_DECISION_REQUIRED":
      return "Transfer yang diterapkan melebihi kebutuhan. Putuskan selisihnya sebagai kredit deposit (Superadmin).";
    case "PAYMENT_BREAKDOWN_MISMATCH":
      return `Rincian tidak tepat total pesanan. ${err.message}`;
    case "REVISION_REVERSAL_PLAN_REQUIRED":
      return "Total revisi lebih kecil dari deposit terpakai. Isi rencana pembatalan deposit yang cukup.";
    case "REVISION_REVERSAL_PLAN_INVALID":
      return "Rencana pembatalan deposit tidak valid (alokasi, nominal, atau alasan).";
    case "FORBIDDEN":
      return `Tindakan ini tidak diizinkan untuk peran Anda. ${err.message}`;
    default:
      return err.message;
  }
}

/** angkaDari membaca rupiah bulat non-negatif; null bila tidak sah. */
export function angkaDari(teks: string): number | null {
  const bersih = teks.replace(/[.\s]/g, "");
  if (!/^\d+$/.test(bersih)) return null;
  const n = Number(bersih);
  return Number.isSafeInteger(n) ? n : null;
}

/** SplitBukti adalah ketikan admin untuk satu bukti pada approve campuran. */
export interface SplitBukti {
  approved: string;
  applied: string;
}

export interface MasukanSettlement {
  versi: number;
  pembayaran: PembayaranPesanan;
  bukti: { id: string }[];
  tujuan: Record<string, string>;
  split: Record<string, SplitBukti>;
  catatan?: string;
  totalTerbaca?: number | null;
}

export interface RingkasSettlement {
  deposit: number;
  transfer: number;
  kredit: number;
  selisih: number;
}

/**
 * hitungSettlement menghitung pratinjau dari ketikan admin. Server tetap
 * menghitung ulang di bawah kunci; angka ini hanya untuk umpan balik layar.
 */
export function hitungSettlement(m: Pick<MasukanSettlement, "pembayaran" | "bukti" | "split">): RingkasSettlement {
  const deposit = m.pembayaran.committed_deposit;
  let transfer = 0;
  let kredit = 0;
  for (const b of m.bukti) {
    const s = m.split[b.id];
    const approved = angkaDari(s?.approved ?? "") ?? 0;
    const applied = angkaDari(s?.applied ?? "") ?? 0;
    transfer += applied;
    kredit += Math.max(approved - applied, 0);
  }
  return { deposit, transfer, kredit, selisih: deposit + transfer - m.pembayaran.current_total };
}

/**
 * susunSettlement menyusun body POST /admin/orders/{id}/verify jalur campuran
 * (D3 §6.2): seluruh alokasi aktif lintas versi dan satu split per bukti.
 * Mengembalikan pesan galat bila ketikan tidak sah.
 */
export function susunSettlement(m: MasukanSettlement): { body: Record<string, unknown> } | { galat: string } {
  const proof_splits = [];
  for (const b of m.bukti) {
    const s = m.split[b.id];
    const approved = angkaDari(s?.approved ?? "");
    const applied = angkaDari(s?.applied ?? "");
    if (approved === null || applied === null) return { galat: "Isi nominal disetujui dan nominal untuk pesanan pada setiap bukti." };
    if (applied > approved) return { galat: "Nominal untuk pesanan tidak boleh melebihi nominal disetujui." };
    const tujuan = m.tujuan[b.id] ?? "";
    if (!tujuan) return { galat: "Pilih rekening tujuan efektif untuk setiap bukti." };
    proof_splits.push({
      payment_proof_id: b.id,
      bank_account_id: tujuan,
      approved_nominal: approved,
      applied_to_order: applied,
      credited_to_deposit_total: approved - applied,
    });
  }
  return {
    body: {
      hasil: "cocok",
      expected_order_version: m.versi,
      deposit_allocation_ids: m.pembayaran.allocations.filter((a) => a.active_amount > 0).map((a) => a.id),
      proof_splits,
      ...(m.catatan ? { catatan: m.catatan } : {}),
      ...(m.totalTerbaca !== null && m.totalTerbaca !== undefined ? { total_terbaca: m.totalTerbaca } : {}),
    },
  };
}

/** RincianSettlement adalah isian split per bukti di modal verifikasi. */
export function RincianSettlement({
  pembayaran,
  bukti,
  split,
  onSplit,
  superadmin,
}: {
  pembayaran: PembayaranPesanan;
  bukti: { id: string; nama_file: string; ocr_nominal?: number }[];
  split: Record<string, SplitBukti>;
  onSplit: (id: string, patch: Partial<SplitBukti>) => void;
  superadmin: boolean;
}) {
  const r = hitungSettlement({ pembayaran, bukti, split });
  return (
    <div className={styles.stack} data-uji="rincian-settlement">
      <div className={styles.row} style={{ justifyContent: "space-between" }}>
        <span className="muted small">Deposit terpakai (semua versi)</span>
        <span className="tabular">{rupiah.format(r.deposit)}</span>
      </div>
      {bukti.map((b) => (
        <div key={b.id} className={styles.row} style={{ gap: 8, alignItems: "flex-end" }}>
          <Field label={`Disetujui · ${b.nama_file}`} htmlFor={`split-ok-${b.id}`}>
            <Input
              id={`split-ok-${b.id}`}
              inputMode="numeric"
              value={split[b.id]?.approved ?? ""}
              placeholder={b.ocr_nominal ? String(b.ocr_nominal) : "0"}
              onChange={(e) => onSplit(b.id, { approved: e.target.value })}
            />
          </Field>
          <Field label="Untuk pesanan" htmlFor={`split-order-${b.id}`}>
            <Input
              id={`split-order-${b.id}`}
              inputMode="numeric"
              value={split[b.id]?.applied ?? ""}
              onChange={(e) => onSplit(b.id, { applied: e.target.value })}
            />
          </Field>
        </div>
      ))}
      <div className={styles.row} style={{ justifyContent: "space-between" }}>
        <span className="muted small">Deposit + transfer untuk pesanan</span>
        <strong className="tabular" data-uji="jumlah-settlement">
          {rupiah.format(r.deposit + r.transfer)}
        </strong>
      </div>
      {r.selisih !== 0 ? (
        <Notice tone="danger">
          {r.selisih < 0
            ? `Kurang ${rupiah.format(-r.selisih)} dari total pesanan.`
            : `Lebih ${rupiah.format(r.selisih)} dari total pesanan — kurangi nominal untuk pesanan.`}
        </Notice>
      ) : null}
      {r.kredit > 0 ? (
        <Notice tone={superadmin ? "warning" : "danger"}>
          {superadmin
            ? `Selisih ${rupiah.format(r.kredit)} akan dikreditkan ke deposit cabang.`
            : `Selisih ${rupiah.format(r.kredit)} perlu dikreditkan ke deposit — hanya Superadmin yang boleh memutuskannya.`}
        </Notice>
      ) : null}
    </div>
  );
}

/** PanelPembayaranAdmin menampilkan rincian deposit pesanan dan aksi koreksi. */
export function PanelPembayaranAdmin({
  orderId,
  pembayaran,
  onBerubah,
}: {
  orderId: string;
  pembayaran: PembayaranPesanan;
  onBerubah: () => void;
}) {
  const { tampilkan } = useToast();
  const [batal, setBatal] = useState<AlokasiDeposit | null>(null);
  const [nominal, setNominal] = useState("");
  const [alasan, setAlasan] = useState("");
  const [sibuk, setSibuk] = useState(false);
  const [galat, setGalat] = useState<string | null>(null);
  const kunci = useRef<string | null>(null);
  const terbuka = pembayaran.status === "submitted";

  const kirimBatal = useCallback(async () => {
    if (!batal) return;
    const n = angkaDari(nominal);
    if (n === null || n <= 0) return setGalat("Isi nominal yang dibatalkan.");
    if (alasan.trim() === "") return setGalat("Alasan wajib diisi.");
    kunci.current ??= buatIdempotencyKey();
    setSibuk(true);
    setGalat(null);
    try {
      await api.post(
        `/admin/orders/${orderId}/deposit-allocations/${batal.id}/reverse`,
        { amount: n, expected_journal_version: pembayaran.balance.journal_version, reason: alasan.trim() },
        { idempotencyKey: kunci.current },
      );
      kunci.current = null;
      setBatal(null);
      tampilkan(`Pemakaian deposit ${rupiah.format(n)} dibatalkan dan kembali ke saldo cabang.`);
      onBerubah();
    } catch (err) {
      setGalat(pesanDepositAdmin(err));
      if (err instanceof ApiClientError && err.status === 409) {
        kunci.current = null;
        onBerubah();
      }
    } finally {
      setSibuk(false);
    }
  }, [batal, nominal, alasan, orderId, pembayaran.balance.journal_version, tampilkan, onBerubah]);

  return (
    <Card>
      <CardHead title="Pembayaran" />
      {pembayaran.revision_payment_state ? (
        <Notice tone="warning">
          {pembayaran.revision_payment_state === "revision_underpayment"
            ? "Revisi menaikkan total: pesanan belum lunas sampai kekurangan diverifikasi."
            : "Revisi menurunkan total: selisih transfer menunggu keputusan Superadmin."}
        </Notice>
      ) : null}
      <div className={styles.metaGrid}>
        <div>
          <div className="muted small">Total kini</div>
          <div className="tabular">{rupiah.format(pembayaran.current_total)}</div>
        </div>
        {pembayaran.is_lunas && pembayaran.settlement ? (
          <>
            <div>
              <div className="muted small">Deposit</div>
              <div className="tabular">{rupiah.format(pembayaran.settlement.deposit_amount)}</div>
            </div>
            <div>
              <div className="muted small">Transfer</div>
              <div className="tabular">{rupiah.format(pembayaran.settlement.transfer_amount)}</div>
            </div>
          </>
        ) : (
          <>
            <div>
              <div className="muted small">Deposit terpakai</div>
              <div className="tabular">{rupiah.format(pembayaran.committed_deposit)}</div>
            </div>
            <div>
              <div className="muted small">Sisa tagihan</div>
              <div className="tabular">{rupiah.format(pembayaran.remaining_due)}</div>
            </div>
          </>
        )}
        <div>
          <div className="muted small">Saldo deposit cabang</div>
          <div className="tabular">{rupiah.format(pembayaran.balance.available_balance)}</div>
        </div>
      </div>
      {pembayaran.allocations.length > 0 ? (
        <Table stack="narrow" caption="Alokasi deposit pesanan">
          <thead>
            <tr>
              <th scope="col">Versi</th>
              <th scope="col" className="num">Aktif</th>
              <th scope="col" className="num">Dibatalkan</th>
              <th scope="col" />
            </tr>
          </thead>
          <tbody>
            {pembayaran.allocations.map((a) => (
              <tr key={a.id}>
                <td>{`v${a.order_version}`}</td>
                <td className="num tabular">{rupiah.format(a.active_amount)}</td>
                <td className="num tabular">{rupiah.format(a.reversed_amount)}</td>
                <td>
                  {terbuka && a.active_amount > 0 ? (
                    <Button
                      variant="secondary"
                      onClick={() => {
                        setBatal(a);
                        setNominal(String(a.active_amount));
                        setAlasan("");
                        setGalat(null);
                        kunci.current = null;
                      }}
                    >
                      Batalkan
                    </Button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      ) : null}
      <Modal
        open={batal !== null}
        title="Batalkan pemakaian deposit"
        onClose={() => {
          if (!sibuk) setBatal(null);
        }}
        footer={
          <>
            <Button variant="secondary" onClick={() => setBatal(null)} disabled={sibuk}>
              Tutup
            </Button>
            <Button variant="primary" onClick={() => void kirimBatal()} disabled={sibuk}>
              {sibuk ? "Memproses…" : "Batalkan pemakaian"}
            </Button>
          </>
        }
      >
        <Field label="Nominal dibatalkan" htmlFor="batal-nominal">
          <Input id="batal-nominal" inputMode="numeric" value={nominal} onChange={(e) => setNominal(e.target.value)} />
        </Field>
        <Field label="Alasan" htmlFor="batal-alasan">
          <Textarea id="batal-alasan" value={alasan} onChange={(e) => setAlasan(e.target.value)} />
        </Field>
        {galat ? <Notice tone="danger">{galat}</Notice> : null}
      </Modal>
    </Card>
  );
}

/** RencanaReversal adalah ketikan rencana pembatalan deposit pada revisi turun. */
export type RencanaReversal = Record<string, { nominal: string; alasan: string }>;

/** susunRencanaReversal mengubah ketikan menjadi `deposit_reversals` (C0 §5.1). */
export function susunRencanaReversal(r: RencanaReversal): { plan: Record<string, unknown>[] } | { galat: string } {
  const plan: Record<string, unknown>[] = [];
  for (const [allocation_id, v] of Object.entries(r)) {
    if (v.nominal.trim() === "") continue;
    const n = angkaDari(v.nominal);
    if (n === null || n <= 0) return { galat: "Nominal pembatalan deposit harus rupiah bulat positif." };
    if (v.alasan.trim() === "") return { galat: "Setiap pembatalan deposit wajib beralasan." };
    plan.push({ allocation_id, amount: n, reason: v.alasan.trim() });
  }
  return { plan };
}

/** IsianRencanaReversal menampilkan alokasi aktif untuk rencana pembatalan revisi. */
export function IsianRencanaReversal({
  alokasi,
  rencana,
  onRencana,
}: {
  alokasi: AlokasiDeposit[];
  rencana: RencanaReversal;
  onRencana: (id: string, patch: Partial<{ nominal: string; alasan: string }>) => void;
}) {
  const aktif = alokasi.filter((a) => a.active_amount > 0);
  if (aktif.length === 0) return null;
  return (
    <div className={styles.stack} data-uji="rencana-reversal">
      <p className="muted small">
        Pesanan memakai deposit. Bila total baru lebih kecil dari deposit terpakai, isi nominal yang dikembalikan ke
        saldo cabang beserta alasannya. Tidak ada pembatalan otomatis.
      </p>
      {aktif.map((a) => (
        <div key={a.id} className={styles.row} style={{ gap: 8, alignItems: "flex-end" }}>
          <Field label={`Kembalikan dari alokasi v${a.order_version} (aktif ${rupiah.format(a.active_amount)})`} htmlFor={`rev-${a.id}`}>
            <Input
              id={`rev-${a.id}`}
              inputMode="numeric"
              value={rencana[a.id]?.nominal ?? ""}
              onChange={(e) => onRencana(a.id, { nominal: e.target.value })}
            />
          </Field>
          <Field label="Alasan" htmlFor={`rev-alasan-${a.id}`}>
            <Input id={`rev-alasan-${a.id}`} value={rencana[a.id]?.alasan ?? ""} onChange={(e) => onRencana(a.id, { alasan: e.target.value })} />
          </Field>
        </div>
      ))}
    </div>
  );
}

type AksiTopup = "review" | "reject" | "verify";

/** DepositAdminPage (/deposit): saldo per cabang dan antrean top-up. */
export function DepositAdminPage() {
  const { peran } = useSesi();
  const superadmin = peran === "superadmin";
  const ikhtisar = useApi<IkhtisarCabang[]>("/admin/deposits");
  const antrean = useApi<TopupAdmin[]>("/admin/deposit-topups?limit=100");
  const { tampilkan } = useToast();
  const [pilih, setPilih] = useState<{ topup: TopupAdmin; aksi: AksiTopup } | null>(null);
  const [nominal, setNominal] = useState("");
  const [alasan, setAlasan] = useState("");
  const [sibuk, setSibuk] = useState(false);
  const [galat, setGalat] = useState<string | null>(null);
  const kunci = useRef<string | null>(null);

  const terbuka = useMemo(
    () => (antrean.data ?? []).filter((t) => t.status === "pending_review" || t.status === "needs_review"),
    [antrean.data],
  );

  const mulai = useCallback((topup: TopupAdmin, aksi: AksiTopup) => {
    setPilih({ topup, aksi });
    setNominal(String(topup.declared_amount));
    setAlasan("");
    setGalat(null);
    kunci.current = null;
  }, []);

  const lihatBukti = useCallback(
    async (t: TopupAdmin) => {
      try {
        const dl = await api.get<{ url: string }>(`/admin/deposit-topups/${t.id}/proof`);
        window.open(dl.url, "_blank", "noopener");
      } catch (err) {
        tampilkan(pesanDepositAdmin(err));
      }
    },
    [tampilkan],
  );

  const kirim = useCallback(
    async (e?: FormEvent) => {
      e?.preventDefault();
      if (!pilih) return;
      const body: Record<string, unknown> = { action: pilih.aksi };
      if (alasan.trim() !== "") body.reason = alasan.trim();
      if (pilih.aksi === "verify") {
        const n = angkaDari(nominal);
        if (n === null || n <= 0) return setGalat("Isi nominal kredit final.");
        if (n !== pilih.topup.declared_amount && alasan.trim() === "") {
          return setGalat("Nominal berbeda dari yang diajukan cabang; alasan wajib diisi.");
        }
        body.approved_amount = n;
        kunci.current ??= buatIdempotencyKey();
      } else if (alasan.trim() === "") {
        return setGalat("Alasan wajib diisi.");
      }
      setSibuk(true);
      setGalat(null);
      try {
        await api.post(
          `/admin/deposit-topups/${pilih.topup.id}/decision`,
          body,
          pilih.aksi === "verify" && kunci.current ? { idempotencyKey: kunci.current } : {},
        );
        kunci.current = null;
        setPilih(null);
        tampilkan(pilih.aksi === "verify" ? "Top-up diverifikasi dan saldo cabang bertambah." : "Keputusan top-up tersimpan.");
        antrean.muatUlang();
        ikhtisar.muatUlang();
      } catch (err) {
        setGalat(pesanDepositAdmin(err));
        if (err instanceof ApiClientError && err.status === 409) {
          antrean.muatUlang();
          ikhtisar.muatUlang();
        }
      } finally {
        setSibuk(false);
      }
    },
    [pilih, alasan, nominal, tampilkan, antrean, ikhtisar],
  );

  return (
    <>
      <PageHead title="Deposit cabang" sub="Saldo per cabang dan antrean top-up" />
      <div className={styles.stack}>
        <Card>
          <CardHead title="Antrean top-up" tools={<span className="muted small">{`${terbuka.length} menunggu`}</span>} />
          {antrean.memuat ? <SkeletonList rows={3} /> : null}
          {antrean.galat ? <Notice tone="danger">{antrean.galat}</Notice> : null}
          {!antrean.memuat && terbuka.length === 0 ? <EmptyState icon="check-circle">Tidak ada top-up yang menunggu.</EmptyState> : null}
          {terbuka.length > 0 ? (
            <Table stack="wide" caption="Antrean top-up deposit">
              <thead>
                <tr>
                  <th scope="col">Cabang</th>
                  <th scope="col">Rekening</th>
                  <th scope="col" className="num">Diajukan</th>
                  <th scope="col">Status</th>
                  <th scope="col" />
                </tr>
              </thead>
              <tbody>
                {terbuka.map((t) => (
                  <tr key={t.id}>
                    <td>
                      {t.branch_nama}
                      <div className="muted small">{waktu.format(new Date(t.created_at))}</div>
                    </td>
                    <td>{`${t.bank} ${t.display_nomor}`}</td>
                    <td className="num tabular">
                      {rupiah.format(t.declared_amount)}
                      {t.ocr_nominal ? <div className="muted small">{`OCR ${rupiah.format(t.ocr_nominal)}`}</div> : null}
                    </td>
                    <td>
                      <Badge tone={TONE_TOPUP[t.status] ?? "neutral"}>{LABEL_TOPUP[t.status] ?? t.status}</Badge>
                    </td>
                    <td>
                      <div className={styles.row} style={{ gap: 6 }}>
                        <Button variant="secondary" onClick={() => void lihatBukti(t)}>
                          Bukti
                        </Button>
                        {t.status === "pending_review" ? (
                          <Button variant="secondary" onClick={() => mulai(t, "review")}>
                            Review
                          </Button>
                        ) : null}
                        <Button variant="secondary" onClick={() => mulai(t, "reject")}>
                          Tolak
                        </Button>
                        {superadmin ? (
                          <Button variant="primary" onClick={() => mulai(t, "verify")} data-uji={`verifikasi-${t.id}`}>
                            Verifikasi
                          </Button>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          ) : null}
          {!superadmin ? (
            <p className="muted small">Kredit saldo hanya dapat diputuskan Superadmin.</p>
          ) : null}
        </Card>
        <Card>
          <CardHead title="Saldo per cabang" />
          {ikhtisar.memuat ? <SkeletonList rows={3} /> : null}
          {(ikhtisar.data ?? []).length > 0 ? (
            <Table stack="narrow" caption="Saldo deposit per cabang">
              <thead>
                <tr>
                  <th scope="col">Cabang</th>
                  <th scope="col" className="num">Saldo</th>
                  <th scope="col" className="num">Top-up pending</th>
                </tr>
              </thead>
              <tbody>
                {(ikhtisar.data ?? []).map((c) => (
                  <tr key={c.branch_id}>
                    <td>{c.branch_nama}</td>
                    <td className="num tabular">{rupiah.format(c.available_balance)}</td>
                    <td className="num tabular">
                      {c.pending_topup_count > 0
                        ? `${c.pending_topup_count} · ${rupiah.format(c.pending_topup_amount_declared)}`
                        : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          ) : null}
        </Card>
      </div>
      <Modal
        open={pilih !== null}
        title={pilih?.aksi === "verify" ? "Verifikasi top-up" : pilih?.aksi === "reject" ? "Tolak top-up" : "Tandai perlu review"}
        onClose={() => {
          if (!sibuk) setPilih(null);
        }}
        footer={
          <>
            <Button variant="secondary" onClick={() => setPilih(null)} disabled={sibuk}>
              Batal
            </Button>
            <Button variant="primary" onClick={() => void kirim()} disabled={sibuk} data-uji="kirim-keputusan-topup">
              {sibuk ? "Memproses…" : "Simpan keputusan"}
            </Button>
          </>
        }
      >
        {pilih ? (
          <p className="muted small">{`${pilih.topup.branch_nama} · diajukan ${rupiah.format(pilih.topup.declared_amount)}`}</p>
        ) : null}
        {pilih?.aksi === "verify" ? (
          <Field label="Nominal kredit final" htmlFor="topup-final">
            <Input id="topup-final" inputMode="numeric" value={nominal} onChange={(e) => setNominal(e.target.value)} />
          </Field>
        ) : null}
        <Field label={pilih?.aksi === "verify" ? "Alasan (wajib bila nominal berbeda)" : "Alasan"} htmlFor="topup-alasan">
          <Textarea id="topup-alasan" value={alasan} onChange={(e) => setAlasan(e.target.value)} />
        </Field>
        {galat ? <Notice tone="danger">{galat}</Notice> : null}
      </Modal>
    </>
  );
}
