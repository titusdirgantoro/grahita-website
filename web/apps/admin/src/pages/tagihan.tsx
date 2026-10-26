import { useState } from "react";
import { api, ApiClientError } from "@grahita/api-client";
import { Badge, Button, Card, CardHead, Field, Input, Notice, PageHead, SkeletonList, Table, useToast } from "@grahita/ui";
import { useApi } from "../useApi";
import { useSesi } from "../session";

const uang = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });
interface Saldo { branch_id:string; branch_name:string; period_month:string; due_date:string; total:number; paid:number; remaining:number; status:string; age_days:number }
interface Ringkasan { total_receivable:number; overdue:number; age_0_30:number; age_31_60:number; age_over_60:number; items:Saldo[] }
interface Pelunasan { id:string; branch_name:string; declared_amount:number; verified_amount?:number; bank:string; account_number:string; filename:string; status:string; submitted_at:string; rejection_reason?:string }
interface Surat { id:string; branch_id:string; period_month:string; version:number; total:number; paid:number; remaining:number; status:string; ready:boolean }
function pesan(err:unknown) { return err instanceof ApiClientError?err.message:"Terjadi kesalahan. Coba lagi."; }

export function TagihanAdminPage() {
  const { peran } = useSesi();
  const { tampilkan } = useToast();
  const ringkasan=useApi<Ringkasan>("/admin/tempo/overview");
  const pelunasan=useApi<Pelunasan[]>("/admin/tempo/repayments");
  const surat=useApi<Surat[]>("/admin/tempo/statements");
  const [nominal,setNominal]=useState<Record<string,string>>({});
  const [alasan,setAlasan]=useState<Record<string,string>>({});
  const [sibuk,setSibuk]=useState<string|null>(null);
  const [galat,setGalat]=useState<string|null>(null);
  async function buka(path:string) {
    try { const result=await api.get<{url:string}>(path); window.open(result.url,"_blank","noopener,noreferrer"); }
    catch(err) {setGalat(pesan(err));}
  }
  async function putuskan(row:Pelunasan,decision:"verify"|"reject") {
    const amount=Number(nominal[row.id]??row.declared_amount);
    if (decision==="verify" && (!Number.isSafeInteger(amount)||amount<=0)) {setGalat("Nominal terverifikasi harus Rupiah bulat positif.");return;}
    if (decision==="reject" && !alasan[row.id]?.trim()) {setGalat("Alasan penolakan wajib diisi.");return;}
    setSibuk(row.id);setGalat(null);
    try {
      await api.post(`/admin/tempo/repayments/${row.id}/decision`,{decision,amount:decision==="verify"?amount:0,reason:alasan[row.id]??""});
      tampilkan(decision==="verify"?"Pelunasan diverifikasi dan dialokasikan.":"Bukti pelunasan ditolak.");
      pelunasan.muatUlang();ringkasan.muatUlang();surat.muatUlang();
    } catch(err) {setGalat(pesan(err));} finally {setSibuk(null);}
  }
  return <>
    <PageHead title="Tagihan cabang" sub="Pantau piutang Tempo dan verifikasi bukti pelunasan" actions={peran==="superadmin"?<Button variant="secondary" onClick={()=>{window.location.href="/api/v1/admin/tempo/export";}}>Ekspor laporan piutang</Button>:undefined} />
    {galat?<Notice tone="danger">{galat}</Notice>:null}
    {ringkasan.memuat?<Card><SkeletonList rows={2}/></Card>:ringkasan.galat?<Notice tone="danger">{ringkasan.galat}</Notice>:ringkasan.data?<>
      <Card><CardHead title="Ringkasan piutang"/><p>Total piutang <b>{uang.format(ringkasan.data.total_receivable)}</b> · Lewat jatuh tempo <b>{uang.format(ringkasan.data.overdue)}</b></p><p>Umur piutang sejak jatuh tempo: 0–30 hari {uang.format(ringkasan.data.age_0_30)} · 31–60 hari {uang.format(ringkasan.data.age_31_60)} · &gt;60 hari {uang.format(ringkasan.data.age_over_60)}</p></Card>
      <Card><CardHead title="Piutang per cabang dan periode"/>{!ringkasan.data.items.length?<p>Belum ada piutang Tempo.</p>:<Table stack="narrow" caption="Piutang Tempo"><thead><tr><th>Cabang</th><th>Periode</th><th>Jatuh tempo</th><th>Total</th><th>Terbayar</th><th>Sisa</th><th>Status</th></tr></thead><tbody>{ringkasan.data.items.map((r,i)=><tr key={`${r.branch_id}-${r.period_month}-${r.due_date}-${i}`}><td>{r.branch_name}</td><td>{r.period_month}</td><td>{r.due_date}</td><td>{uang.format(r.total)}</td><td>{uang.format(r.paid)}</td><td>{uang.format(r.remaining)}</td><td><Badge tone={r.status==="lewat_jatuh_tempo"?"danger":r.status==="lunas"?"success":"warning"}>{r.status.replaceAll("_"," ")}</Badge></td></tr>)}</tbody></Table>}</Card>
    </>:null}
    <Card><CardHead title="Antrean verifikasi pelunasan"/>{pelunasan.memuat?<SkeletonList rows={2}/>:pelunasan.galat?<Notice tone="danger">{pelunasan.galat}</Notice>:!pelunasan.data?.filter(r=>r.status==="pending").length?<p>Belum ada bukti yang menunggu verifikasi.</p>:pelunasan.data.filter(r=>r.status==="pending").map(r=><div key={r.id} style={{borderBottom:"1px solid var(--border)",padding:"16px 0"}}><p><b>{r.branch_name}</b> · {new Date(r.submitted_at).toLocaleString("id-ID",{timeZone:"Asia/Jakarta"})} · Bukti {uang.format(r.declared_amount)}</p><p>Rekening tujuan: {r.bank} {r.account_number}</p><Button variant="secondary" onClick={()=>void buka(`/admin/tempo/repayments/${r.id}/proof`)}>Lihat bukti {r.filename}</Button><Field label="Nominal terverifikasi (Rp)" htmlFor={`nominal-${r.id}`}><Input id={`nominal-${r.id}`} type="number" min="1" step="1" value={nominal[r.id]??String(r.declared_amount)} onChange={e=>setNominal({...nominal,[r.id]:e.target.value})}/></Field><Field label="Alasan penolakan" htmlFor={`alasan-${r.id}`}><Input id={`alasan-${r.id}`} value={alasan[r.id]??""} onChange={e=>setAlasan({...alasan,[r.id]:e.target.value})}/></Field><div style={{display:"flex",gap:8,flexWrap:"wrap"}}><Button variant="primary" disabled={sibuk!==null} onClick={()=>void putuskan(r,"verify")}>{sibuk===r.id?"Memproses…":"Verifikasi dan alokasikan"}</Button><Button variant="secondary" disabled={sibuk!==null} onClick={()=>void putuskan(r,"reject")}>Tolak bukti</Button></div></div>)}</Card>
    <Card><CardHead title="Surat Tagihan"/>{surat.memuat?<SkeletonList rows={2}/>:surat.galat?<Notice tone="danger">{surat.galat}</Notice>:!surat.data?.length?<p>Belum ada surat tagihan terbit.</p>:<Table stack="narrow" caption="Surat tagihan cabang"><thead><tr><th>Cabang</th><th>Periode</th><th>Versi</th><th>Total</th><th>Status</th><th>Dokumen</th></tr></thead><tbody>{surat.data.map(s=><tr key={s.id}><td>{ringkasan.data?.items.find(r=>r.branch_id===s.branch_id)?.branch_name??s.branch_id}</td><td>{s.period_month.slice(0,7)}</td><td>{s.version}</td><td>{uang.format(s.total)}</td><td>{s.status.replaceAll("_"," ")}</td><td><Button variant="secondary" disabled={!s.ready} onClick={()=>void buka(`/admin/tempo/statements/${s.id}/download`)}>Unduh PDF</Button></td></tr>)}</tbody></Table>}</Card>
  </>;
}
