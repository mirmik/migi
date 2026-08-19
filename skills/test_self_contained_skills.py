from __future__ import annotations

import contextlib
import hashlib
import importlib.machinery
import importlib.util
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from email import policy
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from types import ModuleType
from urllib.parse import parse_qs, urlsplit
from unittest import mock


ROOT = Path(__file__).resolve().parent
REPOSITORY = ROOT.parent
FILE_SKILL = ROOT / "migi-file-exchange"
AUDIO_SKILL = ROOT / "migi-audio-player"
PUBLISHER_SKILL = ROOT / "migi-android-publisher"
FILE_ID = "0123456789abcdef0123456789abcdef"
SECOND_ID = "fedcba9876543210fedcba9876543210"
COVER_ID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
PLAYLIST_ID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
EXPIRES = "2026-08-20T12:00:00Z"


def load_script(name: str, path: Path) -> ModuleType:
    loader = importlib.machinery.SourceFileLoader(name, str(path))
    spec = importlib.util.spec_from_loader(name, loader)
    if spec is None:
        raise RuntimeError(f"could not load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    sys.path.insert(0, str(path.parent))
    try:
        loader.exec_module(module)
    except Exception:
        sys.modules.pop(name, None)
        raise
    finally:
        sys.path.pop(0)
    return module


def decode_header(value: str) -> str:
    return value.encode("latin-1").decode("utf-8")


class MigiFixtureHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _format: str, *_args: object) -> None:
        return

    @property
    def state(self) -> dict:
        return self.server.state  # type: ignore[attr-defined]

    def send_json(self, status: int, payload: object) -> None:
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)

    def request_body(self) -> bytes:
        length = int(self.headers.get("Content-Length", "0"))
        return self.rfile.read(length)

    def do_GET(self) -> None:
        parsed = urlsplit(self.path)
        if parsed.path == "/v1/files":
            files = [
                {
                    "id": FILE_ID,
                    "name": "phone.txt",
                    "size": len(self.state["file_content"]),
                    "source": "phone",
                    "expires_at": EXPIRES,
                }
            ]
            self.send_json(200, None if self.state.get("files_null") else files)
            return
        if parsed.path == f"/v1/files/{FILE_ID}":
            self.send_json(200, {"id": FILE_ID, "name": "phone.txt"})
            return
        if parsed.path == f"/v1/files/{FILE_ID}/content":
            content = self.state["file_content"]
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(content)))
            digest = (
                "a" * 64
                if self.state.get("bad_file_digest")
                else hashlib.sha256(content).hexdigest()
            )
            self.send_header("X-Content-SHA256", digest)
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(content)
            return
        if parsed.path == "/v1/media":
            query = parse_qs(parsed.query).get("q", [""])[0].casefold()
            self.state["media_queries"].append(query)
            items = self.state["media"]
            if query:
                items = [
                    item
                    for item in items
                    if all(
                        term
                        in "\n".join(
                            str(item.get(field, ""))
                            for field in ("name", "title", "artist", "source")
                        ).casefold()
                        for term in query.split()
                    )
                ]
            self.send_json(200, items)
            return
        if parsed.path == "/v1/media/origin/requests":
            if self.state["origin_jobs"]:
                self.send_json(200, self.state["origin_jobs"].pop(0))
            else:
                self.send_response(204)
                self.send_header("Content-Length", "0")
                self.send_header("Connection", "close")
                self.end_headers()
            return
        if parsed.path == "/v1/playlists":
            self.send_json(200, self.state["playlists"])
            return
        self.send_error(404)

    def do_POST(self) -> None:
        body = self.request_body()
        if self.path == "/v1/files":
            name = decode_header(self.headers["X-Migi-Filename"])
            self.state["file_uploads"].append(
                {
                    "name": name,
                    "type": self.headers["Content-Type"],
                    "source": self.headers.get("X-Migi-Source", ""),
                    "body": body,
                }
            )
            self.send_json(
                201,
                {"id": FILE_ID, "name": name, "size": len(body), "expires_at": EXPIRES},
            )
            return
        if self.path == "/v1/media":
            name = decode_header(self.headers["X-Migi-Filename"])
            ids = (COVER_ID, FILE_ID, SECOND_ID)
            media_id = ids[len(self.state["media_uploads"])]
            item = {
                "id": media_id,
                "name": name,
                "title": decode_header(self.headers.get("X-Migi-Title", name)),
                "artist": decode_header(self.headers["X-Migi-Artist"])
                if self.headers.get("X-Migi-Artist")
                else "",
                "size": len(body),
                "expires_at": EXPIRES,
            }
            self.state["media_uploads"].append(
                {"headers": dict(self.headers), "body": body, "item": item}
            )
            self.state["media"].append(item)
            self.send_json(201, item)
            return
        if self.path == "/v1/media/origin":
            payload = json.loads(body)
            self.state["origin_media_manifests"].append(payload)
            ids = (COVER_ID, FILE_ID, SECOND_ID)
            result = []
            for index, source in enumerate(payload["items"]):
                result.append(
                    {
                        "id": ids[index],
                        "name": source["name"],
                        "title": source.get("title", Path(source["name"]).stem),
                        "artist": source.get("artist", ""),
                        "mime": source["mime"],
                        "size": source["size"],
                        "sha256": source["sha256"],
                    }
                )
            self.send_json(201, result)
            return
        if self.path == "/v1/playback/queue":
            payload = json.loads(body)
            self.state["queues"].append(payload)
            self.send_json(201, {"id": 42})
            return
        if self.path == "/v1/playlists":
            payload = json.loads(body)
            item = {
                "id": PLAYLIST_ID,
                "name": payload["name"],
                "artwork_media_id": payload.get("artwork_media_id", ""),
                "media_ids": payload["media_ids"],
                "source": "test-agent",
                "created_at": EXPIRES,
                "updated_at": EXPIRES,
            }
            self.state["playlist_saves"].append(payload)
            self.state["playlists"].append(item)
            self.send_json(201, item)
            return
        if self.path == f"/v1/playlists/{PLAYLIST_ID}/queue":
            payload = json.loads(body)
            self.state["playlist_starts"].append(payload)
            self.send_json(201, {"id": 43})
            return
        if self.path == "/v1/releases":
            message = BytesParser(policy=policy.default).parsebytes(
                (
                    "Content-Type: "
                    + self.headers["Content-Type"]
                    + "\r\nMIME-Version: 1.0\r\n\r\n"
                ).encode("ascii")
                + body
            )
            parts = list(message.iter_parts())
            self.state["release"] = {
                "metadata": json.loads(parts[0].get_payload(decode=True)),
                "apk": parts[1].get_payload(decode=True),
                "idempotency": self.headers["Idempotency-Key"],
            }
            self.send_json(
                201,
                {
                    "artifact_id": "release-1",
                    "package_name": "dev.migi.test",
                    "version_code": 1,
                },
            )
            return
        self.send_error(404)

    def do_DELETE(self) -> None:
        if self.path == f"/v1/playlists/{PLAYLIST_ID}":
            self.state["playlist_deletes"].append(PLAYLIST_ID)
            self.state["playlists"] = []
            self.send_response(204)
            self.send_header("Content-Length", "0")
            self.send_header("Connection", "close")
            self.end_headers()
            return
        self.send_error(404)

    def do_PUT(self) -> None:
        body = self.request_body()
        prefix = "/v1/media/origin/requests/"
        if self.path.startswith(prefix):
            self.state["origin_uploads"].append(
                {
                    "request_id": self.path[len(prefix) :],
                    "type": self.headers.get("Content-Type", ""),
                    "body": body,
                }
            )
            self.send_response(204)
            self.send_header("Content-Length", "0")
            self.send_header("Connection", "close")
            self.end_headers()
            return
        self.send_error(404)


class SkillClientTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.state = {
            "file_content": b"phone screenshot",
            "file_uploads": [],
            "media": [],
            "media_queries": [],
            "media_uploads": [],
            "origin_media_manifests": [],
            "origin_jobs": [],
            "origin_uploads": [],
            "queues": [],
            "playlists": [],
            "playlist_saves": [],
            "playlist_starts": [],
            "playlist_deletes": [],
            "release": None,
        }
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), MigiFixtureHandler)
        self.server.state = self.state  # type: ignore[attr-defined]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.endpoint = f"http://127.0.0.1:{self.server.server_port}"
        self.environment = os.environ.copy()
        self.environment.pop("MIGI_AGENT_CONFIG", None)
        self.environment.pop("MIGI_PUBLISHER_CONFIG", None)
        self.environment["XDG_CONFIG_HOME"] = str(self.root / "empty-config")

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        self.temporary.cleanup()

    def copied_skill(self, source: Path) -> Path:
        target = self.root / source.name
        shutil.copytree(source, target)
        return target

    def run_client(self, script: Path, *arguments: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(script), *arguments],
            cwd=self.root,
            env=self.environment,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=10,
            check=False,
        )

    def test_file_skill_runs_from_copy_without_repository_or_installed_cli(self) -> None:
        skill = self.copied_skill(FILE_SKILL)
        source = self.root / "ответ.txt"
        source.write_bytes(b"portable file")
        uploaded = self.run_client(
            skill / "scripts/migi-file",
            "-endpoint",
            self.endpoint,
            "-source",
            "nemor",
            "put",
            str(source),
        )
        self.assertEqual(uploaded.returncode, 0, uploaded.stderr)
        self.assertIn(FILE_ID, uploaded.stdout)
        self.assertEqual(self.state["file_uploads"][0]["name"], "ответ.txt")
        self.assertEqual(self.state["file_uploads"][0]["body"], b"portable file")

        listed = self.run_client(skill / "scripts/migi-file", "-endpoint", self.endpoint, "list")
        self.assertEqual(listed.returncode, 0, listed.stderr)
        self.assertIn("phone.txt", listed.stdout)

        self.state["files_null"] = True
        empty = self.run_client(skill / "scripts/migi-file", "-endpoint", self.endpoint, "list")
        self.assertEqual(empty.returncode, 0, empty.stderr)
        self.assertEqual(empty.stdout, "")
        self.state["files_null"] = False

        destination = self.root / "downloaded.txt"
        downloaded = self.run_client(
            skill / "scripts/migi-file",
            "-endpoint",
            self.endpoint,
            "-output",
            str(destination),
            "get",
            FILE_ID,
        )
        self.assertEqual(downloaded.returncode, 0, downloaded.stderr)
        self.assertEqual(destination.read_bytes(), self.state["file_content"])

        self.state["bad_file_digest"] = True
        rejected_destination = self.root / "bad-download.txt"
        rejected = self.run_client(
            skill / "scripts/migi-file",
            "-endpoint",
            self.endpoint,
            "-output",
            str(rejected_destination),
            "get",
            FILE_ID,
        )
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn("digest does not match", rejected.stderr)
        self.assertFalse(rejected_destination.exists())

    def test_audio_skill_uploads_artwork_tracks_and_one_queue_from_copy(self) -> None:
        skill = self.copied_skill(AUDIO_SKILL)
        cover = self.root / "cover.png"
        first = self.root / "01. First.mp3"
        second = self.root / "02. Second.opus"
        cover.write_bytes(b"png")
        first.write_bytes(b"mp3")
        second.write_bytes(b"opus")
        result = self.run_client(
            skill / "scripts/migi-play",
            "-endpoint",
            self.endpoint,
            "-name",
            "Portable",
            "-cover",
            str(cover),
            "play",
            str(first),
            str(second),
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("queued event 42", result.stdout)
        self.assertEqual(len(self.state["media_uploads"]), 3)
        self.assertEqual(
            self.state["queues"],
            [
                {
                    "name": "Portable",
                    "media_ids": [FILE_ID, SECOND_ID],
                    "artwork_media_id": COVER_ID,
                }
            ],
        )

    def test_audio_skill_registers_one_lazy_manifest_without_uploading_bytes(self) -> None:
        skill = self.copied_skill(AUDIO_SKILL)
        player = load_script("portable_migi_audio", skill / "scripts/migi-play")
        transport = sys.modules["_migi_transport"]
        client = transport.MigiHTTPClient(
            transport.urlsplit(self.endpoint), token="migi_at_test_origin"
        )
        cover = self.root / "cover.png"
        first = self.root / "01. First.flac"
        second = self.root / "02. Second.opus"
        cover.write_bytes(b"png cover")
        first.write_bytes(b"first flac bytes")
        second.write_bytes(b"second opus bytes")
        registry = self.root / "media-origin.json"
        output = io.StringIO()
        with (
            mock.patch.dict(os.environ, {"MIGI_ORIGIN_REGISTRY": str(registry)}),
            mock.patch.object(player, "resolve_agent_client", return_value=client),
            contextlib.redirect_stdout(output),
        ):
            result = player.run(
                [
                    "-name",
                    "Lazy portable",
                    "-cover",
                    str(cover),
                    "--lazy",
                    "play",
                    str(first),
                    str(second),
                ]
            )
        self.assertEqual(result, 0)
        self.assertIn("queued event 42", output.getvalue())
        self.assertEqual(self.state["media_uploads"], [])
        self.assertEqual(len(self.state["origin_media_manifests"]), 1)
        items = self.state["origin_media_manifests"][0]["items"]
        self.assertEqual(
            [item["name"] for item in items],
            ["cover.png", "01. First.flac", "02. Second.opus"],
        )
        self.assertTrue(all("path" not in item for item in items))
        self.assertEqual(
            self.state["queues"],
            [
                {
                    "name": "Lazy portable",
                    "media_ids": [FILE_ID, SECOND_ID],
                    "artwork_media_id": COVER_ID,
                }
            ],
        )
        saved = json.loads(registry.read_text(encoding="utf-8"))
        self.assertEqual(saved["entries"][FILE_ID]["path"], str(first.resolve()))
        self.assertEqual(registry.stat().st_mode & 0o077, 0)

        request_id = "b" * 32
        self.state["origin_jobs"].append(
            {
                "id": request_id,
                "media_id": FILE_ID,
                "name": first.name,
                "mime": "audio/flac",
                "size": first.stat().st_size,
                "sha256": hashlib.sha256(first.read_bytes()).hexdigest(),
                "created_at": EXPIRES,
            }
        )
        origin = load_script("portable_migi_origin", skill / "scripts/migi-origin")
        self.assertEqual(origin.serve(client, registry, once=True), 0)
        self.assertEqual(
            self.state["origin_uploads"],
            [
                {
                    "request_id": request_id,
                    "type": "audio/flac",
                    "body": first.read_bytes(),
                }
            ],
        )

    def test_audio_skill_indexes_origin_without_publishing_queue(self) -> None:
        skill = self.copied_skill(AUDIO_SKILL)
        player = load_script("portable_migi_index", skill / "scripts/migi-play")
        transport = sys.modules["_migi_transport"]
        client = transport.MigiHTTPClient(
            transport.urlsplit(self.endpoint), token="migi_at_test_origin"
        )
        first = self.root / "01. Indexed.flac"
        second = self.root / "02. Indexed.opus"
        first.write_bytes(b"indexed flac")
        second.write_bytes(b"indexed opus")
        registry = self.root / "indexed-origin.json"
        with (
            mock.patch.dict(os.environ, {"MIGI_ORIGIN_REGISTRY": str(registry)}),
            mock.patch.object(player, "resolve_agent_client", return_value=client),
            contextlib.redirect_stdout(io.StringIO()),
        ):
            result = player.run(["index", str(first), str(second)])
        self.assertEqual(result, 0)
        self.assertEqual(self.state["queues"], [])
        self.assertEqual(self.state["media_uploads"], [])
        self.assertEqual(
            [item["name"] for item in self.state["origin_media_manifests"][0]["items"]],
            [first.name, second.name],
        )
        saved = json.loads(registry.read_text(encoding="utf-8"))
        self.assertEqual(len(saved["entries"]), 2)

    def test_audio_skill_searches_and_reuses_saved_playlist(self) -> None:
        skill = self.copied_skill(AUDIO_SKILL)
        self.state["media"] = [
            {
                "id": FILE_ID,
                "name": "01. Weight.flac",
                "title": "Weight of the World",
                "artist": "NieR",
                "source": "agent:origin",
                "size": 123,
                "expires_at": EXPIRES,
            }
        ]
        searched = self.run_client(
            skill / "scripts/migi-play",
            "-endpoint",
            self.endpoint,
            "search",
            "weight",
            "world",
        )
        self.assertEqual(searched.returncode, 0, searched.stderr)
        self.assertIn(FILE_ID, searched.stdout)
        self.assertEqual(self.state["media_queries"][-1], "weight world")

        saved = self.run_client(
            skill / "scripts/migi-play",
            "-endpoint",
            self.endpoint,
            "-name",
            "Reusable album",
            "-artwork-id",
            COVER_ID,
            "save",
            FILE_ID,
            SECOND_ID,
        )
        self.assertEqual(saved.returncode, 0, saved.stderr)
        self.assertIn(PLAYLIST_ID, saved.stdout)
        self.assertEqual(
            self.state["playlist_saves"],
            [
                {
                    "name": "Reusable album",
                    "media_ids": [FILE_ID, SECOND_ID],
                    "artwork_media_id": COVER_ID,
                }
            ],
        )

        listed = self.run_client(
            skill / "scripts/migi-play", "-endpoint", self.endpoint, "playlists"
        )
        self.assertEqual(listed.returncode, 0, listed.stderr)
        self.assertIn("Reusable album", listed.stdout)

        started = self.run_client(
            skill / "scripts/migi-play",
            "-endpoint",
            self.endpoint,
            "-device",
            "phone-1",
            "start",
            PLAYLIST_ID,
        )
        self.assertEqual(started.returncode, 0, started.stderr)
        self.assertIn("queued saved playlist event 43", started.stdout)
        self.assertEqual(self.state["playlist_starts"], [{"device_id": "phone-1"}])

        forgotten = self.run_client(
            skill / "scripts/migi-play",
            "-endpoint",
            self.endpoint,
            "forget",
            PLAYLIST_ID,
        )
        self.assertEqual(forgotten.returncode, 0, forgotten.stderr)
        self.assertEqual(self.state["playlist_deletes"], [PLAYLIST_ID])

    def test_album_helper_uses_bundled_player_and_natural_order(self) -> None:
        skill = self.copied_skill(AUDIO_SKILL)
        album = self.root / "Album"
        album.mkdir()
        (album / "10. Last.mp3").write_bytes(b"last")
        (album / "2. First.mp3").write_bytes(b"first")
        result = self.run_client(
            skill / "scripts/migi-album",
            "--endpoint",
            self.endpoint,
            "--no-cover",
            str(album),
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        names = [item["item"]["name"] for item in self.state["media_uploads"]]
        self.assertEqual(names, ["2. First.mp3", "10. Last.mp3"])
        self.assertEqual(self.state["queues"][0]["media_ids"], [COVER_ID, FILE_ID])

    def test_album_helper_index_only_uses_origin_catalog_command(self) -> None:
        skill = self.copied_skill(AUDIO_SKILL)
        album_helper = load_script("portable_migi_album_index", skill / "scripts/migi-album")
        album = self.root / "Indexed Album"
        album.mkdir()
        first = album / "2. First.flac"
        second = album / "10. Last.opus"
        first.write_bytes(b"first")
        second.write_bytes(b"last")
        with mock.patch.object(album_helper.os, "execv") as execv:
            result = album_helper.run(
                ["--config", "origin.json", "--index-only", "--no-cover", str(album)]
            )
        self.assertEqual(result, 0)
        command = execv.call_args.args[1]
        self.assertIn("index", command)
        self.assertNotIn("play", command)
        self.assertEqual(command[-2:], [str(first), str(second)])

    def test_publisher_streams_multipart_with_content_digest_idempotency(self) -> None:
        skill = self.copied_skill(PUBLISHER_SKILL)
        publisher = load_script("portable_migi_publisher", skill / "scripts/migi-publish-app")
        transport = sys.modules["_migi_transport"]
        apk = self.root / "app.apk"
        apk.write_bytes(b"signed apk bytes")
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            publisher.publish(
                transport.trusted_local_client(self.endpoint),
                "/v1/releases",
                str(apk),
                notes="Portable release",
                source_revision="abc123",
            )
        self.assertIn("release-1", output.getvalue())
        self.assertEqual(self.state["release"]["apk"], b"signed apk bytes")
        self.assertEqual(
            self.state["release"]["metadata"],
            {"release_notes": "Portable release", "source_revision": "abc123"},
        )
        self.assertEqual(
            self.state["release"]["idempotency"],
            hashlib.sha256(b"signed apk bytes").hexdigest(),
        )


class SkillPackageTests(unittest.TestCase):
    def test_every_skill_bundles_the_same_transport(self) -> None:
        copies = [
            (skill / "scripts/_migi_transport.py").read_bytes()
            for skill in (FILE_SKILL, AUDIO_SKILL, PUBLISHER_SKILL)
        ]
        self.assertEqual(copies[0], copies[1])
        self.assertEqual(copies[1], copies[2])

    def test_transport_checks_certificate_before_bearer_token(self) -> None:
        transport = load_script(
            "portable_migi_transport",
            FILE_SKILL / "scripts/_migi_transport.py",
        )
        certificate = b"leaf certificate"
        endpoint = transport.urlsplit("https://migi.example:8790/v1/agent-events")
        client = transport.MigiHTTPClient(
            endpoint,
            token="migi_at_test_secret",
            fingerprint=hashlib.sha256(certificate).hexdigest(),
        )
        connection = mock.Mock()
        connection.sock.getpeercert.return_value = certificate
        response = mock.Mock()
        connection.getresponse.return_value = response
        with mock.patch.object(
            transport.http.client,
            "HTTPSConnection",
            return_value=connection,
        ):
            opened = client.open("GET", "/v1/files")
        self.assertIs(opened.response, response)
        self.assertEqual(
            connection.request.call_args.kwargs["headers"]["Authorization"],
            "Bearer migi_at_test_secret",
        )

        connection.reset_mock()
        connection.sock.getpeercert.return_value = b"wrong certificate"
        with mock.patch.object(
            transport.http.client,
            "HTTPSConnection",
            return_value=connection,
        ):
            with self.assertRaisesRegex(RuntimeError, "fingerprint mismatch"):
                client.open("GET", "/v1/files")
        connection.request.assert_not_called()

    def test_agent_config_is_private_and_strict(self) -> None:
        transport = load_script(
            "portable_migi_config",
            FILE_SKILL / "scripts/_migi_transport.py",
        )
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "agent.json"
            path.write_text(
                json.dumps(
                    {
                        "endpoint": "https://migi.example:8790/v1/agent-events",
                        "token": "migi_at_test_secret",
                        "tls_fingerprint": "aa" * 32,
                    }
                ),
                encoding="utf-8",
            )
            os.chmod(path, 0o644)
            with self.assertRaisesRegex(RuntimeError, "permissions"):
                transport.load_agent_config(path)
            os.chmod(path, 0o600)
            config = transport.load_agent_config(path)
            self.assertEqual(config.endpoint.hostname, "migi.example")

    def test_scripts_have_no_checkout_or_installed_binary_fallback(self) -> None:
        forbidden = ("go run", "server/bin", "command -v migi-", "MIGI_FILE_BIN", "MIGI_PLAY_BIN")
        for skill in (FILE_SKILL, AUDIO_SKILL, PUBLISHER_SKILL):
            for path in (skill / "scripts").iterdir():
                if path.is_file():
                    text = path.read_text(encoding="utf-8")
                    for marker in forbidden:
                        self.assertNotIn(marker, text, f"{marker!r} found in {path}")

    def test_installer_creates_copies_not_repository_symlinks(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "skills"
            environment = os.environ.copy()
            environment["MIGI_SKILL_HOME"] = str(target)
            result = subprocess.run(
                [str(REPOSITORY / "scripts/install-migi-file-exchange-skill")],
                cwd=REPOSITORY,
                env=environment,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=10,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            for name in (
                "migi-file-exchange",
                "migi-audio-player",
                "migi-android-publisher",
            ):
                installed = target / name
                self.assertTrue((installed / "SKILL.md").is_file())
                self.assertFalse(installed.is_symlink())
                self.assertTrue((installed / "scripts/_migi_transport.py").is_file())


if __name__ == "__main__":
    unittest.main()
