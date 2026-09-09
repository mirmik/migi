import argparse
import tempfile
from pathlib import Path
from playwright.sync_api import sync_playwright, expect

parser = argparse.ArgumentParser(
    description="Read-only live chat smoke, then isolated browser action tests"
)
parser.add_argument(
    "--url",
    required=True,
    help="Existing admin chat URL with a paired device and history",
)
args = parser.parse_args()
URL = args.url
output = Path(tempfile.mkdtemp(prefix="migi-web-smoke-"))
with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    page = browser.new_page(viewport={"width": 1440, "height": 1100})
    errors = []
    page.on("pageerror", lambda e: errors.append(str(e)))
    page.goto(URL)
    expect(page.locator("#prompt")).to_be_enabled()
    expect(page.locator("#new")).to_be_enabled()
    assert page.locator(".chat-message").count() > 0
    assert page.locator("#error").is_hidden()
    page.screenshot(path=str(output / "desktop.png"), full_page=True)
    page.set_viewport_size({"width": 390, "height": 844})
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
    page.screenshot(path=str(output / "mobile.png"), full_page=True)
    print("Live page: history, prompt, controls, responsive width OK")
    page.close()

    # UI actions against deterministic responses; do not rewrite the user's
    # global prompt or create test messages in their production conversation.
    page = browser.new_page(viewport={"width": 1440, "height": 1000})
    page.on("pageerror", lambda e: errors.append(str(e)))
    state = dict(
        thread_id="device:test",
        chats=[{"thread_id": "device:test", "title": "Чат 1"}],
        status="idle",
        busy=False,
        elapsed_seconds=0,
        model="test",
        context_tokens_estimate=500,
        context_messages=2,
        context_window=None,
        active_tools=[],
        notice=None,
        messages=[],
        message_total=0,
        live_text="",
    )
    prompt = {"text": "Исходная инструкция", "revision": "one", "is_default": True}
    posted, applied = [], set()

    def state_route(route):
        if route.request.method == "POST":
            body = route.request.post_data_json
            posted.append(body)
            assert route.request.headers.get("x-csrf-token")
            rid = body["request_id"]
            action = body["action"]
            if rid not in applied:
                applied.add(rid)
                if action == "send":
                    state["messages"] += [
                        {"id": rid, "role": "user", "text": body["text"]},
                        {
                            "id": rid + "a",
                            "role": "assistant",
                            "text": "Тестовый ответ",
                        },
                    ]
                    state["message_total"] = len(state["messages"])
                    if len(posted) == 1:
                        route.abort("failed")
                        return
                elif action == "new":
                    state.update(
                        thread_id="device:test:new", messages=[], message_total=0
                    )
                    state["chats"].append(
                        {"thread_id": state["thread_id"], "title": "Чат 2"}
                    )
                elif action == "select":
                    state["thread_id"] = body["target_thread"]
                elif action == "compact":
                    state["notice"] = "Контекст сжат"
                    state["context_tokens_estimate"] = 100
                elif action == "stop":
                    state.update(busy=False, status="cancelled", active_tools=[])
            route.fulfill(json={"thread_id": state["thread_id"]})
            return
        route.fulfill(json=state)

    def prompt_route(route):
        if route.request.method == "POST":
            b = route.request.post_data_json
            if b["revision"] != prompt["revision"]:
                route.fulfill(status=409, body="Промпт изменён в другой вкладке")
                return
            prompt.update(
                text="Исходная инструкция" if b.get("reset") else b["text"],
                revision=prompt["revision"] + "x",
            )
        route.fulfill(json=prompt)

    page.route("**/admin/chat/state?*", state_route)
    page.route("**/admin/chat/prompt", prompt_route)
    page.goto(URL)
    expect(page.locator("#new")).to_be_enabled()
    page.locator("#message").fill("<script>unsafe</script>")
    page.locator("#send").click()
    expect(page.locator(".chat-message")).to_have_count(2, timeout=10000)
    assert len(posted) == 2 and posted[0]["request_id"] == posted[1]["request_id"]
    expect(page.locator("#message")).to_have_value("")
    assert page.locator("#history script").count() == 0
    page.locator("#compact").click()
    expect(page.locator("#notice")).to_have_text("Контекст сжат")
    page.locator("#new").click()
    expect(page.locator("#threads")).to_have_value("device:test:new")
    page.locator("#threads").select_option("device:test")
    expect(page.locator("#new")).to_be_enabled()
    state.update(busy=True, status="running", active_tools=["read_file"])
    page.locator("#refresh").click()
    expect(page.locator("#stop")).to_be_enabled()
    expect(page.locator("#tools")).to_have_text("Инструменты: read_file")
    page.locator("#stop").click()
    expect(page.locator("#status")).to_have_text("Остановлен")
    page.locator("#prompt").fill("Новая инструкция")
    page.locator("#save-prompt").click()
    expect(page.locator("#prompt-status")).to_contain_text("Сохранено")
    prompt["revision"] = "other-tab"
    page.locator("#prompt").fill("Несохранённая правка")
    page.locator("#save-prompt").click()
    expect(page.locator("#prompt-status")).to_contain_text("другой вкладке")
    expect(page.locator("#prompt")).to_have_value("Несохранённая правка")
    page.on("dialog", lambda d: d.accept())
    page.locator("#reload-prompt").click()
    expect(page.locator("#prompt")).to_have_value("Новая инструкция")
    page.locator("#reset-prompt").click()
    expect(page.locator("#prompt")).to_have_value("Исходная инструкция")
    assert not errors, errors
    print(
        "Browser actions: retry dedup, escaped text, compact/new/select/stop, prompt save/conflict/reload/reset OK"
    )
    browser.close()
