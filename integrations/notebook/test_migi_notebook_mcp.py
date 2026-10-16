import io
import json
import unittest
from unittest.mock import patch

import migi_notebook_mcp as m


class FakeAdapter(m.Adapter):
    def __init__(self):
        self.calls = []

    def request(self, op, args):
        self.calls.append((op, args))
        return {"revision": 1, "body": "Проверено"}, False


class NotebookMCPTests(unittest.TestCase):
    def test_stdio_handshake_tools_notifications_and_errors(self):
        adapter = FakeAdapter()
        messages = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": m.PROTOCOL}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
            {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "notebook_create", "arguments": {"key": "tools/slicer", "title": "Слайсер", "body": "Проверено"}}},
            {"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "notebook_update", "arguments": {"key": "tools/slicer", "changes": {"body": "x"}}}},
            {"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": {"name": "notebook_export"}},
        ]
        source = io.BytesIO(("\n".join(json.dumps(x) for x in messages) + "\ninvalid\n").encode())
        sink = io.StringIO()
        m.serve(adapter, source, sink)
        results = [json.loads(x) for x in sink.getvalue().splitlines()]
        self.assertEqual(len(results), 6)
        self.assertEqual(results[0]["result"]["protocolVersion"], m.PROTOCOL)
        self.assertEqual(len(results[1]["result"]["tools"]), 8)
        self.assertFalse(results[2]["result"]["isError"])
        result = json.loads(results[2]["result"]["content"][0]["text"])
        self.assertEqual(result["operation_id"], adapter.calls[0][1]["operation_id"])
        self.assertEqual(result["result"]["body"], "Проверено")
        self.assertEqual(results[3]["error"]["code"], -32602)
        self.assertEqual(results[4]["error"]["code"], -32602)
        self.assertEqual(results[5]["error"]["code"], -32700)

    def test_uncertain_write_preserves_retry_id(self):
        adapter = FakeAdapter()
        with patch.object(adapter, "request", side_effect=m.MigiClientError("connection lost")):
            result = adapter.call("notebook_delete", {"key": "test/item", "expected_revision": 7, "operation_id": "known-id"})
        data = json.loads(result["content"][0]["text"])
        self.assertTrue(result["isError"])
        self.assertEqual(data["operation_id"], "known-id")
        self.assertEqual(data["result"]["error"]["code"], "transport_error")

    def test_reject_unknown_arguments_before_network(self):
        adapter = FakeAdapter()
        with self.assertRaises(ValueError):
            adapter.call("notebook_read", {"keys": ["test/item"], "owner": True})
        self.assertEqual(adapter.calls, [])

    def test_oversized_frame_drained_and_next_request_works(self):
        source = io.BytesIO(b"x" * (m.MAX_MESSAGE + 10) + b'\n{"jsonrpc":"2.0","id":1,"method":"ping"}\n')
        sink = io.StringIO()
        m.serve(FakeAdapter(), source, sink)
        replies = [json.loads(x) for x in sink.getvalue().splitlines()]
        self.assertEqual(replies[0]["error"]["code"], -32700)
        self.assertEqual(replies[1]["result"], {})


if __name__ == "__main__":
    unittest.main()
