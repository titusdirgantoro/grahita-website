#!/usr/bin/env python3
"""Memeriksa konsistensi uid sumber data Grafana:
setiap uid yang dirujuk dasbor harus ada di berkas provisioning.

Kelas cacat yang ditangkap: uid acak (tanpa `uid:` eksplisit) membuat panel
dasbor kehilangan sumber datanya walau JSON-nya valid.
"""
import json
import pathlib
import re
import sys

repo = pathlib.Path(sys.argv[1])
prov = repo / "infra/observability/grafana/provisioning/datasources/datasources.yml"
dash_dir = repo / "infra/observability/grafana/dashboards"

uid_tersedia = set(re.findall(r"^\s*uid:\s*(\S+)\s*$", prov.read_text(), re.M))
if not uid_tersedia:
    print(f"GAGAL: tidak ada uid eksplisit pada {prov}")
    sys.exit(1)
print("uid sumber data tersedia:", sorted(uid_tersedia))

dirujuk = set()


def telusuri(node):
    if isinstance(node, dict):
        ds = node.get("datasource")
        if isinstance(ds, dict) and ds.get("uid"):
            dirujuk.add(ds["uid"])
        for v in node.values():
            telusuri(v)
    elif isinstance(node, list):
        for v in node:
            telusuri(v)


for berkas in sorted(dash_dir.glob("*.json")):
    telusuri(json.loads(berkas.read_text()))
    print(f"  OK  {berkas.name}")

hilang = dirujuk - uid_tersedia
if hilang:
    print(f"GAGAL: dasbor merujuk uid yang tidak diprovisioning: {sorted(hilang)}")
    sys.exit(1)
print(f"uid dirujuk dasbor: {sorted(dirujuk)} — semuanya tersedia")
