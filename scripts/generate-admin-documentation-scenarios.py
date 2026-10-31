#!/usr/bin/env python3
"""Salin katalog 162 skenario dari rencana E2E ke dokumentasi portal admin.

Jalankan dari root repo setiap kali docs/testing/skenario-e2e-seluruh-fitur.md
berubah. File JSON hasilnya statis agar image web produksi tidak perlu membawa
seluruh folder docs.
"""

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "docs/testing/skenario-e2e-seluruh-fitur.md"
TARGET = ROOT / "web/apps/admin/src/pages/documentation-scenarios.json"

groups = []
current = None
for line in SOURCE.read_text(encoding="utf-8").splitlines():
    if line.startswith("## 2. Perjalanan end to end wajib"):
        current = {"title": "Perjalanan end to end wajib", "items": []}
        groups.append(current)
        continue
    if line.startswith("### "):
        name = line[4:].strip()
        if name == "Data awal yang disiapkan":
            continue
        current = {"title": name, "items": []}
        groups.append(current)
        continue
    if not re.match(r"^\|\s*(?:E2E|[A-Z]{3})-\d+\s*\|", line):
        continue
    if current is None:
        raise SystemExit("Baris skenario tanpa heading")
    cols = [part.strip() for part in line.strip().strip("|").split("|")]
    if cols[0].startswith("E2E-"):
        if len(cols) != 3:
            raise SystemExit(f"Format E2E tidak dikenal: {line}")
        ident, priority, action = cols
        item = {"id": ident, "priority": priority, "layer": "UI · API · DB · Job", "action": action, "expected": ""}
    else:
        if len(cols) != 5:
            raise SystemExit(f"Format skenario tidak dikenal: {line}")
        ident, priority, layer, action, expected = cols
        item = {"id": ident, "priority": priority, "layer": layer, "action": action, "expected": expected}
    current["items"].append(item)

groups = [group for group in groups if group["items"]]
ids = [item["id"] for group in groups for item in group["items"]]
if len(ids) != 162 or len(set(ids)) != 162:
    raise SystemExit(f"Katalog harus 162 ID unik; dapat {len(ids)} baris, {len(set(ids))} unik")
TARGET.write_text(json.dumps(groups, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
print(f"{len(groups)} kelompok, {len(ids)} skenario -> {TARGET}")
