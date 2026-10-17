import { Fragment, useCallback, useEffect, useState, type FormEvent } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { Badge, Button, Card, CardHead, EmptyState, Field, Input, Notice, PageHead, Select, SkeletonList, Table, type BadgeTone } from "@grahita/ui";

import { useApi } from "../useApi";
import { PenjagaSuperadmin } from "./shell-pages";
import { formatWaktu } from "./pengguna";
import styles from "./admin-pages.module.css";

/** Jumlah baris per halaman yang selalu diminta UI (server memagari 100). */
export const PER_HALAMAN = 25;

/** Satu baris audit log (snapshot before/after sudah diredaksi server). */
export interface AuditLog {
  id: string;
  at: string;
  actor_kind: string;
  actor_id?: string;
  actor_nama?: string;
  ip?: string;
  entity: string;
  entity_id?: string;
  action: string;
  before?: unknown;
  after?: unknown;
}

/** Satu halaman audit log beserta ringkasan filter yang dipakai server. */
export interface AuditHalaman {
  items: AuditLog[];
  total: number;
  halaman: number;
  per_halaman: number;
  total_halaman: number;
  filter: {
    entitas?: string[];
    aksi?: string;
    aktor?: string;
    dari?: string;
    sampai?: string;
    halaman?: number;
    per_halaman?: number;
  };
}

/** Filter audit yang disimpan di URL search param (bisa di-bookmark/di-share). */
export interface FilterAudit {
  entitas?: string;
  aksi?: string;
  aktor?: string;
  dari?: string;
  sampai?: string;
  halaman?: number;
}

/** queryAudit menerjemahkan filter menjadi query string API. */
export function queryAudit(f: FilterAudit): string {
  const p = new URLSearchParams();
  if (f.entitas) p.set("entitas", f.entitas);
  if (f.aksi) p.set("aksi", f.aksi);
  if (f.aktor) p.set("aktor", f.aktor);
  if (f.dari) p.set("dari", f.dari);
  if (f.sampai) p.set("sampai", f.sampai);
  p.set("halaman", String(f.halaman && f.halaman > 0 ? f.halaman : 1));
  p.set("per_halaman", String(PER_HALAMAN));
  return p.toString();
}

/**
 * ENTITAS_AUDIT adalah nilai kolom `entity` yang dikenal server. Daftarnya
 * sengaja sama dengan `audit.EntitasSah()` di backend supaya salah pilih tidak
 * pernah menghasilkan 400.
 */
export const ENTITAS_AUDIT: { nilai: string; label: string }[] = [
  { nilai: "order", label: "Pesanan (verifikasi/revisi)" },
  { nilai: "branch", label: "Cabang" },
  { nilai: "branch_price", label: "Harga cabang" },
  { nilai: "product", label: "Produk" },
  { nilai: "branch_user", label: "Akun cabang" },
  { nilai: "admin_user", label: "Akun admin" },
  { nilai: "bank_account", label: "Rekening bank" },
  { nilai: "setting", label: "Pengaturan" },
  { nilai: "tutorial", label: "Tutorial" },
  { nilai: "email", label: "Email pesanan" },
  { nilai: "cnc_reminder", label: "Pengingat C&C" },
];

/** AKSI_AUDIT adalah nilai kolom `action` yang dikenal server. */
export const AKSI_AUDIT: { nilai: string; label: string }[] = [
  { nilai: "create", label: "Buat" },
  { nilai: "update", label: "Ubah" },
  { nilai: "delete", label: "Hapus" },
  { nilai: "verify", label: "Verifikasi" },
  { nilai: "revise", label: "Revisi invoice" },
  { nilai: "reset", label: "Tautan/atur ulang" },
  { nilai: "resend", label: "Kirim ulang" },
  { nilai: "token_generate", label: "Generate token" },
  { nilai: "copy", label: "Salin harga" },
  { nilai: "register", label: "Registrasi akun" },
  { nilai: "remind", label: "Pengingat" },
  { nilai: "baca_bukti", label: "Baca bukti (AI)" },
];

const TONE_AKSI: Record<string, BadgeTone> = {
  create: "success",
  update: "info",
  verify: "success",
  revise: "rev",
  reset: "warning",
  resend: "warning",
  delete: "danger",
};

const LABEL_AKTOR: Record<string, string> = {
  admin: "Admin",
  branch: "Cabang",
  system: "Sistem",
};

function labelEntitas(nilai: string): string {
  return ENTITAS_AUDIT.find((e) => e.nilai === nilai)?.label ?? nilai;
}

/**
 * teksSnapshot menampilkan snapshot audit sebagai JSON rapi. Nilai sensitif
 * sudah berupa "[REDACTED]" dari server, jadi tidak perlu disaring di sini.
 */
export function teksSnapshot(v: unknown): string {
  if (v === null || v === undefined) return "—";
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

/** ringkasRevisi mengambil alasan + delta nilai revisi invoice (F2-25). */
export function ringkasRevisi(after: unknown): { alasan?: string; delta?: number } {
  if (!after || typeof after !== "object") return {};
  const a = after as Record<string, unknown>;
  const out: { alasan?: string; delta?: number } = {};
  if (typeof a.alasan === "string" && a.alasan !== "") out.alasan = a.alasan;
  if (typeof a.delta_total === "number") out.delta = a.delta_total;
  return out;
}

const rupiah = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

/**
 * pAuditLog — /audit (Superadmin, F2-28): daftar audit log yang dapat
 * ditelusuri dengan filter entitas/aksi/aktor/waktu dan paginasi server.
 *
 * Filter disimpan di URL search param (SystemDesign.md §7.2) sehingga hasil
 * bisa di-bookmark/di-share. Menu maupun halamannya menolak Manager; API tetap
 * otoritas akhir (403).
 *
 * Pemuatannya berada di komponen dalam yang baru dipasang setelah penjaga
 * peran lolos, sehingga Manager tidak pernah memanggil API audit.
 */
export function AuditPage() {
  return (
    <PenjagaSuperadmin>
      <AuditContent />
    </PenjagaSuperadmin>
  );
}

function AuditContent() {
  const search = useSearch({ strict: false }) as FilterAudit;
  const navigate = useNavigate();
  const [aktorKetik, setAktorKetik] = useState(search.aktor ?? "");
  const [buka, setBuka] = useState<Record<string, boolean>>({});

  // Ketikan aktor dipertahankan saat URL berubah karena navigasi lain
  // (mis. tombol paginasi) — hanya disinkronkan bila search berubah.
  useEffect(() => {
    setAktorKetik(search.aktor ?? "");
  }, [search.aktor]);

  const ubah = useCallback(
    (patch: FilterAudit, resetHalaman = true) => {
      void navigate({
        to: "/audit",
        search: ((prev: FilterAudit) => ({
          ...prev,
          ...patch,
          ...(resetHalaman ? { halaman: 1 } : {}),
        })) as never,
      });
    },
    [navigate],
  );

  const query = queryAudit(search);
  const { data, memuat, galat } = useApi<AuditHalaman>(`/admin/audit-logs?${query}`);

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const halaman = data?.halaman ?? search.halaman ?? 1;
  const totalHalaman = Math.max(1, data?.total_halaman ?? Math.ceil(total / PER_HALAMAN));
  const adaFilter = Boolean(search.entitas || search.aksi || search.aktor || search.dari || search.sampai);

  return (
    <>
      <PageHead title="Audit log" sub="Seluruh mutasi admin: aktor, waktu, dan diff sebelum/sesudah" />

      <Card>
        <CardHead title="Filter" />
        <form
          className={styles.filterGrid}
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            ubah({ aktor: aktorKetik.trim() || undefined });
          }}
        >
          <Field label="Entitas" htmlFor="aud-entitas">
            <Select
              id="aud-entitas"
              value={search.entitas ?? ""}
              onChange={(e) => ubah({ entitas: e.target.value || undefined })}
            >
              <option value="">Semua entitas</option>
              {ENTITAS_AUDIT.map((e) => (
                <option key={e.nilai} value={e.nilai}>
                  {e.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Aksi" htmlFor="aud-aksi">
            <Select id="aud-aksi" value={search.aksi ?? ""} onChange={(e) => ubah({ aksi: e.target.value || undefined })}>
              <option value="">Semua aksi</option>
              {AKSI_AUDIT.map((a) => (
                <option key={a.nilai} value={a.nilai}>
                  {a.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Aktor" htmlFor="aud-aktor" hint="Nama aktor atau idnya">
            <Input
              id="aud-aktor"
              value={aktorKetik}
              placeholder="Contoh: Rina"
              onChange={(e) => setAktorKetik(e.target.value)}
            />
          </Field>
          <Field label="Dari" htmlFor="aud-dari">
            <Input
              id="aud-dari"
              type="date"
              value={search.dari ?? ""}
              onChange={(e) => ubah({ dari: e.target.value || undefined })}
            />
          </Field>
          <Field label="Sampai" htmlFor="aud-sampai">
            <Input
              id="aud-sampai"
              type="date"
              value={search.sampai ?? ""}
              onChange={(e) => ubah({ sampai: e.target.value || undefined })}
            />
          </Field>
          <div className={styles.filterAksi}>
            <Button type="submit" variant="secondary" icon="magnifying-glass">
              Terapkan
            </Button>
            {adaFilter ? (
              <Button
                type="button"
                variant="ghost"
                onClick={() => {
                  setAktorKetik("");
                  ubah({ entitas: undefined, aksi: undefined, aktor: undefined, dari: undefined, sampai: undefined });
                }}
              >
                Reset filter
              </Button>
            ) : null}
          </div>
        </form>

        <Notice tone="info">
          Kata sandi, token, tautan, secret, dan URL bukti sudah diredaksi menjadi [REDACTED] sebelum
          disimpan maupun ditampilkan.
        </Notice>

        {galat ? <Notice tone="danger">{galat}</Notice> : null}

        {memuat && items.length === 0 ? (
          <SkeletonList rows={4} />
        ) : (
          <Table stack="wide" caption="Audit log aktivitas admin">
            <thead>
              <tr>
                <th>Waktu (WIB)</th>
                <th>Aktor</th>
                <th>Aksi</th>
                <th>Entitas</th>
                <th>Dari IP</th>
                <th>Diff</th>
              </tr>
            </thead>
            <tbody>
              {items.map((a) => {
                const terbuka = Boolean(buka[a.id]);
                const revisi = a.action === "revise" ? ringkasRevisi(a.after) : {};
                return (
                  <Fragment key={a.id}>
                    <tr data-uji="baris-audit">
                      <td className="tabular">{formatWaktu(a.at)}</td>
                      <td>
                        {a.actor_nama || LABEL_AKTOR[a.actor_kind] || a.actor_kind}
                        {" "}
                        <Badge tone={a.actor_kind === "admin" ? "rev" : "neutral"}>
                          {LABEL_AKTOR[a.actor_kind] ?? a.actor_kind}
                        </Badge>
                      </td>
                      <td>
                        <Badge tone={TONE_AKSI[a.action] ?? "neutral"}>
                          {AKSI_AUDIT.find((x) => x.nilai === a.action)?.label ?? a.action}
                        </Badge>
                      </td>
                      <td>
                        {labelEntitas(a.entity)}
                        {a.entity_id ? (
                          <div className="muted small tabular">{a.entity_id.slice(0, 8)}…</div>
                        ) : null}
                      </td>
                      <td className="tabular">{a.ip || "—"}</td>
                      <td>
                        <Button
                          type="button"
                          variant="ghost"
                          onClick={() => setBuka((b) => ({ ...b, [a.id]: !terbuka }))}
                        >
                          {terbuka ? "Sembunyikan" : "Lihat diff"}
                        </Button>
                      </td>
                    </tr>
                    {terbuka ? (
                      <tr key={`${a.id}-detail`} data-uji="detail-audit">
                        <td colSpan={6}>
                          {a.action === "revise" ? (
                            <div className={styles.chips} style={{ marginBottom: 8 }}>
                              <Badge tone="rev">
                                {`Alasan: ${revisi.alasan ?? "—"}`}
                              </Badge>
                              <Badge tone={revisi.delta && revisi.delta < 0 ? "danger" : "success"}>
                                {`Δ nilai: ${revisi.delta === undefined ? "—" : rupiah.format(revisi.delta)}`}
                              </Badge>
                            </div>
                          ) : null}
                          <div className={styles.grid}>
                            <div>
                              <div className="muted small">Sebelum</div>
                              <pre data-uji="audit-before">{teksSnapshot(a.before)}</pre>
                            </div>
                            <div>
                              <div className="muted small">Sesudah</div>
                              <pre data-uji="audit-after">{teksSnapshot(a.after)}</pre>
                            </div>
                          </div>
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
              {items.length === 0 && !memuat ? (
                <tr>
                  <td colSpan={6}>
                    <EmptyState icon="clock-clockwise">
                      Belum ada aktivitas admin untuk filter ini.
                    </EmptyState>
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
          <span className="muted small" data-uji="info-halaman-audit">
            {`Halaman ${halaman} dari ${totalHalaman} · ${total} entri`}
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
