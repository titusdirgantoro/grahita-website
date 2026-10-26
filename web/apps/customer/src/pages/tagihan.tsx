import { useState, type FormEvent } from "react";
import { Link } from "@tanstack/react-router";
import { api, ApiClientError } from "@grahita/api-client";
import { Badge, Button, Card, CardHead, EmptyState, Field, Input, Notice, PageHead, SkeletonList, Table } from "@grahita/ui";
import { useApi } from "../useApi";

const uang = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });
interface Saldo { period_month: string; due_date: string; total: number; paid: number; remaining: number; status: string }
interface Ringkasan { total_receivable: number; overdue: number; hold_active: boolean; pending_proof: number; items: Saldo[] }
interface Surat { id: string; period_month: string; version: number; total: number; paid: number; remaining: number; status: string; ready: boolean }
interface Pelunasan { id: string; declared_amount: number; verified_amount?: number; status: string; submitted_at: string }
interface Rekening { id: string; bank: string; nama_pemilik: string; nomor: string }
const label: Record<string,string> = { berjalan: "Berjalan", menunggu_bayar: "Menunggu bayar", lewat_jatuh_tempo: "Lewat jatuh tempo", lunas: "Lunas", versi_lama: "Versi lama", pending: "Menunggu verifikasi", verified: "Terverifikasi", rejected: "Ditolak" };
function pesan(err: unknown) { return err instanceof ApiClientError ? err.message : "Terjadi kesalahan. Coba lagi."; }
function tanggal(s: string) { return new Date(`${s}T00:00:00`).toLocaleDateString("id-ID", { day: "numeric", month: "long", year: "numeric" }); }

export function TagihanPage() {
  const ringkasan = useApi<Ringkasan>("/tempo/overview");
  const surat = useApi<Surat[]>("/tempo/statements");
  const pelunasan = useApi<Pelunasan[]>("/tempo/repayments");
  const rekening = useApi<Rekening[]>("/bank-accounts");
  const [nominal,setNominal]=useState("");
  const [rekeningID,setRekeningID]=useState("");
  const [berkas,setBerkas]=useState<File|null>(null);
  const [sibuk,setSibuk]=useState(false);
  const [galat,setGalat]=useState<string|null>(null);
  const [sukses,setSukses]=useState<string|null>(null);
  async function buka(path: string) {
    try { const result=await api.get<{url:string}>(path); window.open(result.url,"_blank","noopener,noreferrer"); }
    catch (err) { setGalat(pesan(err)); }
  }
  async function kirim(e: FormEvent) {
    e.preventDefault();
    if (!Number.isSafeInteger(Number(nominal)) || Number(nominal)<=0 || !rekeningID || !berkas) {
      setGalat("Isi nominal Rupiah, pilih rekening tujuan, dan lampirkan bukti."); return;
    }
    const body=new FormData(); body.append("amount",nominal); body.append("bank_account_id",rekeningID); body.append("file",berkas);
    setSibuk(true);setGalat(null);setSukses(null);
    try {
      await api.postForm("/tempo/repayments",body);
      setNominal("");setBerkas(null);setSukses("Bukti pelunasan diterima dan menunggu verifikasi admin.");
      ringkasan.muatUlang();pelunasan.muatUlang();
    } catch(err) { setGalat(pesan(err)); } finally { setSibuk(false); }
  }
  return <>
    <PageHead title="Tagihan" sub="Piutang Tempo, surat tagihan, dan bukti pelunasan" />
    {galat ? <Notice tone="danger">{galat}</Notice> : null}
    {sukses ? <Notice tone="success">{sukses}</Notice> : null}
    {ringkasan.memuat ? <Card><SkeletonList rows={2}/></Card> : ringkasan.galat ? <Notice tone="danger">{ringkasan.galat}</Notice> : ringkasan.data ? <>
      {ringkasan.data.hold_active ? <Notice tone="warning">Pengiriman pesanan ditahan karena tagihan lewat jatuh tempo sebesar {uang.format(ringkasan.data.overdue)}. Unggah bukti pelunasan yang menutup seluruh tunggakan agar pemeriksaan dapat dimulai.</Notice> : null}
      <Card><CardHead title="Ringkasan"/><p>Piutang: <b>{uang.format(ringkasan.data.total_receivable)}</b> · Lewat jatuh tempo: <b>{uang.format(ringkasan.data.overdue)}</b></p>
      {ringkasan.data.pending_proof>0 ? <p>Bukti menunggu verifikasi: {uang.format(ringkasan.data.pending_proof)}</p> : null}</Card>
      <Card><CardHead title="Tagihan per periode"/>{ringkasan.data.items.length===0 ? <EmptyState icon="file-text">Belum ada tagihan Tempo.</EmptyState> : <Table stack="narrow" caption="Daftar tagihan Tempo"><thead><tr><th>Periode</th><th>Jatuh tempo</th><th>Total</th><th>Terbayar</th><th>Sisa</th><th>Status</th></tr></thead><tbody>{ringkasan.data.items.map((row,i)=><tr key={`${row.period_month}-${row.due_date}-${i}`}><td>{row.period_month}</td><td>{tanggal(row.due_date)}</td><td>{uang.format(row.total)}</td><td>{uang.format(row.paid)}</td><td>{uang.format(row.remaining)}</td><td><Badge tone={row.status==="lewat_jatuh_tempo"?"danger":row.status==="lunas"?"success":"warning"}>{label[row.status]??row.status}</Badge></td></tr>)}</tbody></Table>}</Card>
    </> : null}
    <Card><CardHead title="Surat Tagihan"/><p>Butuh invoice per pesanan? <Link to="/invoice">Buka Riwayat invoice</Link>.</p>{surat.memuat ? <SkeletonList rows={2}/> : surat.galat ? <Notice tone="danger">{surat.galat}</Notice> : !surat.data?.length ? <p>Surat Tagihan terbit tanggal 1 bulan berikutnya.</p> : <Table stack="narrow" caption="Surat tagihan"><thead><tr><th>Periode</th><th>Versi</th><th>Total</th><th>Status</th><th>Dokumen</th></tr></thead><tbody>{surat.data.map(s=><tr key={s.id}><td>{s.period_month.slice(0,7)}</td><td>{s.version}</td><td>{uang.format(s.total)}</td><td>{label[s.status]??s.status}</td><td><Button variant="secondary" disabled={!s.ready} onClick={()=>void buka(`/tempo/statements/${s.id}/download`)}>Unduh PDF</Button></td></tr>)}</tbody></Table>}</Card>
    <Card><CardHead title="Unggah bukti pelunasan"/><p>Nominal yang diverifikasi dialokasikan ke invoice terlama. Kelebihan pembayaran menjadi saldo Tempo setelah persetujuan Superadmin.</p>
      <form onSubmit={(e)=>void kirim(e)}>
        <Field label="Nominal yang ditransfer (Rp)" htmlFor="tempo-amount"><Input id="tempo-amount" type="number" min="1" step="1" value={nominal} onChange={e=>setNominal(e.target.value)}/></Field>
        <Field label="Rekening tujuan" htmlFor="tempo-bank"><select id="tempo-bank" value={rekeningID} onChange={e=>setRekeningID(e.target.value)} required><option value="">Pilih rekening</option>{(rekening.data??[]).map(r=><option key={r.id} value={r.id}>{r.bank} · {r.nomor} · {r.nama_pemilik}</option>)}</select></Field>
        {rekening.galat ? <Notice tone="danger">{rekening.galat}</Notice> : null}
        <Field label="Bukti transfer (JPG, PNG, PDF; maks. 10 MB)" htmlFor="tempo-file"><Input id="tempo-file" type="file" accept=".jpg,.jpeg,.png,.pdf,image/jpeg,image/png,application/pdf" onChange={e=>setBerkas(e.target.files?.[0]??null)}/></Field>
        <Button variant="primary" type="submit" disabled={sibuk || rekening.memuat || !rekening.data?.length}>{sibuk?"Mengunggah…":"Kirim bukti pelunasan"}</Button>
      </form>
    </Card>
    <Card><CardHead title="Riwayat pelunasan"/>{pelunasan.memuat ? <SkeletonList rows={2}/> : pelunasan.galat ? <Notice tone="danger">{pelunasan.galat}</Notice> : !pelunasan.data?.length ? <p>Belum ada bukti pelunasan.</p> : <Table stack="narrow" caption="Riwayat bukti pelunasan"><thead><tr><th>Tanggal</th><th>Nominal</th><th>Status</th><th>Bukti</th></tr></thead><tbody>{pelunasan.data.map(p=><tr key={p.id}><td>{new Date(p.submitted_at).toLocaleString("id-ID",{timeZone:"Asia/Jakarta"})}</td><td>{uang.format(p.verified_amount??p.declared_amount)}</td><td>{label[p.status]??p.status}</td><td><Button variant="secondary" onClick={()=>void buka(`/tempo/repayments/${p.id}/proof`)}>Lihat</Button></td></tr>)}</tbody></Table>}</Card>
  </>;
}
