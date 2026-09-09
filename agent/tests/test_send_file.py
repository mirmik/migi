import asyncio
from contextlib import contextmanager
from io import BytesIO
import json
import os

from migi_agent.service import create_app, SERVICE, register_migi_tools
from nemor.core import ToolRegistry
from test_service import payload, response


class UploadClient:
    def __init__(self):
        self.sent = []
        self.status = 201
        self.result = None

    @contextmanager
    def open(self, method, path, body, headers):
        assert method == 'POST' and path == '/v1/files'
        assert not isinstance(body, bytes)  # stream, not a whole-file memory copy
        content = body.read()
        self.sent.append((content, headers))
        result = self.result if self.result is not None else {
            'id': 'a' * 32, 'name': 'записка.txt', 'size': len(content),
            'expires_at': '2026-09-10T00:00:00Z'}
        r = BytesIO(json.dumps(result).encode())
        r.status, r.reason = self.status, 'test'
        yield r


def test_agent_creates_and_sends_file(tmp_path):
    client = UploadClient()
    text = 'Файл, созданный агентом.\nПроверка UTF-8.'
    def model(messages, **kwargs):
        if messages[-1]['role'] == 'user':
            name, args = 'write_file', {'path': 'записка.txt', 'content': text}
        elif messages[-1].get('tool_call_id') == 'write_file':
            name, args = 'migi_send_file', {'path': 'записка.txt'}
        else:
            value = json.loads(messages[-1]['content'])
            assert value['uploaded'] and value['id'] == 'a' * 32
            return response('Отправлено')
        return response(calls=[{'id': name, 'type': 'function', 'function': {
            'name': name, 'arguments': json.dumps(args)}}])
    async def scenario():
        app = create_app(tmp_path / 'state', tmp_path, completion=model, client=client)
        app[SERVICE].submit(payload('upload'))
        await app[SERVICE].wait_for_idle()
        assert app[SERVICE].status('device:phone', 'upload')['status'] == 'completed'
        assert client.sent[0][0] == text.encode()
        headers = client.sent[0][1]
        assert headers['Content-Type'] == 'text/plain'
        assert headers['Content-Length'] == str(len(text.encode()))
        assert headers['X-Migi-Filename'] == 'записка.txt'.encode()
        app[SERVICE].submit(payload('upload'))
        assert len(client.sent) == 1
    asyncio.run(scenario())


def test_upload_binary_and_failures(tmp_path):
    client = UploadClient()
    registry = ToolRegistry()
    register_migi_tools(registry, client)
    send = registry.get_tools({})['migi_send_file']['func']
    config = {'cwd': str(tmp_path)}
    binary = tmp_path / 'test.bin'
    binary.write_bytes(bytes(range(256)))
    assert json.loads(send({'path': str(binary), 'mime_type': 'application/octet-stream'}, config))['uploaded']
    assert client.sent[0][0] == binary.read_bytes()
    (tmp_path / 'empty').touch()
    os.mkfifo(tmp_path / 'pipe')
    for path in ('missing', 'empty', '.', 'pipe'):
        assert 'error' in json.loads(send({'path': path}, config))
    assert 'error' in json.loads(send({'path': 'test.bin', 'mime_type': 'x\r\nInjected: y'}, config))
    assert len(client.sent) == 1
    client.status = 503
    assert 'uploaded' not in json.loads(send({'path': 'test.bin'}, config))
    client.status, client.result = 201, {}
    assert 'error' in json.loads(send({'path': 'test.bin'}, config))
