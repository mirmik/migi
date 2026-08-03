import hashlib
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import migi_codex_hook as hook
import configure_notify
import provision_local


class HookTests(unittest.TestCase):
    def test_maps_supported_lifecycle_events(self):
        completed = hook.notification_for({"hook_event_name": "Stop", "cwd": "/work/migi"})
        self.assertEqual(completed.kind, "agent.completed")
        self.assertIn("migi", completed.title)

        attention = hook.notification_for(
            {"hook_event_name": "PermissionRequest", "cwd": "/work/migi", "tool_name": "Bash"}
        )
        self.assertEqual(attention.kind, "agent.attention_required")
        self.assertIn("Bash", attention.body)
        self.assertIsNone(hook.notification_for({"hook_event_name": "SessionStart"}))

    def test_failure_output_matches_event_schema(self):
        self.assertNotIn("continue", hook.failure_output("PermissionRequest"))
        self.assertTrue(hook.failure_output("Stop")["continue"])

    def test_maps_codex_notify_message(self):
        message = hook.agent_message_for(
            {
                "type": "agent-turn-complete",
                "thread-id": "thread-1",
                "turn-id": "turn-1",
                "cwd": "/work/migi",
                "last-assistant-message": "Euler: $e^{i\\pi}+1=0$",
            }
        )
        self.assertIsNotNone(message)
        self.assertEqual(message.turn_id, "turn-1")
        self.assertIn("$e^{i\\pi}+1=0$", message.body)
        self.assertIsNone(hook.agent_message_for({"type": "other"}))

    def test_requires_private_config_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "agent.json"
            path.write_text(
                json.dumps(
                    {
                        "endpoint": "https://127.0.0.1:8790/v1/agent-events",
                        "token": "migi_at_test_secret",
                        "tls_fingerprint": "aa" * 32,
                    }
                ),
                encoding="utf-8",
            )
            os.chmod(path, 0o644)
            with self.assertRaisesRegex(ValueError, "permissions"):
                hook.load_config(path)
            os.chmod(path, 0o600)
            config = hook.load_config(path)
            self.assertEqual(config.endpoint.port, 8790)

    def test_checks_pin_before_sending_bearer_token(self):
        certificate = b"test certificate"
        config = hook.ClientConfig(
            endpoint=hook.urlsplit("https://127.0.0.1:8790/v1/agent-events"),
            token="migi_at_test_secret",
            fingerprint=hashlib.sha256(certificate).hexdigest(),
        )
        notification = hook.Notification("agent.completed", "Done", "Finished")
        connection = mock.Mock()
        connection.sock.getpeercert.return_value = certificate
        connection.getresponse.return_value.status = 201
        connection.getresponse.return_value.read.return_value = b"{}"
        with mock.patch.object(hook.http.client, "HTTPSConnection", return_value=connection):
            hook.send_notification(config, notification)
        connection.request.assert_called_once()
        headers = connection.request.call_args.kwargs["headers"]
        self.assertEqual(headers["Authorization"], "Bearer migi_at_test_secret")

        connection.reset_mock()
        connection.sock.getpeercert.return_value = b"wrong certificate"
        with mock.patch.object(hook.http.client, "HTTPSConnection", return_value=connection):
            with self.assertRaisesRegex(RuntimeError, "fingerprint mismatch"):
                hook.send_notification(config, notification)
        connection.request.assert_not_called()

    def test_sends_agent_message_to_dedicated_endpoint(self):
        certificate = b"test certificate"
        config = hook.ClientConfig(
            endpoint=hook.urlsplit("https://127.0.0.1:8790/v1/agent-events"),
            token="migi_at_test_secret",
            fingerprint=hashlib.sha256(certificate).hexdigest(),
        )
        message = hook.AgentMessage("thread", "turn", "/work/migi", "Answer", "Body")
        connection = mock.Mock()
        connection.sock.getpeercert.return_value = certificate
        connection.getresponse.return_value.status = 201
        connection.getresponse.return_value.read.return_value = b"{}"
        with mock.patch.object(hook.http.client, "HTTPSConnection", return_value=connection):
            hook.send_agent_message(config, message)
        self.assertEqual(connection.request.call_args.args[1], "/v1/agent-messages")

    def test_provisioner_extracts_and_writes_private_config(self):
        parser = provision_local.CredentialConfigParser()
        parser.feed(
            '<div class="credential"><pre>{&#34;endpoint&#34;:&#34;https://127.0.0.1:8790/v1/agent-events&#34;,'
            '&#34;token&#34;:&#34;migi_at_test_secret&#34;,&#34;tls_fingerprint&#34;:&#34;aa&#34;}</pre></div>'
        )
        config = parser.config()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "migi" / "agent.json"
            provision_local.write_private_config(path, config)
            self.assertEqual(stat_mode(path), 0o600)
            self.assertEqual(json.loads(path.read_text(encoding="utf-8")), config)

    def test_configures_top_level_notify_without_losing_config(self):
        source = 'model = "gpt-test"\n\n[projects."/work"]\ntrust_level = "trusted"\n'
        updated = configure_notify.configured_text(source, "/home/test/migi-codex-hook")
        self.assertIn('notify = ["/home/test/migi-codex-hook"]', updated)
        self.assertIn('[projects."/work"]', updated)
        replaced = configure_notify.configured_text(updated, "/new/hook")
        self.assertEqual(replaced.count("notify ="), 1)
        self.assertIn('notify = ["/new/hook"]', replaced)


def stat_mode(path: Path) -> int:
    return os.stat(path).st_mode & 0o777


if __name__ == "__main__":
    unittest.main()
