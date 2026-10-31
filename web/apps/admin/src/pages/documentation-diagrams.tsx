import styles from "./documentation.module.css";

type FlowNode = { label: string; decision?: boolean; continueWhen?: string };
type FlowBranch = { condition: string; outcome: string };

const flows: Record<string, { nodes: FlowNode[]; branches: FlowBranch[] }> = {
  akses: {
    nodes: [{ label: "Superadmin buat token" }, { label: "Token masih valid?", decision: true, continueWhen: "Ya → lanjut" }, { label: "Cabang aktivasi akun" }, { label: "Masuk ke portal" }],
    branches: [{ condition: "Tidak", outcome: "Minta token baru ke Superadmin" }, { condition: "Lupa kata sandi", outcome: "Reset sandi → sesi lama dicabut" }],
  },
  master: {
    nodes: [{ label: "Buat cabang & izin bayar" }, { label: "Atur rekening & produk" }, { label: "Tetapkan harga cabang" }, { label: "Katalog siap dipakai?", decision: true }],
    branches: [{ condition: "Ya", outcome: "Cabang dapat membuat pesanan" }, { condition: "Tidak", outcome: "Lengkapi produk aktif dan tarif cabang" }],
  },
  pesanan: {
    nodes: [{ label: "Buat draft per instansi" }, { label: "Isi produk, qty & Ket" }, { label: "Data & pembayaran valid?", decision: true, continueWhen: "Ya → lanjut" }, { label: "Kirim → nomor & snapshot" }],
    branches: [{ condition: "Tidak", outcome: "Perbaiki draft atau unggah bukti yang diwajibkan" }, { condition: "Ya", outcome: "Pantau status dan dokumen pesanan" }],
  },
  transfer: {
    nodes: [{ label: "Pilih Transfer & unggah bukti" }, { label: "Admin tinjau / OCR opsional" }, { label: "Nominal cocok?", decision: true, continueWhen: "Ya → lanjut" }, { label: "Lunas → invoice & surat jalan" }],
    branches: [{ condition: "Tidak", outcome: "Perlu review → koreksi bukti atau nominal" }, { condition: "Lebih bayar", outcome: "Keputusan alokasi oleh Superadmin" }],
  },
  deposit: {
    nodes: [{ label: "Pilih Deposit" }, { label: "Saldo cukup?", decision: true, continueWhen: "Ya → lanjut" }, { label: "Alokasi saldo & verifikasi" }, { label: "Lunas → invoice & surat jalan" }],
    branches: [{ condition: "Tidak", outcome: "Unggah bukti selisih / top-up → verifikasi admin" }, { condition: "Revisi", outcome: "Otorisasi & reversal jurnal bila alokasi berubah" }],
  },
  tempo: {
    nodes: [{ label: "Pilih Tempo & kirim" }, { label: "Tunggakan menahan submit?", decision: true, continueWhen: "Tidak → lanjut" }, { label: "Admin setujui → dokumen" }, { label: "Tagihan → bayar → FIFO → lunas" }],
    branches: [{ condition: "Ya", outcome: "Lunasi tunggakan atau tunggu verifikasi bukti pending" }, { condition: "Bayar sebagian", outcome: "Sisa piutang tetap terbuka sampai lunas" }],
  },
  dokumen: {
    nodes: [{ label: "Buka detail & dokumen" }, { label: "Perlu koreksi?", decision: true, continueWhen: "Ya → lanjut" }, { label: "Revisi beralasan" }, { label: "Versi baru → unduh / email" }],
    branches: [{ condition: "Tidak", outcome: "Unduh versi yang tersedia" }, { condition: "Job belum selesai", outcome: "Periksa antrean dan status render / email" }],
  },
  pantau: {
    nodes: [{ label: "Buka dashboard & laporan" }, { label: "Ada masalah transaksi?", decision: true, continueWhen: "Ya → lanjut" }, { label: "Periksa timeline, job & audit" }, { label: "Tindak lanjut → ekspor" }],
    branches: [{ condition: "Tidak", outcome: "Pantau piutang, deposit, dan pesanan berkala" }, { condition: "Akses bermasalah", outcome: "Periksa akun, peran, sesi, dan izin cabang" }],
  },
  skenario: {
    nodes: [{ label: "Pilih kelompok skenario" }, { label: "Cari ID atau fitur" }, { label: "Jalankan aksi & variasi" }, { label: "Cocokkan hasil aktual", decision: true }],
    branches: [{ condition: "Sesuai", outcome: "Catat bukti dan hasil lulus pada laporan uji" }, { condition: "Berbeda", outcome: "Catat temuan, bukti, dan lingkungan uji" }],
  },
};

export function GuideFlowDiagram({ sectionId, title }: { sectionId: string; title: string }) {
  const flow = flows[sectionId];
  if (!flow) return null;
  return (
    <figure className={styles.sectionFlow} aria-label={`Diagram alur ${title}`}>
      <figcaption>Diagram alur · {title}</figcaption>
      <ol className={styles.sectionFlowNodes}>
        {flow.nodes.map((node, index) => (
          <li key={node.label} className={node.decision ? styles.sectionFlowDecision : undefined}>
            <span className={styles.sectionFlowIndex}>{String(index + 1).padStart(2, "0")}{node.decision ? " · Keputusan" : ""}</span>
            <strong>{node.label}</strong>
            {node.continueWhen ? <small className={styles.sectionFlowContinue}>{node.continueWhen}</small> : null}
          </li>
        ))}
      </ol>
      <div className={styles.sectionFlowBranches} aria-label="Cabang keputusan dan tindakan lanjutan">
        {flow.branches.map((branch) => (
          <div key={branch.condition}><span>{branch.condition}</span><span aria-hidden="true">→</span><p>{branch.outcome}</p></div>
        ))}
      </div>
    </figure>
  );
}

type Entity = { name: string; fields: string[]; x: number; y: number };
type Relation = { from: string; to: string; fromCardinality: string; toCardinality: string; fromAnchor: [number, number]; toAnchor: [number, number]; path?: string };
type SchemaDiagram = { title: string; description: string; entities: Entity[]; relations: Relation[] };

const schemaDiagrams: SchemaDiagram[] = [
  {
    title: "Tarif efektif dan rekening tujuan cabang",
    description: "Penugasan rekening dan versi tarif menghubungkan cabang ke rekening bank serta varian produk. Tarif historis tetap tersimpan saat versi baru berlaku.",
    entities: [
      { name: "branches", x: 20, y: 20, fields: ["PK id", "nama", "bank_allowlist_version"] },
      { name: "branch_bank_accounts", x: 335, y: 20, fields: ["PK/FK branch_id", "PK/FK bank_account_id", "is_allowed"] },
      { name: "bank_accounts", x: 650, y: 20, fields: ["PK id", "bank, nomor", "is_active"] },
      { name: "branch_tariff_versions", x: 20, y: 240, fields: ["PK id", "FK branch_id", "FK variant_id", "valid_from, superseded_at"] },
      { name: "product_variants", x: 335, y: 240, fields: ["PK id", "FK product_id", "code, nama"] },
      { name: "products", x: 650, y: 240, fields: ["PK id", "nama, jenis", "is_active"] },
    ],
    relations: [
      { from: "branches", to: "branch_bank_accounts", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [270, 95], toAnchor: [335, 95] },
      { from: "bank_accounts", to: "branch_bank_accounts", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [650, 130], toAnchor: [585, 130] },
      { from: "branches", to: "branch_tariff_versions", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [145, 180], toAnchor: [145, 240] },
      { from: "product_variants", to: "branch_tariff_versions", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [335, 315], toAnchor: [270, 315] },
      { from: "products", to: "product_variants", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [650, 345], toAnchor: [585, 345] },
    ],
  },
  {
    title: "Master, harga, dan pesanan",
    description: "Cabang memiliki pengguna, harga per produk, dan banyak pesanan. Pesanan memiliki banyak baris; baris produk mengacu ke master produk.",
    entities: [
      { name: "branches", x: 20, y: 20, fields: ["PK id", "nama, email", "allow_transfer, is_cnc, is_tempo", "tanggal_bayar"] },
      { name: "branch_prices", x: 335, y: 20, fields: ["PK/FK branch_id", "PK/FK product_id", "harga"] },
      { name: "products", x: 650, y: 20, fields: ["PK id", "nama, jenis", "is_active"] },
      { name: "branch_users", x: 20, y: 240, fields: ["PK id", "FK branch_id UNIQUE", "email"] },
      { name: "orders", x: 335, y: 240, fields: ["PK id", "FK branch_id", "status, total", "is_cnc, is_tempo"] },
      { name: "order_lines", x: 650, y: 240, fields: ["PK id", "FK order_id", "FK product_id", "nama_instansi, serial_number"] },
    ],
    relations: [
      { from: "branches", to: "branch_prices", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [270, 95], toAnchor: [335, 95] },
      { from: "products", to: "branch_prices", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [650, 130], toAnchor: [585, 130] },
      { from: "branches", to: "branch_users", fromCardinality: "1", toCardinality: "0..1", fromAnchor: [145, 180], toAnchor: [145, 240] },
      { from: "branches", to: "orders", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [270, 180], toAnchor: [335, 240] },
      { from: "orders", to: "order_lines", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [585, 315], toAnchor: [650, 315] },
      { from: "products", to: "order_lines", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [775, 180], toAnchor: [775, 240] },
    ],
  },
  {
    title: "Pembayaran dan jurnal Deposit",
    description: "Bukti dan verifikasi terkait pesanan. Top-up serta alokasi Deposit dicatat melalui jurnal, dengan FK ke bukti atau pesanan yang menjadi sumbernya.",
    entities: [
      { name: "orders", x: 20, y: 20, fields: ["PK id", "FK branch_id", "status, total"] },
      { name: "payment_proofs", x: 335, y: 20, fields: ["PK id", "FK order_id? / branch_id", "purpose, storage_key"] },
      { name: "verification_logs", x: 650, y: 20, fields: ["PK id", "FK order_id", "hasil, admin_id"] },
      { name: "deposit_topups", x: 20, y: 240, fields: ["PK id", "FK branch_id", "FK payment_proof_id UNIQUE"] },
      { name: "branch_deposit_transactions", x: 335, y: 240, fields: ["PK id", "FK branch_id", "FK source_proof_id?", "amount, kind"] },
      { name: "deposit_allocations", x: 650, y: 240, fields: ["PK id", "FK order_id", "FK debit_transaction_id", "amount"] },
    ],
    relations: [
      { from: "orders", to: "payment_proofs", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [270, 95], toAnchor: [335, 95] },
      { from: "orders", to: "verification_logs", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [145, 20], toAnchor: [775, 20], path: "M145 20 V8 H775 V20" },
      { from: "payment_proofs", to: "deposit_topups", fromCardinality: "1", toCardinality: "0..1", fromAnchor: [335, 180], toAnchor: [270, 240] },
      { from: "payment_proofs", to: "branch_deposit_transactions", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [460, 180], toAnchor: [460, 240] },
      { from: "branch_deposit_transactions", to: "deposit_allocations", fromCardinality: "1", toCardinality: "0..1", fromAnchor: [585, 315], toAnchor: [650, 315] },
    ],
  },
  {
    title: "Piutang Tempo dan Surat Tagihan",
    description: "Charge timbul dari pesanan. Receipt dan kredit dialokasikan ke charge; Surat Tagihan menyimpan baris snapshot per periode.",
    entities: [
      { name: "orders", x: 20, y: 20, fields: ["PK id", "FK branch_id", "tempo_due_date, status"] },
      { name: "tempo_charges", x: 335, y: 20, fields: ["PK id", "FK order_id", "amount, paid_amount", "due_date"] },
      { name: "tempo_allocations", x: 650, y: 20, fields: ["PK id", "FK charge_id", "FK receipt_id? / credit_entry_id?", "amount"] },
      { name: "tempo_receipts", x: 20, y: 240, fields: ["PK id", "FK payment_proof_id UNIQUE", "FK branch_id", "verified_amount"] },
      { name: "tempo_statements", x: 335, y: 240, fields: ["PK id", "FK branch_id", "period_month, version"] },
      { name: "tempo_statement_lines", x: 650, y: 240, fields: ["PK statement_id, charge_id", "FK order_id", "amount, paid_amount"] },
    ],
    relations: [
      { from: "orders", to: "tempo_charges", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [270, 95], toAnchor: [335, 95] },
      { from: "tempo_charges", to: "tempo_allocations", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [585, 95], toAnchor: [650, 95] },
      { from: "tempo_receipts", to: "tempo_allocations", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [270, 315], toAnchor: [650, 180] },
      { from: "tempo_statements", to: "tempo_statement_lines", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [585, 315], toAnchor: [650, 315] },
      { from: "tempo_charges", to: "tempo_statement_lines", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [460, 180], toAnchor: [775, 240] },
    ],
  },
  {
    title: "Dokumen dan revisi invoice",
    description: "Dokumen dan revisi merupakan anak pesanan. Versi menjaga riwayat PDF/XLSX tanpa mengganti nomor invoice.",
    entities: [
      { name: "orders", x: 20, y: 20, fields: ["PK id", "nomor, versi", "status, total"] },
      { name: "documents", x: 335, y: 20, fields: ["PK id", "FK order_id", "jenis, versi", "storage_key"] },
      { name: "invoice_revisions", x: 650, y: 20, fields: ["PK id", "FK order_id", "versi_lama, versi_baru", "delta_total, snapshot"] },
      { name: "order_events", x: 20, y: 240, fields: ["PK id", "FK order_id", "at, teks, actor_kind"] },
      { name: "email_outbox", x: 335, y: 240, fields: ["PK id", "FK order_id?", "jenis, status"] },
    ],
    relations: [
      { from: "orders", to: "documents", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [270, 95], toAnchor: [335, 95] },
      { from: "orders", to: "invoice_revisions", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [145, 20], toAnchor: [775, 20], path: "M145 20 V8 H775 V20" },
      { from: "orders", to: "order_events", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [145, 180], toAnchor: [145, 240] },
      { from: "orders", to: "email_outbox", fromCardinality: "1", toCardinality: "0..*", fromAnchor: [270, 180], toAnchor: [335, 240] },
    ],
  },
];

function edgeLabelPosition(anchor: [number, number], other: [number, number]) {
  const direction = Math.sign(other[0] - anchor[0]);
  const textAnchor: "end" | "start" = direction < 0 ? "end" : "start";
  return {
    x: anchor[0] + (direction * 10 || 10),
    y: anchor[1] - 8,
    textAnchor,
  };
}

function DatabaseDiagram({ diagram }: { diagram: SchemaDiagram }) {
  return (
    <figure className={styles.umlFigure}>
      <figcaption>{diagram.title}</figcaption>
      <p>{diagram.description}</p>
      <p className={styles.umlScrollHint}>Geser diagram ke kanan untuk melihat seluruh tabel dan relasinya →</p>
      <div className={styles.umlViewport} tabIndex={0} aria-label={`Diagram UML database: ${diagram.title}. Geser horizontal untuk melihat seluruh tabel.`}>
        <svg viewBox="0 0 920 420" width="920" height="420" role="img" aria-label={`Diagram UML database ${diagram.title}: tabel, kolom kunci, dan kardinalitas relasinya`}>
          <g className={styles.umlEdges}>
            {diagram.relations.map((relation) => {
              const startLabel = edgeLabelPosition(relation.fromAnchor, relation.toAnchor);
              const endLabel = edgeLabelPosition(relation.toAnchor, relation.fromAnchor);
              return <g key={`${relation.from}-${relation.to}`}>
                {relation.path ? <path d={relation.path} /> : <line x1={relation.fromAnchor[0]} y1={relation.fromAnchor[1]} x2={relation.toAnchor[0]} y2={relation.toAnchor[1]} />}
                <text x={startLabel.x} y={startLabel.y} textAnchor={startLabel.textAnchor}>{relation.fromCardinality}</text>
                <text x={endLabel.x} y={endLabel.y} textAnchor={endLabel.textAnchor}>{relation.toCardinality}</text>
              </g>;
            })}
          </g>
          {diagram.entities.map((entity) => (
            <g key={entity.name} className={styles.umlEntity}>
              <rect x={entity.x} y={entity.y} width="250" height="160" rx="10" />
              <path d={`M ${entity.x} ${entity.y + 38} H ${entity.x + 250}`} />
              <text className={styles.umlEntityTitle} x={entity.x + 12} y={entity.y + 25}>{entity.name}</text>
              {entity.fields.map((field, index) => <text key={field} x={entity.x + 12} y={entity.y + 62 + index * 22}>{field}</text>)}
            </g>
          ))}
        </svg>
      </div>
      <ul className={styles.umlRelationList} aria-label={`Relasi tabel ${diagram.title}`}>
        {diagram.relations.map((relation) => <li key={`${relation.from}-${relation.to}`}><code>{relation.from}</code> ({relation.fromCardinality}) → <code>{relation.to}</code> ({relation.toCardinality})</li>)}
      </ul>
    </figure>
  );
}

export function DatabaseUmlDiagrams() {
  return <div className={styles.umlStack}>{schemaDiagrams.map((diagram) => <DatabaseDiagram key={diagram.title} diagram={diagram} />)}</div>;
}
