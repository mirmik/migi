'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const csrf = document.querySelector('meta[name="csrf-token"]').content;
  const storage = sessionStorage;
  let state = null, loading = false, limit = 40, signature = '', promptState = null, promptBusy = false;
  let pending = JSON.parse(storage.getItem('migi-chat-pending') || 'null');
  const savedDevice = pending?.device || storage.getItem('migi-chat-device');
  if ([...$('device').options].some(o => o.value === savedDevice)) $('device').value = savedDevice;
  const draftKey = () => 'migi-chat-draft:' + $('device').value;
  $('message').value = storage.getItem(draftKey()) || '';
  const show = (id, text) => { $(id).textContent = text || ''; $(id).hidden = !text; };
  function controls() {
    const busy = !!state?.busy, ready = !!state && !pending && !loading;
    for (const id of ['new', 'compact', 'threads']) $(id).disabled = !ready || busy;
    $('send').disabled = !ready || busy || !$('message').value.trim();
    $('stop').disabled = !ready || !busy;
    $('device').disabled = !!pending || loading;
  }
  async function request(path, body) {
    const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const response = await fetch(path, {method: body ? 'POST' : 'GET', cache: 'no-store', signal: controller.signal,
        headers: body ? {'Content-Type': 'application/json', 'X-CSRF-Token': csrf} : {}, body: body ? JSON.stringify(body) : undefined});
      if (!response.ok) { const e = new Error(await response.text()); e.status = response.status; throw e; }
      return await response.json();
    } finally { clearTimeout(timeout); }
  }
  function render(s) {
    const switched = state?.thread_id !== s.thread_id;
    state = s;
    const statuses = {idle: 'Готов к сообщению', completed: 'Готов к сообщению', running: 'Отвечает', queued: 'Начинает ответ', cancelled: 'Остановлен', failed: 'Ошибка агента', compacting: 'Сжимает контекст', stopping: 'Останавливается'};
    $('status').textContent = (statuses[s.status] || s.status) + (s.busy && s.elapsed_seconds ? ` · ${s.elapsed_seconds} с` : '');
    $('model').textContent = s.model;
    $('context').textContent = `Контекст ≈ ${Number(s.context_tokens_estimate).toLocaleString('ru')} токенов · ${s.context_messages} сообщений`;
    $('context-progress').hidden = !s.context_window;
    if (s.context_window) $('context-progress').value = Math.min(100, 100 * s.context_tokens_estimate / s.context_window);
    $('tools').textContent = s.active_tools.length ? 'Инструменты: ' + s.active_tools.join(', ') : '';
    if (s.compaction_progress && (s.status === 'compacting' || s.status === 'stopping')) {
      const p = s.compaction_progress;
      const phases = {preparing: 'Подготовка', generating: 'Ожидание модели', reasoning: 'Модель обдумывает сводку', summarizing: 'Формирование сводки', committing: 'Сохранение'};
      $('tools').textContent = (phases[p.phase] || 'Сжатие') + (p.summary_chars ? ` · ${p.summary_chars} символов сводки` : '');
    }
    const options = s.chats.map(c => [c.thread_id, c.title]);
    if (JSON.stringify(options) !== $('threads').dataset.options) {
      $('threads').replaceChildren(...options.map(([id, title]) => new Option(title, id)));
      $('threads').dataset.options = JSON.stringify(options);
    }
    $('threads').value = s.thread_id;
    show('notice', s.notice);
    $('earlier').hidden = s.message_total <= s.messages.length || limit >= 10000;
    const next = JSON.stringify([s.thread_id, s.messages, s.live_text]);
    if (next !== signature) {
      const h = $('history'), bottom = h.scrollHeight - h.scrollTop - h.clientHeight < 80;
      const oldHeight = h.scrollHeight, oldTop = h.scrollTop;
      const oldFirst = h.firstElementChild?.dataset.id;
      const rows = s.messages.map(m => ({...m}));
      if (s.live_text) rows.push({role: 'assistant', text: s.live_text, live: true});
      h.replaceChildren(...rows.map(m => {
        const el = document.createElement('article'); el.className = 'chat-message ' + (m.role === 'user' ? 'user' : 'assistant'); el.dataset.id = m.id || 'live';
        const title = document.createElement('strong'); title.textContent = m.role === 'user' ? 'Вы' : m.live ? 'Агент · пишет…' : 'Агент';
        const body = document.createElement('p'); body.textContent = m.text; el.append(title, body); return el;
      }));
      if (!rows.length) { const p = document.createElement('p'); p.textContent = 'Здесь появится переписка. Можно написать с телефона, из браузера или надиктовать в очках.'; h.append(p); }
      if (switched || bottom) h.scrollTop = h.scrollHeight;
      else if (oldFirst && s.messages.some(m => m.id === oldFirst) && s.messages[0]?.id !== oldFirst) h.scrollTop = oldTop + h.scrollHeight - oldHeight;
      else h.scrollTop = oldTop;
      signature = next;
    }
  }
  async function refresh() {
    if (loading || !$('device').value || $('agent-chat').dataset.enabled !== 'true') return;
    loading = true; controls();
    try {
      if (pending) {
        try {
          await request('state?device=' + encodeURIComponent(pending.device), pending.body);
          if (pending.body.action === 'send' && $('message').value === pending.body.text) {
            $('message').value = ''; storage.removeItem(draftKey());
          }
          storage.removeItem('migi-chat-pending'); pending = null;
        } catch (e) {
          if (e.status >= 400 && e.status < 500) { storage.removeItem('migi-chat-pending'); pending = null; }
          throw e;
        }
      }
      render(await request('state?device=' + encodeURIComponent($('device').value) + '&limit=' + limit));
      show('error', '');
    } catch (e) { show('error', (e.message || 'Ошибка соединения') + (pending ? ' Действие сохранено и будет повторено с тем же ID.' : '')); }
    finally { loading = false; controls(); }
  }
  function act(action, extra = {}) {
    if (!state || pending || loading) return;
    const random = crypto.getRandomValues(new Uint8Array(16));
    const body = {action, request_id: 'web-' + [...random].map(x => x.toString(16).padStart(2, '0')).join(''), thread_id: state.thread_id, ...extra};
    if (new TextEncoder().encode(JSON.stringify(body)).length > 64 * 1024) { show('error', 'Сообщение слишком длинное (максимум 64 КиБ).'); return; }
    const operation = {device: $('device').value, body};
    try { storage.setItem('migi-chat-pending', JSON.stringify(operation)); } catch (_) { show('error', 'Браузер не смог сохранить действие. Освободите место в хранилище.'); return; }
    pending = operation; refresh();
  }
  $('device').addEventListener('change', () => {
    storage.setItem('migi-chat-device', $('device').value); $('message').value = storage.getItem(draftKey()) || '';
    state = null; signature = ''; limit = 40; $('history').replaceChildren(); refresh();
  });
  $('threads').addEventListener('change', () => act('select', {target_thread: $('threads').value}));
  $('new').addEventListener('click', () => act('new'));
  $('compact').addEventListener('click', () => act('compact'));
  $('stop').addEventListener('click', () => act('stop'));
  $('refresh').addEventListener('click', refresh);
  $('earlier').addEventListener('click', () => { limit = Math.min(10000, limit + 40); refresh(); });
  $('message').addEventListener('input', () => { storage.setItem(draftKey(), $('message').value); controls(); });
  $('composer').addEventListener('submit', e => { e.preventDefault(); if (!$('send').disabled) act('send', {text: $('message').value}); });
  $('message').addEventListener('keydown', e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); $('composer').requestSubmit(); } });
  function promptControls() {
    $('prompt').disabled = !promptState || promptBusy;
    $('save-prompt').disabled = !promptState || promptBusy || !$('prompt').value.trim() || $('prompt').value === promptState.text;
    $('reset-prompt').disabled = !promptState || promptBusy;
    $('reload-prompt').disabled = promptBusy;
  }
  async function promptRequest(body) {
    if (promptBusy) return;
    promptBusy = true; promptControls();
    try {
      promptState = await request('prompt', body);
      $('prompt').value = promptState.text;
      $('prompt-status').textContent = body ? 'Сохранено. Применится со следующего сообщения.' : 'Сохранённый промпт загружен.';
    } catch (e) { $('prompt-status').textContent = e.message; }
    finally { promptBusy = false; promptControls(); }
  }
  $('prompt').addEventListener('input', promptControls);
  $('save-prompt').addEventListener('click', () => promptRequest({text: $('prompt').value, revision: promptState.revision}));
  $('reload-prompt').addEventListener('click', () => { if (!promptState || $('prompt').value === promptState.text || confirm('Заменить несохранённый текст сохранённой версией?')) promptRequest(); });
  $('reset-prompt').addEventListener('click', () => { if (confirm('Восстановить штатный системный промпт для всех чатов?')) promptRequest({reset: true, revision: promptState.revision}); });
  if (!$('device').value) $('status').textContent = 'Сначала подключите телефон к Migi';
  if ($('agent-chat').dataset.enabled === 'true') { refresh(); promptRequest(); }
  setInterval(() => { if (!document.hidden) refresh(); }, 2000);
})();
