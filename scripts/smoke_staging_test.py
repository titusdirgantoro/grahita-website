#!/usr/bin/env python3
import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "smoke-staging.sh"


FAKE_CURL = r'''#!/usr/bin/env python3
import json
import os
import pathlib
import sys

args = sys.argv[1:]
out = None
headers = None
jar = None
for index, arg in enumerate(args):
    if arg == '-o' and index + 1 < len(args):
        out = args[index + 1]
    elif arg == '-D' and index + 1 < len(args):
        headers = args[index + 1]
    elif arg == '-c' and index + 1 < len(args):
        jar = args[index + 1]
urls = [arg for arg in args if arg.startswith(('http://', 'https://'))]
url = urls[-1] if urls else ''
state_path = pathlib.Path(os.environ['FAKE_CURL_STATE'])
try:
    state = int(state_path.read_text())
except (FileNotFoundError, ValueError):
    state = 0
status = 200
body = b''
response_headers = ''

if url.endswith('/'):
    body = b'<html><div id="root"></div></html>'
    response_headers = ''.join([
        'content-security-policy: default-src self\n',
        'strict-transport-security: max-age=31536000\n',
        'x-content-type-options: nosniff\n',
        'x-frame-options: DENY\n',
    ])
elif url.endswith('/api/v1/meta'):
    body = json.dumps({'data': {'env': 'test'}}).encode()
elif url.endswith('/api/v1/auth/login'):
    role = 'superadmin' if '/admin/' in url else 'branch'
    body = json.dumps({'data': {'user': {'role': role}}}).encode()
    response_headers = 'set-cookie: grahita_session=session; Path=/; HttpOnly; Secure; SameSite=Lax\n'
    if jar:
        pathlib.Path(jar).write_text('.example\tFALSE\t/\tTRUE\t0\tgrahita_session\tsession\n.example\tFALSE\t/\tTRUE\t0\tgrahita_csrf\tcsrf\n')
elif url.endswith('/api/v1/catalog'):
    body = json.dumps({'data': [{'id': 'product-1', 'jenis': 'reguler', 'harga': 100000}]}).encode()
elif url.endswith('/api/v1/orders'):
    status = 201
    body = json.dumps({'data': {'id': 'order-1', 'total': 200000}}).encode()
elif url.endswith('/api/v1/orders/order-1/submit'):
    body = json.dumps({'data': {'status': 'submitted', 'nomor': 'UJI-001'}}).encode()
elif url.endswith('/api/v1/auth/me') and '/admin/' in url:
    status = 401
    body = json.dumps({'error': {'code': 'UNAUTHORIZED'}}).encode()
elif url.endswith('/api/v1/admin/orders/order-1/verify'):
    body = json.dumps({'data': {'order': {'status': 'done'}, 'verifikasi': {'hasil': 'cocok'}}}).encode()
elif url.endswith('/api/v1/orders/order-1/documents'):
    state += 1
    state_path.write_text(str(state))
    docs = [] if state == 1 else [{'id': 'document-1', 'jenis': 'invoice_pdf'}]
    body = json.dumps({'data': docs}).encode()
elif url.endswith('/api/v1/documents/document-1/download'):
    body = json.dumps({'data': {'url': 'https://objects.example/document-1?signature=secret'}}).encode()
elif url.startswith('https://objects.example/'):
    body = b'x' * 2048
else:
    status = 404
    body = json.dumps({'error': {'code': 'NOT_FOUND'}}).encode()

if out and out != '/dev/null':
    pathlib.Path(out).write_bytes(body)
if headers:
    pathlib.Path(headers).write_text(response_headers)
if not out:
    sys.stdout.buffer.write(body)
if any(arg == '-w' for arg in args):
    sys.stdout.write(f'\n{status}')
'''


class SmokeStagingTest(unittest.TestCase):
    def test_menunggu_dokumen_dan_mengunduh_presigned_url(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            tmp = pathlib.Path(raw_tmp)
            fake_curl = tmp / "curl"
            fake_curl.write_text(FAKE_CURL)
            fake_curl.chmod(0o755)
            env = os.environ.copy()
            env.update({
                "PATH": f"{tmp}:{env['PATH']}",
                "FAKE_CURL_STATE": str(tmp / "state"),
                "CUSTOMER_BASE": "https://branch.example/branch",
                "ADMIN_BASE": "https://admin.example/admin",
                "SMOKE_EMAIL": "uji@example.invalid",
                "SMOKE_PASSWORD": "not-a-secret",
                "SMOKE_ADMIN_EMAIL": "admin@example.invalid",
                "SMOKE_ADMIN_PASSWORD": "not-a-secret",
                "SMOKE_DOCUMENT_WAIT_SECONDS": "2",
                "SMOKE_DOCUMENT_POLL_SECONDS": "1",
            })
            result = subprocess.run(
                ["sh", str(SCRIPT)],
                cwd=ROOT,
                env=env,
                text=True,
                capture_output=True,
                timeout=20,
                check=False,
            )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("dokumen tersedia setelah menunggu 1s", result.stdout)
        self.assertIn("unduh dokumen 200 (2048 byte, invoice_pdf)", result.stdout)
        self.assertIn("HASIL: LULUS", result.stdout)
        self.assertNotIn("signature=secret", result.stdout)


if __name__ == "__main__":
    unittest.main()
