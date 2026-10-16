"""Exercise the installed notebook package outside the checkout."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import ssl
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parent
INSTALLER = ROOT.parent / 'scripts/install-migi-file-exchange-skill'


class NotebookPackageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.skill = self.root / 'migi-notebook'
        shutil.copytree(ROOT / 'migi-notebook', self.skill, ignore=shutil.ignore_patterns('__pycache__'))
        self.env = {**os.environ, 'PYTHONPATH': '', 'MIGI_AGENT_CONFIG': str(self.root / 'agent.json'),
                    'PYTHONDONTWRITEBYTECODE': '1'}

    def cli(self, *args, payload=''):
        return subprocess.run([sys.executable, '-s', str(self.skill / 'scripts/migi-notebook'), *args],
                              input=payload, text=True, capture_output=True, cwd=self.root,
                              env=self.env, timeout=10)

    def test_copied_stdio_and_missing_config(self):
        # No repository or installed package on sys.path; only sibling scripts are needed.
        messages = [{'jsonrpc': '2.0', 'id': 1, 'method': 'initialize'},
                    {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/list'}]
        result = self.cli(payload='\n'.join(map(json.dumps, messages)) + '\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        replies = list(map(json.loads, result.stdout.splitlines()))
        self.assertEqual(replies[0]['result']['serverInfo']['name'], 'migi-notebook')
        self.assertEqual(len(replies[1]['result']['tools']), 8)
        missing = self.cli('--call', 'notebook_list', payload='{}')
        self.assertEqual(missing.returncode, 1, missing.stderr)
        self.assertEqual(json.loads(missing.stdout)['error']['code'], 'transport_error')
        self.assertNotEqual(self.cli('--check-config').returncode, 0)

    def test_selected_install_preserves_existing_and_preflights_selection(self):
        target = self.root / 'installed'
        existing = target / 'migi-file-exchange'
        existing.mkdir(parents=True)
        marker = existing / 'keep'
        marker.write_text('existing user files')
        env = {**self.env, 'MIGI_SKILL_HOME': str(target)}
        def install(*args):
            return subprocess.run([str(INSTALLER), *args], env=env, capture_output=True, text=True, timeout=10)
        for args in [(), ('migi-notebook', 'unknown'), ('migi-notebook', 'migi-notebook')]:
            self.assertNotEqual(install(*args).returncode, 0)
            self.assertFalse((target / 'migi-notebook').exists())
        result = install('migi-notebook')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(marker.read_text(), 'existing user files')
        self.assertFalse((target / 'migi-notebook').is_symlink())
        self.assertNotEqual(install('migi-notebook').returncode, 0)
        shutil.rmtree(target / 'migi-notebook')
        (target / 'migi-notebook').symlink_to(target / 'missing')
        self.assertNotEqual(install('migi-notebook').returncode, 0)
        self.assertTrue((target / 'migi-notebook').is_symlink())

    def test_copied_https_retry_conflict_and_bad_pin(self):
        cert, key = self.root / 'cert.pem', self.root / 'key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                        '-subj', '/CN=localhost', '-keyout', str(key), '-out', str(cert)],
                       check=True, capture_output=True, timeout=20)
        requests = []
        token = 'migi_at_notebook_test_secret'
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def handle(self):
                try:
                    super().handle()
                except ConnectionError:
                    # A rejected certificate closes TLS before sending HTTP.
                    pass
            def do_POST(self):
                body = self.rfile.read(int(self.headers['Content-Length']))
                requests.append((self.path, self.headers.get('Authorization'), body))
                if len(requests) == 1:
                    # Simulate a committed write whose response was lost.
                    self.close_connection = True
                    return
                if self.path.endswith('/update'):
                    status, data = 409, {'error': {'code': 'revision_conflict', 'current_revision': 2}}
                else:
                    status, data = 200, {'key': 'test/item', 'body': 'Проверено', 'revision': 1}
                payload = json.dumps(data, ensure_ascii=False).encode()
                self.send_response(status)
                self.send_header('Content-Length', str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert, key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        def stop():
            server.shutdown()
            server.server_close()
            thread.join()
        self.addCleanup(stop)
        config = {'endpoint': f'https://127.0.0.1:{server.server_port}/v1/agent-events', 'token': token,
                  'tls_fingerprint': hashlib.sha256(ssl.PEM_cert_to_DER_cert(cert.read_text())).hexdigest()}
        path = self.root / 'agent.json'
        path.write_text(json.dumps(config))
        path.chmod(0o600)
        checked = self.cli('--check-config')
        self.assertEqual(checked.returncode, 0, checked.stderr)
        self.assertNotIn(token, checked.stdout + checked.stderr)
        self.assertEqual(requests, [])
        result = self.cli('--call', 'notebook_create', payload=json.dumps(
            {'key': 'test/item', 'title': 'Тест', 'body': 'Проверено'}))
        self.assertEqual(result.returncode, 0, result.stderr)
        created = json.loads(result.stdout)
        self.assertEqual(created['result']['body'], 'Проверено')
        self.assertEqual(len(requests), 2)
        self.assertEqual(requests[0], requests[1])
        self.assertEqual(requests[0][1], 'Bearer ' + token)
        self.assertEqual(json.loads(requests[0][2])['operation_id'], created['operation_id'])
        conflict = self.cli('--call', 'notebook_update', payload=json.dumps(
            {'key': 'test/item', 'expected_revision': 1, 'changes': {'body': 'changed'}}))
        self.assertEqual(conflict.returncode, 1)
        self.assertEqual(json.loads(conflict.stdout)['result']['error']['code'], 'revision_conflict')
        self.assertEqual(len(requests), 3, 'HTTP conflicts must not trigger automatic retries')
        config['tls_fingerprint'] = '00' * 32
        path.write_text(json.dumps(config))
        bad = self.cli('--call', 'notebook_list', payload='{}')
        self.assertEqual(bad.returncode, 1)
        self.assertIn('fingerprint mismatch', bad.stdout)
        self.assertNotIn(token, bad.stdout + bad.stderr)
        self.assertEqual(len(requests), 3, 'pin must be checked before sending any request')


if __name__ == '__main__':
    unittest.main()
