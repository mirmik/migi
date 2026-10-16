"""Isolated notebook browser + pinned HTTPS/MCP smoke. Never touches live data.

Requires Playwright and Chromium; build the server first. Example:
python3 scripts/test-notebook-browser.py --server /tmp/migi-notebook-server
"""
import argparse
import json
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

from playwright.sync_api import sync_playwright, expect


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--server", required=True)
parser.add_argument("--chromium", help="Optional existing Chromium executable")
args = parser.parse_args()
repo = Path(__file__).resolve().parents[1]
root = Path(tempfile.mkdtemp(prefix="migi-notebook-smoke-"))
subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=localhost", "-keyout", str(root / "key.pem"), "-out", str(root / "cert.pem")], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
admin_port, agent_port, ingest_port, quic_port = port(), port(), port(), port()
base = f"http://127.0.0.1:{admin_port}"
endpoint = f"https://127.0.0.1:{agent_port}"
command = [str(Path(args.server).resolve()), "-listen", f"127.0.0.1:{quic_port}", "-ingest-listen", f"127.0.0.1:{ingest_port}", "-agent-listen", f"127.0.0.1:{agent_port}", "-admin-listen", f"127.0.0.1:{admin_port}", "-agent-endpoint", endpoint, "-cert", str(root / "cert.pem"), "-key", str(root / "key.pem"), "-db", str(root / "migi.db")]
log = (root / "server.log").open("w")


def start():
    process = subprocess.Popen(command, cwd=root, stdout=log, stderr=log)
    for _ in range(100):
        if process.poll() is not None:
            raise RuntimeError(f"Server stopped; see {root / 'server.log'}")
        try:
            urllib.request.urlopen(base + "/admin/notebook/", timeout=1).close()
            return process
        except OSError:
            time.sleep(0.1)
    process.terminate()
    process.wait(timeout=15)
    raise RuntimeError("Server did not start")


def stop(process):
    process.send_signal(signal.SIGTERM)
    process.wait(timeout=20)
    assert process.returncode == 0


process = start()
try:
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True, executable_path=args.chromium)
        page = browser.new_page(viewport={"width": 1440, "height": 1100})
        errors = []
        page.on("pageerror", lambda e: errors.append(str(e)))
        page.on("dialog", lambda dialog: dialog.accept())
        page.goto(base + "/admin/notebook/")
        expect(page.locator("#new")).to_be_enabled()
        page.locator("#key").fill("workshop/printer")
        page.locator("#title").fill("Принтер мастерской")
        page.locator("#body").fill("Адрес: 192.0.2.1\nСлайсер: tools/slicer\n<script>throw new Error('XSS')</script>")
        page.locator("#tags").fill("workshop, печать")
        page.locator("#save").click()
        expect(page.locator("#metadata")).to_contain_text("Версия 1")
        page.locator("#query").fill("ПРИНТЕР*")
        page.locator("#search-form button[type=submit]").click()
        expect(page.locator("#results button")).to_have_count(1)
        expect(page.locator("#new")).to_be_enabled()

        # Another writer changes the record while this editor keeps revision 1.
        result = page.evaluate("""async () => {
          const csrf = document.querySelector('meta[name="csrf-token"]').content;
          const response = await fetch('api/update', {method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:JSON.stringify({key:'workshop/printer',expected_revision:1,changes:{body:'Адрес: 192.0.2.2'},operation_id:'other-writer'})});
          return response.status;
        }""")
        assert result == 200
        page.locator("#body").fill("Мой несохранённый текст")
        page.locator("#save").click()
        expect(page.locator("#notice")).to_contain_text("Ваш текст сохранён в редакторе")
        expect(page.locator("#body")).to_have_value("Мой несохранённый текст")
        page.locator("#reload").click()
        expect(page.locator("#metadata")).to_contain_text("Версия 2")

        # Drop the response after committing, then retry from the same editor.
        def lose_response(route):
            route.fetch()
            route.abort("failed")
            page.unroute("**/api/update", lose_response)

        page.route("**/api/update", lose_response)
        page.locator("#body").fill("Адрес: 192.0.2.3")
        page.locator("#save").click()
        expect(page.locator("#notice")).to_contain_text("Failed")
        page.locator("#save").click()
        expect(page.locator("#metadata")).to_contain_text("Версия 3")

        page.locator("#delete").click()
        expect(page.locator("#editor-title")).to_have_text("Запись в корзине")
        page.locator("#history").click()
        expect(page.locator("#revisions button")).to_have_count(4)
        page.locator("#revisions button").filter(has_text="Версия 1 ·").click()
        expect(page.locator("#revision-preview")).to_contain_text("192.0.2.1")
        page.locator("#restore").click()
        expect(page.locator("#metadata")).to_contain_text("Версия 5")
        expect(page.locator("#new")).to_be_enabled()
        with page.expect_download() as download:
            page.locator("#export").click()
        export_file = root / "export.json"
        download.value.save_as(export_file)
        exported = json.loads(export_file.read_text())
        assert len(exported["history"]) == 5
        page.screenshot(path=str(root / "desktop.png"), full_page=True)
        page.set_viewport_size({"width": 390, "height": 844})
        assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
        page.screenshot(path=str(root / "mobile.png"), full_page=True)

        # Provision only the isolated server's agent, then exercise stdio -> TLS.
        page.goto(base + "/admin/agents/")
        form = page.locator('form[action="../agents/create"]')
        form.locator('input[name="name"]').fill("notebook-smoke")
        form.locator('button').click()
        config = json.loads(page.locator(".credential pre").inner_text())
        config_path = root / "agent.json"
        config_path.write_text(json.dumps(config))
        config_path.chmod(0o600)

        def mcp_call(name, arguments, failed=False):
            messages = [{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"smoke","version":"1"}}}, {"jsonrpc":"2.0","method":"notifications/initialized"}, {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":name,"arguments":arguments}}]
            result = subprocess.run([sys.executable,str(repo / "integrations/notebook/migi_notebook_mcp.py"),"--config",str(config_path)],input="\n".join(json.dumps(m) for m in messages)+"\n",text=True,capture_output=True,check=True)
            reply = json.loads(result.stdout.splitlines()[-1])["result"]
            assert reply["isError"] == failed, reply
            return json.loads(reply["content"][0]["text"])

        mcp_call("notebook_read", {"keys":["workshop/printer"]}, failed=True)
        page.goto(base + "/admin/notebook/")
        expect(page.locator("#new")).to_be_enabled()
        grant = page.locator(".notebook-grant")
        grant.locator('input[name="write"]').check()
        grant.locator("button").click()
        expect(page.locator("#new")).to_be_enabled()
        mcp_call("notebook_create", {"key":"tools/slicer","title":"Слайсер","body":"/opt/OrcaSlicer --profile /profiles/PLA.json"})
        result = mcp_call("notebook_search", {"query":"/opt/OrcaSlicer","mode":"literal"})
        assert result["items"][0]["key"] == "tools/slicer"
        result = mcp_call("notebook_read", {"keys":["workshop/printer","tools/slicer"]})
        assert len(result["items"]) == 2
        stop(process)
        process = start()
        result = mcp_call("notebook_read", {"keys":["tools/slicer"]})
        assert result["items"][0]["revision"] == 1
        page.goto(base + "/admin/notebook/")
        expect(page.locator("#results button")).to_have_count(2)

        # Restore the downloaded export through the actual UI into a new DB.
        stop(process)
        command.extend(["-notebook-db", str(root / "restored.sqlite")])
        process = start()
        page.goto(base + "/admin/notebook/")
        expect(page.locator("#new")).to_be_enabled()
        page.locator("#import-file").set_input_files(export_file)
        page.locator("#import").click()
        expect(page.locator("#notice")).to_contain_text("Блокнот восстановлен")
        expect(page.locator("#results button")).to_have_count(1)
        page.locator("#results button").click()
        expect(page.locator("#metadata")).to_contain_text("Версия 5")
        page.locator("#history").click()
        expect(page.locator("#revisions button")).to_have_count(5)
        mcp_call("notebook_read", {"keys":["workshop/printer"]}, failed=True)
        assert not errors, errors
        browser.close()
    print(f"PASS: browser CRUD/conflicts/retries/history/export/import/mobile, MCP permissions + pinned TLS + restart. Artifacts: {root}")
finally:
    if process.poll() is None:
        stop(process)
    log.close()
