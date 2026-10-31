import styles from "./documentation.module.css";

type Step = { label: string; input?: boolean };
type Alternate = { condition: string; action: string; next: "loop" | "success" | "end"; loopTo?: number };
type Flow = {
  before: Step[];
  decision: string;
  continueWhen: string;
  after: Step[];
  alternate: Alternate;
  variation?: { condition: string; action: string };
};

const flows: Record<string, Flow> = {
  akses: {
    before: [{ label: "Superadmin membuat token" }],
    decision: "Token masih valid?",
    continueWhen: "Ya",
    after: [{ label: "Cabang aktivasi akun", input: true }, { label: "Masuk ke portal", input: true }],
    alternate: { condition: "Tidak", action: "Minta token baru ke Superadmin", next: "loop", loopTo: 0 },
    variation: { condition: "Lupa kata sandi", action: "Reset sandi; sesi lama dicabut" },
  },
  master: {
    before: [{ label: "Buat cabang & izin bayar", input: true }, { label: "Atur rekening & produk", input: true }, { label: "Tetapkan harga cabang", input: true }],
    decision: "Katalog siap dipakai?",
    continueWhen: "Ya",
    after: [{ label: "Cabang dapat membuat pesanan" }],
    alternate: { condition: "Tidak", action: "Lengkapi produk aktif dan tarif cabang", next: "loop", loopTo: 1 },
  },
  pesanan: {
    before: [{ label: "Buat draft per instansi", input: true }, { label: "Isi produk, qty & Ket", input: true }],
    decision: "Data & pembayaran valid?",
    continueWhen: "Ya",
    after: [{ label: "Kirim; nomor & snapshot dibuat" }],
    alternate: { condition: "Tidak", action: "Perbaiki draft atau unggah bukti wajib", next: "loop", loopTo: 1 },
    variation: { condition: "Setelah kirim", action: "Pantau status dan dokumen pesanan" },
  },
  transfer: {
    before: [{ label: "Pilih Transfer & unggah bukti", input: true }, { label: "Admin tinjau / OCR opsional" }],
    decision: "Nominal cocok?",
    continueWhen: "Ya",
    after: [{ label: "Lunas; invoice & surat jalan" }],
    alternate: { condition: "Tidak", action: "Perlu review; koreksi bukti atau nominal", next: "loop", loopTo: 1 },
    variation: { condition: "Lebih bayar", action: "Superadmin memutuskan alokasinya" },
  },
  deposit: {
    before: [{ label: "Pilih Deposit", input: true }],
    decision: "Saldo menutup total?",
    continueWhen: "Ya",
    after: [{ label: "Alokasi & verifikasi; dokumen terbit" }],
    alternate: { condition: "Tidak", action: "Unggah bukti selisih atau top-up", next: "success" },
    variation: { condition: "Revisi", action: "Otorisasi dan reversal jurnal bila alokasi berubah" },
  },
  tempo: {
    before: [{ label: "Pilih Tempo & kirim", input: true }],
    decision: "Tunggakan menahan submit?",
    continueWhen: "Tidak",
    after: [{ label: "Admin setujui; dokumen terbit" }, { label: "Tagihan → bayar → FIFO → lunas" }],
    alternate: { condition: "Ya", action: "Lunasi tunggakan atau tunggu verifikasi", next: "loop", loopTo: 0 },
    variation: { condition: "Bayar sebagian", action: "Sisa piutang tetap terbuka sampai lunas" },
  },
  dokumen: {
    before: [{ label: "Buka detail & dokumen" }],
    decision: "Perlu koreksi?",
    continueWhen: "Ya",
    after: [{ label: "Buat revisi beralasan", input: true }, { label: "Versi baru → unduh / email" }],
    alternate: { condition: "Tidak", action: "Unduh versi yang tersedia", next: "end" },
    variation: { condition: "Job belum selesai", action: "Periksa antrean render dan email" },
  },
  pantau: {
    before: [{ label: "Buka dashboard & laporan" }],
    decision: "Ada masalah transaksi?",
    continueWhen: "Ya",
    after: [{ label: "Periksa timeline, job & audit" }, { label: "Tindak lanjut & ekspor laporan" }],
    alternate: { condition: "Tidak", action: "Pantau piutang, deposit, dan pesanan", next: "end" },
    variation: { condition: "Akses bermasalah", action: "Periksa akun, peran, sesi, dan izin cabang" },
  },
  skenario: {
    before: [{ label: "Pilih kelompok skenario", input: true }, { label: "Cari ID atau fitur", input: true }, { label: "Jalankan aksi & variasi" }],
    decision: "Hasil sesuai harapan?",
    continueWhen: "Ya",
    after: [{ label: "Catat bukti & hasil lulus" }],
    alternate: { condition: "Tidak", action: "Catat temuan, bukti & lingkungan uji", next: "end" },
  },
};

function splitLabel(label: string, maxLength: number) {
  const lines: string[] = [];
  let line = "";
  for (const word of label.split(" ")) {
    if (line && `${line} ${word}`.length > maxLength) {
      lines.push(line);
      line = word;
    } else {
      line = line ? `${line} ${word}` : word;
    }
  }
  if (line) lines.push(line);
  return lines;
}

function FlowShape({ x, y, label, kind }: { x: number; y: number; label: string; kind: "terminal" | "input" | "decision" | "process" | "alternate" }) {
  const width = kind === "alternate" ? 200 : 190;
  const height = kind === "alternate" ? 96 : 76;
  const lines = splitLabel(label, kind === "decision" ? 16 : 23);
  const lineHeight = 17;
  const firstY = y - ((lines.length - 1) * lineHeight) / 2 + 5;
  return <g className={styles.flowShape}>
    {kind === "terminal" ? <ellipse className={styles.flowTerminal} cx={x} cy={y} rx="64" ry="27" /> : null}
    {kind === "input" ? <polygon className={styles.flowInput} points={`${x - width / 2 + 18},${y - height / 2} ${x + width / 2},${y - height / 2} ${x + width / 2 - 18},${y + height / 2} ${x - width / 2},${y + height / 2}`} /> : null}
    {kind === "decision" ? <polygon className={styles.flowDecisionDiamond} points={`${x},${y - 63} ${x + 90},${y} ${x},${y + 63} ${x - 90},${y}`} /> : null}
    {kind === "process" || kind === "alternate" ? <rect className={styles.flowProcess} x={x - width / 2} y={y - height / 2} width={width} height={height} rx="8" /> : null}
    <text className={styles.flowShapeText} textAnchor="middle">
      {lines.map((line, index) => <tspan key={`${line}-${index}`} x={x} y={firstY + index * lineHeight}>{line}</tspan>)}
    </text>
  </g>;
}

function Flowchart({ flow, sectionId, title }: { flow: Flow; sectionId: string; title: string }) {
  const mainX = 220;
  const rightX = 470;
  const startY = 40;
  const beforeY = (index: number) => 150 + index * 125;
  const decisionY = beforeY(flow.before.length);
  const alternateY = decisionY + 175;
  const endY = decisionY + 300;
  const height = endY + 45;
  const arrowId = `flow-arrow-${sectionId}`;
  const marker = `url(#${arrowId})`;
  const lastBeforeY = beforeY(flow.before.length - 1);
  const lastAfterY = decisionY + (flow.after.length - 1) * 115;
  const loopTargetY = beforeY(flow.alternate.loopTo ?? 0);

  return <svg className={styles.flowChartSvg} width="620" height={height} viewBox={`0 0 620 ${height}`} role="img" aria-label={`Flowchart ${title}: mulai, ${flow.before.map((step) => step.label).join(", ")}, keputusan ${flow.decision}, ${flow.continueWhen} menuju ${flow.after.map((step) => step.label).join(", ")}, ${flow.alternate.condition} menuju ${flow.alternate.action}, selesai.`}>
    <defs><marker id={arrowId} viewBox="0 0 8 8" refX="7" refY="4" markerWidth="8" markerHeight="8" orient="auto"><path className={styles.flowArrowHead} d="M0 0 L8 4 L0 8 Z" /></marker></defs>
    <g className={styles.flowConnectors} fill="none" markerEnd={marker}>
      <path d={`M${mainX} 67 V112`} />
      {flow.before.slice(1).map((step, index) => <path key={step.label} d={`M${mainX} ${beforeY(index) + 38} V${beforeY(index + 1) - 38}`} />)}
      <path d={`M${mainX} ${lastBeforeY + 38} V${decisionY - 63}`} />
      <path d={`M${mainX + 90} ${decisionY} H${rightX - 95}`} />
      {flow.after.slice(1).map((step, index) => <path key={step.label} d={`M${rightX} ${decisionY + index * 115 + 38} V${decisionY + (index + 1) * 115 - 38}`} />)}
      <path d={`M${rightX} ${lastAfterY + 38} V${endY} H${mainX + 64}`} />
      <path d={`M${mainX} ${decisionY + 63} V${alternateY - 48}`} />
      {flow.alternate.next === "loop" ? <path d={`M${mainX - 100} ${alternateY} H45 V${loopTargetY} H${mainX - 95}`} /> : null}
      {flow.alternate.next === "success" ? <path d={`M${mainX + 100} ${alternateY} H${rightX} V${decisionY + 38}`} /> : null}
      {flow.alternate.next === "end" ? <path d={`M${mainX} ${alternateY + 48} V${endY - 27}`} /> : null}
    </g>
    <text className={styles.flowBranchLabel} x="342" y={decisionY - 12} textAnchor="middle">{flow.continueWhen}</text>
    <text className={styles.flowBranchLabel} x={mainX + 14} y={decisionY + 105}>{flow.alternate.condition}</text>
    <FlowShape x={mainX} y={startY} label="Mulai" kind="terminal" />
    {flow.before.map((step, index) => <FlowShape key={step.label} x={mainX} y={beforeY(index)} label={step.label} kind={step.input ? "input" : "process"} />)}
    <FlowShape x={mainX} y={decisionY} label={flow.decision} kind="decision" />
    {flow.after.map((step, index) => <FlowShape key={step.label} x={rightX} y={decisionY + index * 115} label={step.label} kind={step.input ? "input" : "process"} />)}
    <FlowShape x={mainX} y={alternateY} label={flow.alternate.action} kind="alternate" />
    <FlowShape x={mainX} y={endY} label="Selesai" kind="terminal" />
  </svg>;
}

export function GuideFlowDiagram({ sectionId, title }: { sectionId: string; title: string }) {
  const flow = flows[sectionId];
  if (!flow) return null;
  return <figure className={styles.sectionFlow} aria-label={`Diagram alur ${title}`}>
    <figcaption>Diagram alur · {title}</figcaption>
    <p className={styles.flowLegend}>Oval: mulai/selesai · Jajaran genjang: masukan · Belah ketupat: keputusan · Kotak: proses</p>
    <p className={styles.flowScrollHint}>Geser diagram ke kanan untuk melihat seluruh alur →</p>
    <div className={styles.flowChartViewport} tabIndex={0} aria-label={`Flowchart ${title}. Geser secara horizontal pada layar kecil untuk melihat seluruh alur.`}>
      <Flowchart flow={flow} sectionId={sectionId} title={title} />
    </div>
    {flow.variation ? <p className={styles.flowVariation}><b>{flow.variation.condition}:</b> {flow.variation.action}.</p> : null}
  </figure>;
}

export function OverviewFlowDiagram() {
  const arrowId = "flow-arrow-overview";
  const marker = `url(#${arrowId})`;
  const branches = [
    { x: 115, title: "Transfer", first: "Unggah bukti transfer", second: "Verifikasi → lunas → dokumen", input: true },
    { x: 350, title: "Deposit", first: "Pakai saldo / bukti selisih", second: "Verifikasi → lunas → dokumen", input: true },
    { x: 585, title: "Tempo", first: "Setujui tanpa bukti", second: "Dokumen → tagihan → lunas", input: false },
  ] as const;
  return <figure className={styles.sectionFlow} aria-label="Diagram alur utama pesanan">
    <figcaption>Diagram alur · dari master hingga laporan</figcaption>
    <p className={styles.flowLegend}>Oval: mulai/selesai · Jajaran genjang: masukan · Belah ketupat: pilihan pembayaran · Kotak: proses</p>
    <p className={styles.flowScrollHint}>Geser diagram ke kanan untuk melihat seluruh alur →</p>
    <div className={styles.flowChartViewport} tabIndex={0} aria-label="Flowchart alur utama. Geser secara horizontal pada layar kecil untuk melihat ketiga jalur pembayaran.">
      <svg className={styles.flowOverviewSvg} width="700" height="935" viewBox="0 0 700 935" role="img" aria-label="Mulai, siapkan master, buat draft, pilih Transfer atau Deposit atau Tempo, selesaikan pembayaran sesuai mode, lalu lihat dokumen, riwayat, dan laporan.">
        <defs><marker id={arrowId} viewBox="0 0 8 8" refX="7" refY="4" markerWidth="8" markerHeight="8" orient="auto"><path className={styles.flowArrowHead} d="M0 0 L8 4 L0 8 Z" /></marker></defs>
        <g className={styles.flowConnectors} fill="none" markerEnd={marker}>
          <path d="M350 67 V102" />
          <path d="M350 178 V207" />
          <path d="M350 283 V317" />
          <path d="M260 380 H115 V462" />
          <path d="M350 443 V462" />
          <path d="M440 380 H585 V462" />
          {branches.map((branch) => <path key={branch.title} d={`M${branch.x} 538 V577`} />)}
          <path d="M115 653 V710 H350" markerEnd="none" />
          <path d="M585 653 V710 H350" markerEnd="none" />
          <path d="M350 653 V752" />
          <path d="M350 828 V868" />
        </g>
        <text className={styles.flowBranchLabel} x="185" y="368" textAnchor="middle">Transfer</text>
        <text className={styles.flowBranchLabel} x="375" y="448">Deposit</text>
        <text className={styles.flowBranchLabel} x="510" y="368" textAnchor="middle">Tempo</text>
        <FlowShape x={350} y={40} label="Mulai" kind="terminal" />
        <FlowShape x={350} y={140} label="Siapkan cabang, tarif & rekening" kind="input" />
        <FlowShape x={350} y={245} label="Buat draft & pilih cara bayar" kind="input" />
        <FlowShape x={350} y={380} label="Cara pembayaran?" kind="decision" />
        {branches.map((branch) => <g key={branch.title}>
          <FlowShape x={branch.x} y={500} label={branch.first} kind={branch.input ? "input" : "process"} />
          <FlowShape x={branch.x} y={615} label={branch.second} kind="process" />
        </g>)}
        <FlowShape x={350} y={790} label="Lihat riwayat & laporan" kind="process" />
        <FlowShape x={350} y={895} label="Selesai" kind="terminal" />
      </svg>
    </div>
  </figure>;
}
