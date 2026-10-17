import { useEffect, useId, useRef, useState, type PointerEvent } from "react";

import { batasAtas, bilangan, rupiah, sumbuRp } from "./dashboard-format";
import styles from "./dashboard.module.css";

/**
 * Grafik dashboard admin, port dari admin_v2.html (pillBars, gaugeRings,
 * areaChart). Bedanya: grafik batang & area digambar selebar kartunya (diukur
 * ResizeObserver) alih-alih menskalakan viewBox, sehingga label sumbu tetap
 * 11px di kartu lebar maupun di ponsel.
 */

/** useLebar mengukur lebar elemen; awal dipakai sebelum terukur (dan di jsdom). */
function useLebar(awal: number) {
  const ref = useRef<HTMLDivElement>(null);
  const [lebar, setLebar] = useState(awal);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([entri]) => {
      const w = Math.round(entri.contentRect.width);
      if (w > 0) setLebar(w);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return { ref, lebar };
}

/** usePola memberi id unik pola arsir (#hatch prototype) per grafik. */
function usePola() {
  return `arsir-${useId().replace(/[^a-zA-Z0-9-]/g, "")}`;
}

/** DefsArsir adalah pola arsir diagonal + bayangan gelembung prototype. */
function DefsArsir({ pola }: { pola: string }) {
  return (
    <defs>
      <pattern id={pola} width="7" height="7" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
        <rect width="2" height="7" className={styles.arsir} />
      </pattern>
      <filter id={`${pola}-bayang`} x="-30%" y="-40%" width="160%" height="200%">
        <feDropShadow dx="0" dy="4" stdDeviation="5" floodColor="#141419" floodOpacity="0.16" />
      </filter>
    </defs>
  );
}

export interface BatangBulan {
  kunci: string;
  label: string;
  /** Nama bulan lengkap untuk tooltip. */
  nama: string;
  nilai: number;
  keadaan: "lalu" | "kini" | "nanti";
}

/**
 * GrafikBulanan adalah batang-pil 12 bulan: slot berarsir, batang di bawah
 * rata-rata berwarna aksen, bulan berjalan diberi gelembung perubahan.
 */
export function GrafikBulanan({
  batang,
  uang,
  gelembung,
  label,
}: {
  batang: BatangBulan[];
  uang: boolean;
  gelembung: string | null;
  label: string;
}) {
  const pola = usePola();
  const { ref, lebar } = useLebar(600);
  const kiri = 44;
  const atas = 44;
  const bawah = 30;
  const tinggi = 250;
  const langkah = (lebar - kiri) / batang.length;
  const lebarBatang = Math.max(8, Math.min(26, langkah * 0.62));
  const maks = batasAtas(Math.max(...batang.map((b) => b.nilai), 1));
  const aktif = batang.filter((b) => b.keadaan !== "nanti" && b.nilai > 0);
  const rata = aktif.reduce((s, b) => s + b.nilai, 0) / (aktif.length || 1);
  const ph = tinggi - atas - bawah;
  const y = (v: number) => atas + ph * (1 - v / maks);
  // Slot sempit (ponsel): label bulan dijarangkan tiap tiga bulan, tanpa
  // menempel ke label bulan berjalan yang selalu tampil.
  const indeksKini = batang.findIndex((b) => b.keadaan === "kini");
  const tampilLabel = (i: number) =>
    langkah >= 30 || i === indeksKini || (i % 3 === 0 && Math.abs(i - indeksKini) >= 2);

  return (
    <div ref={ref} className={styles.grafikWrap}>
      <svg className={styles.grafik} width={lebar} height={tinggi} viewBox={`0 0 ${lebar} ${tinggi}`} role="img" aria-label={label}>
        <DefsArsir pola={pola} />
        {[0, 1, 2, 3, 4].map((k) => {
          const v = (maks * k) / 4;
          return (
            <text key={k} className={styles.sumbu} x={kiri - 12} y={y(v) + 4} textAnchor="end">
              {uang ? sumbuRp(v) : Math.round(v)}
            </text>
          );
        })}
        {batang.map((b, i) => {
          const cx = kiri + langkah * i + langkah / 2;
          const x = cx - lebarBatang / 2;
          const h = b.nilai > 0 ? Math.max(lebarBatang, (ph * b.nilai) / maks) : 0;
          const kini = b.keadaan === "kini";
          const by = atas + ph - Math.max(h, lebarBatang) - 34;
          return (
            <g key={b.kunci} data-bulan={b.kunci}>
              <rect x={x} y={atas} width={lebarBatang} height={ph} rx={lebarBatang / 2} fill={`url(#${pola})`} />
              {b.keadaan !== "nanti" && h > 0 ? (
                <rect
                  data-batang=""
                  className={b.nilai < rata ? styles.batangRendah : styles.batangTinggi}
                  x={x}
                  y={atas + ph - h}
                  width={lebarBatang}
                  height={h}
                  rx={lebarBatang / 2}
                >
                  <title>{`${b.nama}: ${uang ? rupiah(b.nilai) : `${bilangan(b.nilai)} pesanan`}`}</title>
                </rect>
              ) : null}
              {tampilLabel(i) ? (
                <text className={kini ? `${styles.sumbu} ${styles.sumbuKini}` : styles.sumbu} x={cx} y={tinggi - 12} textAnchor="middle">
                  {b.label}
                </text>
              ) : null}
              {kini ? (
                <>
                  {gelembung ? (
                    <>
                      <rect x={cx - 32} y={by} width={64} height={24} rx={12} fill="#fff" filter={`url(#${pola}-bayang)`} />
                      <text x={cx} y={by + 16} textAnchor="middle" className={styles.gelembung}>
                        {gelembung}
                      </text>
                    </>
                  ) : null}
                  <circle cx={cx} cy={tinggi - 2} r={2.5} className={styles.titikTinta} />
                </>
              ) : null}
            </g>
          );
        })}
      </svg>
    </div>
  );
}

export interface CincinGauge {
  /** Porsi 0–1 dari seluruh pesanan periode. */
  porsi: number;
  warna: string;
  judul: string;
}

/** GaugeStatus adalah cincin konsentris 270° dengan jarum porsi terverifikasi. */
export function GaugeStatus({ cincin, jarum, labelJarum }: { cincin: CincinGauge[]; jarum: number; labelJarum: string }) {
  const pola = usePola();
  const cx = 130;
  const cy = 125;
  const r0 = 100;
  const tebal = 14;
  const celah = 8;
  const a0 = -135;
  const sapu = 270;
  const titik = (r: number, derajat: number) => {
    const a = ((derajat - 90) * Math.PI) / 180;
    return [cx + r * Math.cos(a), cy + r * Math.sin(a)];
  };
  const busur = (r: number, d0: number, d1: number) => {
    const [x0, y0] = titik(r, d0);
    const [x1, y1] = titik(r, d1);
    return `M${x0.toFixed(1)} ${y0.toFixed(1)} A${r} ${r} 0 ${d1 - d0 > 180 ? 1 : 0} 1 ${x1.toFixed(1)} ${y1.toFixed(1)}`;
  };
  const na = a0 + sapu * Math.min(Math.max(jarum, 0), 1);
  const [nx, ny] = titik(r0 + 6, na);

  return (
    <svg className={styles.gauge} viewBox="-30 -10 320 240" role="img" aria-label="Status pembayaran">
      <DefsArsir pola={pola} />
      {cincin.map((c, i) => {
        const r = r0 - i * (tebal + celah);
        const f = Math.min(c.porsi, 0.999);
        return (
          <g key={c.judul}>
            <path d={busur(r, a0, a0 + sapu)} fill="none" strokeWidth={tebal} strokeLinecap="round" stroke={`url(#${pola})`} />
            {f > 0.004 ? (
              <path d={busur(r, a0, a0 + sapu * f)} fill="none" strokeWidth={tebal} strokeLinecap="round" style={{ stroke: c.warna }}>
                <title>{c.judul}</title>
              </path>
            ) : null}
          </g>
        );
      })}
      <line x1={cx} y1={cy} x2={nx.toFixed(1)} y2={ny.toFixed(1)} strokeWidth={2} className={styles.garisTinta} />
      <circle cx={cx} cy={cy} r={6} className={styles.titikTinta} />
      {/* Label di celah bawah busur 270° yang selalu kosong: tidak menimpa
          legenda atau keluar kartu ke mana pun jarum menunjuk. */}
      <text x={cx} y={cy + 70} textAnchor="middle" className={styles.labelJarum} data-testid="label-jarum">
        {labelJarum}
      </text>
      <text x={cx} y={cy + 88} textAnchor="middle" className={styles.sumbu}>
        terverifikasi
      </text>
    </svg>
  );
}

/** halus membentuk kurva Catmull-Rom yang tidak melewati garis dasar. */
function halus(p: number[][], dasar: number): string {
  if (!p.length) return "";
  let d = `M${p[0][0].toFixed(1)} ${p[0][1].toFixed(1)}`;
  for (let i = 0; i < p.length - 1; i++) {
    const p0 = p[i - 1] ?? p[i];
    const p1 = p[i];
    const p2 = p[i + 1];
    const p3 = p[i + 2] ?? p2;
    const c1 = [p1[0] + (p2[0] - p0[0]) / 6, Math.min(dasar, p1[1] + (p2[1] - p0[1]) / 6)];
    const c2 = [p2[0] - (p3[0] - p1[0]) / 6, Math.min(dasar, p2[1] - (p3[1] - p1[1]) / 6)];
    d += ` C${c1[0].toFixed(1)} ${c1[1].toFixed(1)} ${c2[0].toFixed(1)} ${c2[1].toFixed(1)} ${p2[0].toFixed(1)} ${p2[1].toFixed(1)}`;
  }
  return d;
}

/**
 * GrafikHarian adalah area pesanan per hari sepanjang bulan berjalan: area
 * sampai hari ini, sisa bulan berarsir, penanda mengikuti penunjuk (default
 * hari ini) dengan label "tgl.bln · jumlah".
 */
export function GrafikHarian({ nilai, indeksHariIni, bulan }: { nilai: number[]; indeksHariIni: number; bulan: string }) {
  const pola = usePola();
  const { ref, lebar } = useLebar(640);
  const [sorot, setSorot] = useState<number | null>(null);
  const tinggi = 230;
  const kiri = 36;
  const atas = 30;
  const bawah = 30;
  const kanan = 12;
  const n = nilai.length;
  const maks = batasAtas(Math.max(...nilai, 1));
  const ph = tinggi - atas - bawah;
  const dasar = atas + ph;
  const x = (i: number) => kiri + ((lebar - kiri - kanan) * i) / Math.max(n - 1, 1);
  const y = (v: number) => atas + ph * (1 - v / maks);
  const titik = nilai.slice(0, indeksHariIni + 1).map((v, i) => [x(i), y(v)]);
  const aktif = Math.max(0, Math.min(indeksHariIni, sorot ?? indeksHariIni));
  const px = x(aktif);
  const py = y(nilai[aktif] ?? 0);
  const tgl = (i: number) => `${String(i + 1).padStart(2, "0")}.${bulan}`;
  const langkahLabel = lebar < 420 ? 7 : 4;

  function gerak(e: PointerEvent<SVGSVGElement>) {
    const kotak = e.currentTarget.getBoundingClientRect();
    const sx = ((e.clientX - kotak.left) / kotak.width) * lebar;
    let terbaik = 0;
    let jarak = Number.POSITIVE_INFINITY;
    for (let i = 0; i <= indeksHariIni; i++) {
      const d = Math.abs(x(i) - sx);
      if (d < jarak) {
        jarak = d;
        terbaik = i;
      }
    }
    setSorot(terbaik);
  }

  return (
    <div ref={ref} className={styles.grafikWrap}>
      <svg
        className={styles.grafik}
        width={lebar}
        height={tinggi}
        viewBox={`0 0 ${lebar} ${tinggi}`}
        role="img"
        aria-label="Pesanan harian bulan ini"
        onPointerMove={gerak}
        onPointerLeave={() => setSorot(null)}
      >
        <DefsArsir pola={pola} />
        {[0, 1, 2, 3, 4].map((k) => {
          const v = (maks * k) / 4;
          return (
            <text key={k} className={styles.sumbu} x={kiri - 10} y={y(v) + 4} textAnchor="end">
              {Math.round(v)}
            </text>
          );
        })}
        {indeksHariIni < n - 1 ? (
          <rect x={x(indeksHariIni)} y={atas} width={x(n - 1) - x(indeksHariIni)} height={ph} fill={`url(#${pola})`} />
        ) : null}
        {titik.length > 1 ? (
          <path
            data-area=""
            d={`${halus(titik, dasar)} L${titik[titik.length - 1][0].toFixed(1)} ${dasar} L${titik[0][0].toFixed(1)} ${dasar} Z`}
            className={styles.area}
          />
        ) : null}
        {nilai.map((_, i) =>
          i % langkahLabel === 0 ? (
            <text key={i} className={styles.sumbu} x={x(i)} y={tinggi - 8} textAnchor="middle">
              {tgl(i)}
            </text>
          ) : null,
        )}
        <line x1={px} x2={px} y1={atas - 6} y2={dasar} strokeDasharray="3 3" strokeWidth={1.2} className={styles.garisTinta} />
        <circle cx={px} cy={py} r={4} className={styles.titikTinta} />
        <g transform={`translate(${Math.min(Math.max(px, 70), lebar - 50)},${py - 4})`}>
          <rect x={-38} y={-30} width={76} height={24} rx={8} fill="#fff" filter={`url(#${pola}-bayang)`} />
          <text y={-13} textAnchor="middle" className={styles.gelembung} data-testid="tip-harian">
            {`${tgl(aktif)} · ${nilai[aktif] ?? 0}`}
          </text>
        </g>
      </svg>
    </div>
  );
}
