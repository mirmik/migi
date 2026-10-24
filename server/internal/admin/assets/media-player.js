(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const player = $('audio') || $('video');
  const isVideo = player.tagName === 'VIDEO';
  const storageKey = isVideo ? 'migi.video.v1' : 'migi.music.v1';
  const labels = isVideo ? {
    all: 'Все видео', count: 'Видео', collections: 'Коллекций', items: 'Видео',
    empty: 'Пока пусто. Добавьте видео через агента.',
    choose: 'Выберите коллекцию или видео, чтобы начать.', fallback: 'Видео',
    unavailable: 'Некоторые видео коллекции больше недоступны. Обновите медиатеку.',
    emptyCollection: 'В этой коллекции нет видео.', finished: 'Коллекция завершена',
    loadError: 'Не удалось загрузить видео', playing: 'Воспроизводится',
    playError: 'Не удалось воспроизвести видео. Проверьте доступность хранилища; возможно, браузер не поддерживает формат. Можно повторить запуск, выбрав видео в очереди.'
  } : {
    all: 'Все треки', count: 'Треков', collections: 'Плейлистов', items: 'Треков',
    empty: 'Пока пусто. Добавьте музыку через агента.',
    choose: 'Выберите плейлист или трек, чтобы начать.', fallback: 'Музыка',
    unavailable: 'Некоторые треки плейлиста больше недоступны. Обновите медиатеку.',
    emptyCollection: 'В этом плейлисте нет треков.', finished: 'Плейлист завершён',
    loadError: 'Не удалось загрузить трек', playing: 'Играет',
    playError: 'Не удалось воспроизвести трек. Проверьте доступность хранилища; возможно, браузер не поддерживает формат. Можно повторить запуск, выбрав трек в очереди.'
  };
  let library = {tracks: [], playlists: []};
  let queue = [], index = -1, queueName = '', artwork = null, tab = 'playlists';
  let selection = 0, libraryRequest = 0, sourceVersion = 0, resumeTime = 0, lastSave = 0;
  const validID = id => typeof id === 'string' && /^[a-f0-9]{32}$/.test(id);
  const mediaURL = id => `api/media/${encodeURIComponent(id)}`;
  const error = message => { const box = $(isVideo ? 'video-error' : 'music-error'); box.textContent = message; box.hidden = !message; };
  const status = message => { $('playback-status').textContent = message; };

  async function getJSON(url) {
    const response = await fetch(url, {cache: 'no-store'});
    if (!response.ok) throw new Error(response.status === 409 ? labels.unavailable : `Не удалось загрузить медиатеку (${response.status}). Попробуйте обновить её.`);
    return response.json();
  }
  function save() {
    if (index < 0) return;
    try {
      localStorage.setItem(storageKey, JSON.stringify({ids: queue.map(t => t.id), index, name: queueName, artwork, time: resumeTime || player.currentTime || 0}));
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
      ? row(item.name, `${labels.count}: ${item.track_count} · открыть`, () => selectPlaylist(item))
      : row(item.title, item.artist || item.name, () => { selection++; setQueue(data, data.indexOf(item), labels.all, null); })));
    $('library-status').textContent = data.length ? `${tab === 'playlists' ? labels.collections : labels.items}: ${data.length}` : query ? 'Ничего не найдено.' : labels.empty;
  }
  function renderQueue() {
    $('queue-list').replaceChildren(...queue.map((track, i) => {
      const li = document.createElement('li');
      const button = row(`${i + 1}. ${track.title}`, track.artist || '', () => { selection++; loadTrack(i, true); });
      button.setAttribute('aria-current', String(i === index)); li.append(button); return li;
    }));
    $('queue-status').textContent = queue.length ? `${index + 1} / ${queue.length} · ${queueName}` : labels.choose;
    $('previous').disabled = index <= 0;
    $('next').disabled = index < 0 || index >= queue.length - 1;
  }
  function setQueue(items, selected, name, cover, autoplay = true, time = 0) {
    queue = items; queueName = name; artwork = cover;
    if (isVideo) {
      player.removeAttribute('poster');
      if (artwork && validID(artwork.id)) player.poster = mediaURL(artwork.id);
    } else {
      $('cover').hidden = true; $('cover-placeholder').hidden = false;
      $('cover').removeAttribute('src');
      if (artwork && validID(artwork.id)) $('cover').src = mediaURL(artwork.id);
    }
    loadTrack(selected, autoplay, time);
  }
  function loadTrack(selected, autoplay, time = 0) {
    if (selected < 0 || selected >= queue.length) return;
    const version = ++sourceVersion;
    player.pause(); index = selected; resumeTime = time;
    error('');
    $('track-title').textContent = queue[index].title;
    $('track-artist').textContent = queue[index].artist || '';
    $('queue-name').textContent = queueName;
    player.src = mediaURL(queue[index].id);
    // Only an explicit play requests media; restoring a queue remains silent.
    player.preload = time > 0 ? 'metadata' : 'none';
    player.load(); renderQueue(); save();
    status(autoplay ? 'Загрузка…' : 'Нажмите Play, чтобы продолжить');
    if (autoplay) player.play().catch(e => {
      if (version !== sourceVersion || e.name === 'AbortError') return;
      if (e.name === 'NotAllowedError') status('Нажмите Play, чтобы начать');
      else error(labels.playError);
    });
  }
  async function selectPlaylist(playlist) {
    const token = ++selection;
    error('');
    $('library-status').textContent = `Загрузка «${playlist.name}»…`;
    try {
      const manifest = await getJSON(`api/playlists/${encodeURIComponent(playlist.id)}`);
      if (token !== selection) return;
      if (!manifest.items.length) throw new Error(labels.emptyCollection);
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
      setQueue(items, selected, typeof saved.name === 'string' ? saved.name : labels.fallback, saved.artwork, false, time);
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
  if (!isVideo) {
    $('cover').addEventListener('load', () => { $('cover').hidden = false; $('cover-placeholder').hidden = true; });
    $('cover').addEventListener('error', () => { $('cover').hidden = true; $('cover-placeholder').hidden = false; });
  }
  $('show-playlists').onclick = () => { tab = 'playlists'; renderLibrary(); };
  $('show-tracks').onclick = () => { tab = 'tracks'; renderLibrary(); };
  $('search').oninput = renderLibrary;
  $('refresh').onclick = () => { error(''); refresh(); };
  $('previous').onclick = () => { selection++; loadTrack(index - 1, true); };
  $('next').onclick = () => { selection++; loadTrack(index + 1, true); };
  player.addEventListener('loadedmetadata', () => {
    if (resumeTime > 0) {
      player.currentTime = Number.isFinite(player.duration) ? Math.min(resumeTime, Math.max(0, player.duration - 0.1)) : resumeTime;
      resumeTime = 0;
    }
  });
  player.addEventListener('playing', () => { error(''); status(labels.playing); });
  player.addEventListener('waiting', () => { if (!player.paused) status('Загрузка…'); });
  player.addEventListener('pause', () => { if (!player.ended) status('Пауза'); save(); });
  player.addEventListener('ended', () => {
    if (index + 1 < queue.length) loadTrack(index + 1, true);
    else { status(labels.finished); save(); }
  });
  player.addEventListener('error', () => {
    status(labels.loadError);
    error(labels.playError);
  });
  player.addEventListener('seeked', save);
  player.addEventListener('timeupdate', () => { if (Date.now() - lastSave > 2000) { lastSave = Date.now(); save(); } });
  window.addEventListener('pagehide', save);
  refresh(true);
})();
