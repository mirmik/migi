(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const audio = $('audio');
  const storageKey = 'migi.music.v1';
  let library = {tracks: [], playlists: []};
  let queue = [], index = -1, queueName = '', artwork = null, tab = 'playlists';
  let selection = 0, libraryRequest = 0, sourceVersion = 0, resumeTime = 0, lastSave = 0;
  const validID = id => typeof id === 'string' && /^[a-f0-9]{32}$/.test(id);
  const mediaURL = id => `api/media/${encodeURIComponent(id)}`;
  const error = message => { $('music-error').textContent = message; $('music-error').hidden = !message; };
  const status = message => { $('playback-status').textContent = message; };

  async function getJSON(url) {
    const response = await fetch(url, {cache: 'no-store'});
    if (!response.ok) throw new Error(response.status === 409 ? 'Некоторые треки плейлиста больше недоступны. Обновите медиатеку.' : `Не удалось загрузить медиатеку (${response.status}). Попробуйте обновить её.`);
    return response.json();
  }
  function save() {
    if (index < 0) return;
    try {
      localStorage.setItem(storageKey, JSON.stringify({ids: queue.map(t => t.id), index, name: queueName, artwork, time: resumeTime || audio.currentTime || 0}));
    } catch (_) { /* Playback still works when browser storage is unavailable. */ }
  }
  function row(title, subtitle, action) {
    const button = document.createElement('button');
    button.type = 'button'; button.className = 'music-row';
    const strong = document.createElement('strong'); strong.textContent = title;
    const small = document.createElement('small'); small.textContent = subtitle;
    button.append(strong, small); button.addEventListener('click', action);
    return button;
  }
  function renderLibrary() {
    $('show-playlists').setAttribute('aria-pressed', String(tab === 'playlists'));
    $('show-tracks').setAttribute('aria-pressed', String(tab === 'tracks'));
    const query = $('search').value.toLocaleLowerCase().trim();
    const terms = query.split(/\s+/).filter(Boolean);
    const data = (tab === 'playlists' ? library.playlists : library.tracks).filter(item => terms.every(term => `${item.name || ''} ${item.title || ''} ${item.artist || ''}`.toLocaleLowerCase().includes(term)));
    $('library-list').replaceChildren(...data.map(item => tab === 'playlists'
      ? row(item.name, `Треков: ${item.track_count} · открыть`, () => selectPlaylist(item))
      : row(item.title, item.artist || item.name, () => { selection++; setQueue(data, data.indexOf(item), 'Все треки', null); })));
    $('library-status').textContent = data.length ? `${tab === 'playlists' ? 'Плейлистов' : 'Треков'}: ${data.length}` : query ? 'Ничего не найдено.' : 'Пока пусто. Добавьте музыку через агента.';
  }
  function renderQueue() {
    $('queue-list').replaceChildren(...queue.map((track, i) => {
      const li = document.createElement('li');
      const button = row(`${i + 1}. ${track.title}`, track.artist || '', () => { selection++; loadTrack(i, true); });
      button.setAttribute('aria-current', String(i === index)); li.append(button); return li;
    }));
    $('queue-status').textContent = queue.length ? `${index + 1} / ${queue.length} · ${queueName}` : 'Выберите плейлист или трек, чтобы начать.';
    $('previous').disabled = index <= 0;
    $('next').disabled = index < 0 || index >= queue.length - 1;
  }
  function setQueue(items, selected, name, cover, autoplay = true, time = 0) {
    queue = items; queueName = name; artwork = cover;
    $('cover').hidden = true; $('cover-placeholder').hidden = false;
    $('cover').removeAttribute('src');
    if (artwork && validID(artwork.id)) $('cover').src = mediaURL(artwork.id);
    loadTrack(selected, autoplay, time);
  }
  function loadTrack(selected, autoplay, time = 0) {
    if (selected < 0 || selected >= queue.length) return;
    const version = ++sourceVersion;
    audio.pause(); index = selected; resumeTime = time;
    error('');
    $('track-title').textContent = queue[index].title;
    $('track-artist').textContent = queue[index].artist || '';
    $('queue-name').textContent = queueName;
    audio.src = mediaURL(queue[index].id);
    // Only an explicit play requests media; restoring a queue remains silent.
    audio.preload = time > 0 ? 'metadata' : 'none';
    audio.load(); renderQueue(); save();
    status(autoplay ? 'Загрузка…' : 'Нажмите Play, чтобы продолжить');
    if (autoplay) audio.play().catch(e => {
      if (version !== sourceVersion || e.name === 'AbortError') return;
      if (e.name === 'NotAllowedError') status('Нажмите Play, чтобы начать');
      else error('Не удалось воспроизвести трек. Хранилище недоступно или браузер не поддерживает этот формат.');
    });
  }
  async function selectPlaylist(playlist) {
    const token = ++selection;
    error('');
    $('library-status').textContent = `Загрузка «${playlist.name}»…`;
    try {
      const manifest = await getJSON(`api/playlists/${encodeURIComponent(playlist.id)}`);
      if (token !== selection) return;
      if (!manifest.items.length) throw new Error('В этом плейлисте нет треков.');
      setQueue(manifest.items, 0, manifest.name, manifest.artwork || null);
    } catch (e) { if (token === selection) error(e.message); }
    finally { if (token === selection) renderLibrary(); }
  }
  function restore() {
    try {
      const saved = JSON.parse(localStorage.getItem(storageKey));
      if (!saved || !Array.isArray(saved.ids) || !Number.isInteger(saved.index)) return;
      const byID = new Map(library.tracks.map(track => [track.id, track]));
      const available = saved.ids.map((id, originalIndex) => ({track: validID(id) ? byID.get(id) : null, originalIndex})).filter(item => item.track);
      const items = available.map(item => item.track);
      const selected = available.findIndex(item => item.originalIndex === saved.index);
      if (!items.length || selected < 0) return;
      const time = Number.isFinite(saved.time) && saved.time >= 0 ? saved.time : 0;
      setQueue(items, selected, typeof saved.name === 'string' ? saved.name : 'Музыка', saved.artwork, false, time);
    } catch (_) { /* Ignore stale state. */ }
  }
  async function refresh(initial = false) {
    const token = ++libraryRequest;
    $('refresh').disabled = true;
    try {
      const result = await getJSON('api/library');
      if (token !== libraryRequest) return;
      library = result; renderLibrary();
      if (initial && index < 0 && selection === 0) restore();
    } catch (e) { error(e.message); $('library-status').textContent = 'Медиатека недоступна.'; }
    finally { if (token === libraryRequest) $('refresh').disabled = false; }
  }
  $('cover').addEventListener('load', () => { $('cover').hidden = false; $('cover-placeholder').hidden = true; });
  $('cover').addEventListener('error', () => { $('cover').hidden = true; $('cover-placeholder').hidden = false; });
  $('show-playlists').onclick = () => { tab = 'playlists'; renderLibrary(); };
  $('show-tracks').onclick = () => { tab = 'tracks'; renderLibrary(); };
  $('search').oninput = renderLibrary;
  $('refresh').onclick = () => { error(''); refresh(); };
  $('previous').onclick = () => { selection++; loadTrack(index - 1, true); };
  $('next').onclick = () => { selection++; loadTrack(index + 1, true); };
  audio.addEventListener('loadedmetadata', () => {
    if (resumeTime > 0) {
      audio.currentTime = Number.isFinite(audio.duration) ? Math.min(resumeTime, Math.max(0, audio.duration - 0.1)) : resumeTime;
      resumeTime = 0;
    }
  });
  audio.addEventListener('playing', () => { error(''); status('Играет'); });
  audio.addEventListener('waiting', () => { if (!audio.paused) status('Загрузка…'); });
  audio.addEventListener('pause', () => { if (!audio.ended) status('Пауза'); save(); });
  audio.addEventListener('ended', () => {
    if (index + 1 < queue.length) loadTrack(index + 1, true);
    else { status('Плейлист завершён'); save(); }
  });
  audio.addEventListener('error', () => {
    status('Не удалось загрузить трек');
    error('Не удалось воспроизвести трек. Проверьте доступность хранилища; возможно, браузер не поддерживает формат. Можно повторить запуск, выбрав трек в очереди.');
  });
  audio.addEventListener('seeked', save);
  audio.addEventListener('timeupdate', () => { if (Date.now() - lastSave > 2000) { lastSave = Date.now(); save(); } });
  window.addEventListener('pagehide', save);
  refresh(true);
})();
