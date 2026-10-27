import { useState, type FormEvent, type ReactElement } from "react";
import { useNavigate } from "@tanstack/react-router";

import { ApiClientError, api } from "@grahita/api-client";
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
  SkeletonList,
  Table,
} from "@grahita/ui";

import { useApi } from "../useApi";
import styles from "./invoice.module.css";

/**
 * pInvoice — /invoice (PRD §3.8, §3.10 CP-ORD-10; kartu F1-19).
 *
 * Daftar riwayat invoice cabang SESI dari GET /api/v1/invoices: nomor,
 * tanggal, total, versi (versi lama tetap tampil setelah revisi), dan tombol
 * unduh PDF.
 *
 * Unduhan TIDAK memakai URL berkas langsung: klik tombol meminta presigned URL
 * berumur pendek dari `GET /api/v1/documents/{id}/download` lalu membukanya —
 * kunci object storage tidak pernah sampai ke klien (F1-17, SystemDesign.md §8).
 * Tombol unduh hanya aktif bila server menandai `dapat_diunduh`; invoice yang
 * belum selesai (masih Submitted) atau yang dokumennya belum dirender tampil
 * tanpa PDF, dan API memang menjawab 404 untuk dokumen yang tidak ada.
 *
 * Filter periode disimpan di URL search param (`?dari=&sampai=`) supaya bisa
 * di-bookmark dan di-share (SystemDesign.md §7.2).
 */

export interface Invoice {
  order_id: string;
  nomor: string;
  tanggal?: string;
  status: "submitted" | "approved_unpaid" | "done";
  versi: number;
  is_cnc: boolean;
  is_tempo?: boolean;
  total: number;
  jumlah_item: number;
  dokumen_id?: string;
  versi_dokumen: number | null;
  ukuran_byte: number;
  terbaru: boolean;
  dapat_diunduh: boolean;
}

/** TautanDokumen adalah presigned URL berumur pendek dari domain dokumen. */
export interface TautanDokumen {
  url: string;
  expires_in: number;
  nama_file: string;
  mime_type: string;
}

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

/** tanggalWIB menampilkan tanggal bisnis (YYYY-MM-DD) tanpa menggeser harinya. */
function tanggalWIB(tanggal?: string): string {
  if (!tanggal) return "—";
  const t = new Date(`${tanggal}T00:00:00`);
  if (Number.isNaN(t.getTime())) return tanggal;
  return t.toLocaleDateString("id-ID", { day: "2-digit", month: "short", year: "numeric" });
}

function galatPesan(err: unknown): string {
  if (err instanceof ApiClientError) return err.message;
  return "Tidak bisa menyelesaikan permintaan. Coba lagi.";
}

/** sukuQuery menyusun string query dari filter periode yang terisi. */
export function sukuQuery(dari?: string, sampai?: string): string {
  const p = new URLSearchParams();
  if (dari) p.set("dari", dari);
  if (sampai) p.set("sampai", sampai);
  const s = p.toString();
  return s ? `?${s}` : "";
}

/** labelVersi menjelaskan satu baris dokumen invoice (asli/revisi/versi lama). */
function labelVersi(inv: Invoice): ReactElement {
  // Tombol Dokumen di baris yang sama sudah bertuliskan "Belum tersedia",
  // jadi kolom versi cukup tanda kosong seperti nilai kosong lain di portal.
  if (inv.versi_dokumen === null) {
    return <span className="muted">—</span>;
  }
  return (
    <>
      {inv.versi_dokumen === 0 ? (
        <span className="muted small">Asli</span>
      ) : (
        <Badge tone="rev">Revisi {inv.versi_dokumen}</Badge>
      )}
      {!inv.terbaru ? <span className="muted small"> versi lama</span> : null}
    </>
  );
}

export function InvoicePage({ dari, sampai }: { dari?: string; sampai?: string }) {
  const navigate = useNavigate();
  const [dDari, setDDari] = useState(dari ?? "");
  const [dSampai, setDSampai] = useState(sampai ?? "");
  const [galatUnduh, setGalatUnduh] = useState<string | null>(null);
  const [mengunduh, setMengunduh] = useState<string | null>(null);

  const query = sukuQuery(dari, sampai);
  const { data, memuat, galat } = useApi<Invoice[]>(`/invoices${query}`);

  function terapkan(e: FormEvent) {
    e.preventDefault();
    void navigate({
      to: "/invoice",
      search: { dari: dDari || undefined, sampai: dSampai || undefined },
    });
  }

  function reset() {
    setDDari("");
    setDSampai("");
    void navigate({ to: "/invoice", search: { dari: undefined, sampai: undefined } });
  }

  async function unduh(inv: Invoice) {
    if (!inv.dokumen_id) return;
    setGalatUnduh(null);
    setMengunduh(inv.dokumen_id);
    try {
      const tautan = await api.get<TautanDokumen>(`/documents/${inv.dokumen_id}/download`);
      window.open(tautan.url, "_blank", "noopener,noreferrer");
    } catch (err) {
      setGalatUnduh(galatPesan(err));
    } finally {
      setMengunduh(null);
    }
  }

  const jumlahDokumen = (data ?? []).filter((i) => i.dapat_diunduh).length;

  return (
    <>
      <PageHead
        title="Riwayat invoice"
        sub="Semua versi invoice cabang Anda. Invoice bisa diunduh setelah pesanan selesai."
      />

      <div className={styles.stack}>
        <Card>
          <CardHead title="Filter periode" />
          <form className={styles.filter} onSubmit={terapkan}>
            <Field label="Dari tanggal" htmlFor="inv-dari">
              <Input
                id="inv-dari"
                type="date"
                value={dDari}
                onChange={(e) => setDDari(e.target.value)}
              />
            </Field>
            <Field label="Sampai tanggal" htmlFor="inv-sampai">
              <Input
                id="inv-sampai"
                type="date"
                value={dSampai}
                onChange={(e) => setDSampai(e.target.value)}
              />
            </Field>
            <div className={styles.aksiFilter}>
              <Button type="submit" variant="primary" icon="funnel">
                Terapkan
              </Button>
              <Button type="button" variant="ghost" onClick={reset}>
                Reset
              </Button>
            </div>
          </form>
        </Card>

        {galatUnduh ? <Notice tone="danger">{galatUnduh}</Notice> : null}

        <Card>
          <CardHead
            title="Daftar invoice"
            tools={
              data ? (
                <span className="muted small">
                  {data.length} baris · {jumlahDokumen} siap diunduh
                </span>
              ) : undefined
            }
          />
          {memuat ? (
            <SkeletonList rows={3} />
          ) : galat ? (
            <Notice tone="danger">{galat}</Notice>
          ) : !data || data.length === 0 ? (
            <EmptyState icon="file-text">
              Belum ada invoice pada periode ini. Invoice muncul setelah pembayaran pesanan
              diverifikasi admin Pusat.
            </EmptyState>
          ) : (
            <>
              <Table mobileCards caption="Riwayat invoice cabang">
                <thead>
                  <tr>
                    <th scope="col">Nomor invoice</th>
                    <th scope="col">Tanggal</th>
                    <th scope="col" className="num">
                      Total
                    </th>
                    <th scope="col">Versi</th>
                    <th scope="col">Status</th>
                    <th scope="col" className="num">
                      Dokumen
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {data.map((inv) => (
                    <tr key={`${inv.order_id}-${inv.versi_dokumen ?? "belum"}`}>
                      <td>
                        <b className="tabular">{inv.nomor}</b>
                        {inv.is_cnc ? <Badge tone="cnc">C&amp;C</Badge> : null}
                        {inv.is_tempo ? <Badge tone="info">Tempo</Badge> : null}
                      </td>
                      <td className="tabular muted">{tanggalWIB(inv.tanggal)}</td>
                      <td className="num">
                        <b>{rupiah.format(inv.total)}</b>
                      </td>
                      <td>{labelVersi(inv)}</td>
                      <td>
                        <Badge tone={inv.status === "done" ? "success" : "warning"}>
                          {inv.status === "done" ? "Selesai" : inv.status === "approved_unpaid" ? "Disetujui — belum lunas" : "Menunggu verifikasi"}
                        </Badge>
                      </td>
                      <td className="num">
                        {inv.dapat_diunduh ? (
                          <Button
                            variant="secondary"
                            icon="download-simple"
                            disabled={mengunduh === inv.dokumen_id}
                            onClick={() => void unduh(inv)}
                          >
                            Unduh
                          </Button>
                        ) : (
                          <Button
                            variant="secondary"
                            disabled
                            title="Invoice belum tersedia. Tunggu persetujuan pesanan dan penerbitan dokumen."
                          >
                            Belum tersedia
                          </Button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </Table>
              <p className="muted small">
                Tombol Unduh meminta tautan berumur pendek dari server; invoice yang belum selesai
                belum punya berkas PDF sama sekali.
              </p>
            </>
          )}
        </Card>
      </div>
    </>
  );
}
