"use strict";
(() => {
  const el = id => document.getElementById(id);
  const csrf = document.querySelector('meta[name="csrf-token"]').content;
  let record = null, dirty = false, cursor = "", searchInput = {}, preview = null;
  let pending = null, busy = false, searchEpoch = 0;
  const tags = value => value.split(",").map(x => x.trim()).filter(Boolean);
  const notice = text => { el("notice").textContent = text; };
  const uuid = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), b => b.toString(16).padStart(2, "0")).join("");
  async function api(op, input) {
    const response = await fetch(`api/${op}`, {method: "POST", headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf}, body: JSON.stringify(input)});
    const text = await response.text();
    let data; try { data = JSON.parse(text); } catch { throw new Error(text || `HTTP ${response.status}`); }
    if (!response.ok) {
      if (data.error?.code === "revision_conflict") throw new Error("Запись уже изменена. Ваш текст сохранён в редакторе. Скопируйте нужные изменения, перечитайте запись и объедините их с актуальной версией.");
      throw new Error(data.error?.message || `HTTP ${response.status}`);
    }
    return data;
  }
  async function mutate(op, input) {
    const signature = JSON.stringify([op, input]);
    if (!pending || pending.signature !== signature) pending = {signature, id: uuid()};
    const result = await api(op, {...input, operation_id: pending.id});
    pending = null;
    return result;
  }
  function guard() { return !dirty || confirm("Отбросить несохранённый текст?"); }
  function show(r) {
    record = r; dirty = false; preview = null;
    el("key").value = r?.key || ""; el("key").readOnly = !!r;
    el("title").value = r?.title || ""; el("tags").value = r?.tags.join(", ") || ""; el("body").value = r?.body || "";
    el("editor-title").textContent = r ? (r.deleted ? "Запись в корзине" : "Редактирование") : "Новая запись";
    el("metadata").textContent = r ? `Версия ${r.revision} · ${r.updated_at} · ${r.updated_by}` : "";
    for (const id of ["history", "reload"]) el(id).hidden = !r;
    el("delete").hidden = !r || r.deleted; el("save").hidden = !!r?.deleted;
    for (const id of ["title", "tags", "body"]) el(id).readOnly = !!r?.deleted;
    el("history-panel").hidden = true; el("restore").hidden = true;
  }
  async function open(key) { const data = await api("read", {keys: [key]}); show(data.items[0]); }
  function row(item) {
    const li = document.createElement("li"), button = document.createElement("button"), p = document.createElement("p");
    button.type = "button"; button.textContent = `${item.title} · ${item.key}`;
    button.addEventListener("click", () => run(async () => { if (guard()) await open(item.key); }));
    p.textContent = item.snippet; li.append(button, p); return li;
  }
  async function search(more = false) {
    const epoch = ++searchEpoch;
    if (!more) {
      searchInput = {query: el("query").value, mode: el("mode").value, prefix: el("prefix").value, tags: tags(el("filter-tags").value), deleted: el("trash").checked, limit: 10};
      cursor = "";
    }
    const data = await api(searchInput.query ? "search" : "list", {...searchInput, cursor});
    if (epoch !== searchEpoch) return;
    if (!more) el("results").replaceChildren();
    data.items.forEach(item => el("results").append(row(item)));
    cursor = data.next_cursor || ""; el("more").hidden = !cursor;
    if (!more && !data.items.length) el("results").textContent = "Записей не найдено.";
  }
  async function run(fn) {
    if (busy) return;
    busy = true;
    const controls = Array.from(document.querySelectorAll("button, input, textarea, select"));
    controls.forEach(x => { x.disabled = true; });
    try { await fn(); } catch (error) { notice(error.message); }
    finally { controls.forEach(x => { x.disabled = false; }); busy = false; }
  }
  el("editor").addEventListener("input", () => { dirty = true; });
  window.addEventListener("beforeunload", event => { if (dirty) { event.preventDefault(); event.returnValue = ""; } });
  el("new").onclick = () => { if (guard()) { show(null); notice(""); } };
  el("reload").onclick = () => run(async () => { if (record && guard()) await open(record.key); });
  el("search-form").onsubmit = event => { event.preventDefault(); run(() => search()); };
  el("more").onclick = () => run(() => search(true));
  el("editor").onsubmit = event => {
    event.preventDefault(); run(async () => {
      const fields = {title: el("title").value, body: el("body").value, tags: tags(el("tags").value)};
      const r = record ? await mutate("update", {key: record.key, expected_revision: record.revision, changes: fields}) : await mutate("create", {key: el("key").value, ...fields});
      show(r); notice(`Сохранена версия ${r.revision}.`); await search();
    });
  };
  el("delete").onclick = () => run(async () => {
    if (!record || !guard() || !confirm("Переместить запись в корзину?")) return;
    show(await mutate("delete", {key: record.key, expected_revision: record.revision})); notice("Запись в корзине. Восстановить её можно через историю."); await search();
  });
  el("history").onclick = () => run(async () => {
    const revisions = await api("history", {key: record.key}); el("revisions").replaceChildren();
    for (const rev of revisions) {
      const li = document.createElement("li"), button = document.createElement("button"); button.type = "button";
      button.textContent = `Версия ${rev.revision} · ${rev.updated_at}${rev.deleted ? " · удалена" : ""}`;
      button.onclick = () => run(async () => {
        preview = await api("history", {key: record.key, revision: rev.revision});
        el("revision-preview").textContent = `${preview.title}\n${preview.tags.join(", ")}\n\n${preview.body}`; el("restore").hidden = false;
      }); li.append(button); el("revisions").append(li);
    } el("history-panel").hidden = false;
  });
  el("restore").onclick = () => run(async () => {
    if (!record || !preview || !guard() || !confirm(`Восстановить версию ${preview.revision}?`)) return;
    show(await mutate("restore", {key: record.key, expected_revision: record.revision, revision: preview.revision})); notice("Версия восстановлена."); await search();
  });
  el("export").onclick = () => run(async () => {
    const data = await api("export", {}); const url = URL.createObjectURL(new Blob([JSON.stringify(data)], {type: "application/json"}));
    const a = document.createElement("a"); a.href = url; a.download = "notebook.json"; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); notice("Экспорт готов.");
  });
  el("import").onclick = () => run(async () => {
    const file = el("import-file").files[0]; if (!file) throw new Error("Выберите JSON-файл.");
    if (file.size > 64 * 1024 * 1024) throw new Error("Размер файла превышает 64 MiB.");
    if (!guard() || !confirm("Восстановить блокнот из файла? Блокнот должен быть пустым.")) return;
    await api("import", JSON.parse(await file.text())); show(null); notice("Блокнот восстановлен. Настройте доступ агентов."); await search();
  });
  run(() => search());
})();
