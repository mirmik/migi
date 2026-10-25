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
  let compatible = false, preparationVersion = -1, preparationReady = -1, preparationRequest = null, preparationPromise = null;
  function playbackURL(suffix = '', audioID = '') {
    const query = new URLSearchParams();
    if (compatible) query.set('compatible', '1');
    if (audioID) query.set('audio', audioID);
    return mediaURL(queue[index].id) + suffix + (query.size ? `?${query}` : '');
  }
  function resetPreparation() {
    if (!isVideo) return;
    if (preparationRequest) preparationRequest.abort();
    compatible = false; preparationVersion = -1; preparationReady = -1; preparationPromise = null;
    $('video-preparation').hidden = true;
  }
  function preparePlayback(version) {
    if (preparationVersion === version) return preparationPromise;
    preparationVersion = version;
    const controller = new AbortController(); preparationRequest = controller;
    const base = `${mediaURL(queue[index].id)}/prepare`;
    const codecs = {'mp4a': 'mp4a.40.2', 'ec-3': 'ec-3', 'ac-3': 'ac-3', 'Opus': 'opus', '.mp3': 'mp3'};
    const supported = Object.keys(codecs).filter(codec => player.canPlayType(`audio/mp4; codecs="${codecs[codec]}"`));
    const begin = () => fetch(base, {method: 'POST', signal: controller.signal, headers: {
      'Content-Type': 'application/json', 'X-CSRF-Token': document.querySelector('meta[name="csrf-token"]').content
    }, body: JSON.stringify({supported_audio: supported})});
    preparationPromise = (async () => {
      try {
        let response = await begin();
        while (version === sourceVersion) {
          if (!response.ok) throw new Error(response.status === 403 ? 'Обновите страницу и повторите запуск видео.' : 'Не удалось подготовить видео. Повторите запуск.');
          const result = await response.json();
          if (version !== sourceVersion) return false;
          if (result.state === 'ready' || result.state === 'direct') {
            compatible = result.state === 'ready';
            if (compatible) {
              const time = resumeTime || player.currentTime || 0;
              player.pause(); resumeTime = time;
              player.src = playbackURL(); player.load();
              resetSubtitles();
            }
            preparationReady = version; $('video-preparation').hidden = true;
            return true;
          }
          if (result.state === 'error') throw new Error(result.message);
          $('video-preparation').hidden = false; $('preparation-retry').hidden = true;
          $('preparation-status').textContent = result.state === 'processing' ? 'Подготовка звука для браузера…' : 'Ожидание подготовки видео…';
          $('preparation-progress').hidden = false;
          if (result.progress) $('preparation-progress').value = result.progress;
          else $('preparation-progress').removeAttribute('value');
          await new Promise((resolve, reject) => {
            const aborted = () => { clearTimeout(timer); reject(new DOMException('Aborted', 'AbortError')); };
            const timer = setTimeout(() => { controller.signal.removeEventListener('abort', aborted); resolve(); }, 900);
            if (controller.signal.aborted) aborted();
            else controller.signal.addEventListener('abort', aborted, {once: true});
          });
          response = result.state === 'busy' || result.state === 'idle' ? await begin() : await fetch(base, {signal: controller.signal, cache: 'no-store'});
        }
      } catch (e) {
        if (version !== sourceVersion || e.name === 'AbortError') return false;
        $('video-preparation').hidden = false; $('preparation-progress').hidden = true;
        $('preparation-status').textContent = e.message; $('preparation-retry').hidden = false;
        status('Подготовка видео не завершена');
      }
      return false;
    })();
    return preparationPromise;
  }
  async function startVideo(version) {
    if (!await preparePlayback(version) || version !== sourceVersion) return;
    await prepareAudio(version); requestPlay(version);
  }
  let audioRequest = null, audioVersion = -1, audioReady = null, audioTracks = [], currentAudio = '';
  let audioPreference = {language: '', label: '', ordinal: 0};
  if (isVideo) {
    try {
      const saved = JSON.parse(localStorage.getItem(`${storageKey}.audio`));
      if (saved && typeof saved.language === 'string' && typeof saved.label === 'string') audioPreference = saved;
    } catch (_) { /* Automatic selection when storage is unavailable. */ }
  }
  function audioStatus(message, retry = false) {
    $('audio-track-status').textContent = message;
    $('audio-track-retry').hidden = !retry;
  }
  function resetAudio() {
    if (!isVideo) return;
    if (audioRequest) audioRequest.abort();
    audioRequest = null; audioVersion = -1; audioReady = null; audioTracks = []; currentAudio = '';
    $('audio-tracks').replaceChildren(new Option('Нажмите Play для загрузки', ''));
    $('audio-tracks').disabled = true; audioStatus('');
  }
  function requestPlay(version) {
    if (version !== sourceVersion) return;
    player.play().catch(e => {
      if (version !== sourceVersion || e.name === 'AbortError') return;
      if (e.name === 'NotAllowedError') status('Нажмите Play, чтобы начать');
      else error(labels.playError);
    });
  }
  function chooseAudio(remember = false) {
    const selected = audioTracks.find(track => track.id === $('audio-tracks').value);
    if (remember) {
      audioPreference = selected ? {
        language: selected.language || '', label: selected.label,
        ordinal: audioTracks.filter(track => track.language === selected.language && track.label === selected.label).findIndex(track => track.id === selected.id)
      } : {language: '', label: '', ordinal: 0};
      try { localStorage.setItem(`${storageKey}.audio`, JSON.stringify(audioPreference)); } catch (_) { /* Storage is optional. */ }
    }
    const id = selected ? selected.id : '';
    if (id === currentAudio) return;
    const playing = !player.paused && !player.ended;
    const time = resumeTime || player.currentTime || 0;
    player.pause(); resumeTime = time; currentAudio = id;
    player.src = playbackURL('', id);
    player.preload = time > 0 ? 'metadata' : 'none';
    player.load(); save();
    if (subtitleVersion === sourceVersion) chooseSubtitle();
    if (playing) requestPlay(sourceVersion);
  }
  function prepareAudio(version) {
    if (audioVersion === version) return audioReady;
    audioVersion = version;
    const controller = new AbortController(); audioRequest = controller;
    audioStatus('Поиск звуковых дорожек…');
    audioReady = (async () => {
      try {
        const response = await fetch(playbackURL('/audio'), {signal: controller.signal, cache: 'no-store'});
        if (!response.ok) throw new Error('audio unavailable');
        const info = await response.json();
        if (version !== sourceVersion) return;
        audioTracks = info.tracks;
        const options = audioTracks.map((track, i) => new Option(`${track.label}${audioTracks.some((other, j) => j < i && other.label === track.label) ? ` (${i + 1})` : ''}`, track.id));
        $('audio-tracks').replaceChildren(new Option('Автоматически', ''), ...options);
        $('audio-tracks').disabled = !audioTracks.length;
        const matches = audioTracks.filter(track => track.language === audioPreference.language && track.label === audioPreference.label);
        const preferred = matches[Number.isInteger(audioPreference.ordinal) ? audioPreference.ordinal : 0]
          || (audioPreference.language && audioTracks.find(track => track.language === audioPreference.language));
        $('audio-tracks').value = preferred ? preferred.id : '';
        chooseAudio();
        audioStatus(info.supported ? '' : 'Для выбора звука используйте версию MP4.');
      } catch (e) {
        if (version !== sourceVersion || e.name === 'AbortError') return;
        audioStatus('Не удалось проверить звуковые дорожки.', true);
      }
    })();
    return audioReady;
  }
  let subtitleRequest = null, subtitleVersion = -1, subtitleTracks = [], activeSubtitle = null;
  let subtitlePreference = {enabled: true, language: 'ru'};
  if (isVideo) {
    try {
      const saved = JSON.parse(localStorage.getItem(`${storageKey}.subtitles`));
      if (saved && typeof saved.enabled === 'boolean' && typeof saved.language === 'string') subtitlePreference = saved;
    } catch (_) { /* Use Russian subtitles by default. */ }
  }
  function subtitleStatus(message, retry = false) {
    $('subtitle-status').textContent = message;
    $('subtitle-retry').hidden = !retry;
  }
  function removeSubtitle() {
    if (activeSubtitle) {
      activeSubtitle.track.mode = 'disabled';
      activeSubtitle.remove(); activeSubtitle = null;
    }
  }
  function resetSubtitles() {
    if (!isVideo) return;
    if (subtitleRequest) subtitleRequest.abort();
    subtitleRequest = null; subtitleVersion = -1; subtitleTracks = [];
    removeSubtitle();
    $('subtitles').replaceChildren(new Option('Нажмите Play для загрузки', ''));
    $('subtitles').disabled = true; subtitleStatus('');
  }
  function chooseSubtitle(remember = false) {
    const selected = subtitleTracks.find(track => track.id === $('subtitles').value);
    removeSubtitle(); subtitleStatus('');
    if (remember) {
      subtitlePreference = selected ? {enabled: true, language: selected.language || '', label: selected.label} : {...subtitlePreference, enabled: false};
      try { localStorage.setItem(`${storageKey}.subtitles`, JSON.stringify(subtitlePreference)); } catch (_) { /* Storage is optional. */ }
    }
    if (!selected) return;
    const version = sourceVersion;
    const track = document.createElement('track');
    track.kind = 'subtitles'; track.label = selected.label;
    track.srclang = selected.language || 'und'; track.default = true;
    track.src = playbackURL(`/subtitles/${encodeURIComponent(selected.id)}`);
    track.onload = () => { if (version === sourceVersion && activeSubtitle === track) subtitleStatus(''); };
    track.onerror = () => { if (version === sourceVersion && activeSubtitle === track) subtitleStatus('Не удалось загрузить субтитры.', true); };
    activeSubtitle = track; subtitleStatus('Загрузка субтитров…');
    player.append(track); track.track.mode = 'showing';
  }
  async function loadSubtitles() {
    if (!isVideo || index < 0 || subtitleVersion === sourceVersion || preparationReady !== sourceVersion) return;
    const version = sourceVersion;
    subtitleVersion = version;
    const controller = new AbortController(); subtitleRequest = controller;
    subtitleStatus('Поиск субтитров…');
    try {
      const response = await fetch(playbackURL('/subtitles'), {signal: controller.signal, cache: 'no-store'});
      if (!response.ok) throw new Error('subtitles unavailable');
      const tracks = await response.json();
      if (version !== sourceVersion) return;
      subtitleTracks = tracks;
      const options = tracks.map((track, i) => new Option(`${track.label}${tracks.some((other, j) => j < i && other.label === track.label) ? ` (${i + 1})` : ''}`, track.id));
      $('subtitles').replaceChildren(new Option(tracks.length ? 'Выключены' : 'Нет доступных субтитров', ''), ...options);
      $('subtitles').disabled = !tracks.length;
      if (subtitlePreference.enabled) {
        const preferred = tracks.find(track => track.language === subtitlePreference.language && track.label === subtitlePreference.label)
          || tracks.find(track => track.language === subtitlePreference.language)
          || tracks.find(track => track.language === 'ru') || tracks.find(track => track.default) || tracks[0];
        $('subtitles').value = preferred ? preferred.id : '';
      }
      chooseSubtitle();
    } catch (e) {
      if (version !== sourceVersion || e.name === 'AbortError') return;
      $('subtitles').replaceChildren(new Option('Субтитры недоступны', ''));
      $('subtitles').disabled = true;
      subtitleStatus('Не удалось проверить субтитры.', true);
    }
  }

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
    resetPreparation();
    resetAudio();
    resetSubtitles();
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
    if (autoplay) {
      if (isVideo) startVideo(version);
      else requestPlay(version);
    }
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
  } else {
    $('preparation-retry').onclick = () => { preparationVersion = -1; startVideo(sourceVersion); };
    $('audio-tracks').onchange = () => chooseAudio(true);
    $('audio-track-retry').onclick = () => { audioVersion = -1; prepareAudio(sourceVersion); };
    player.addEventListener('play', () => {
      if (preparationReady === sourceVersion && audioVersion === sourceVersion) return;
      const version = sourceVersion;
      player.pause(); startVideo(version);
    });
    $('subtitles').onchange = () => chooseSubtitle(true);
    $('subtitle-retry').onclick = () => {
      if (subtitleTracks.length) chooseSubtitle();
      else { subtitleVersion = -1; loadSubtitles(); }
    };
    player.addEventListener('play', loadSubtitles);
    player.textTracks.addEventListener('change', () => {
      if (!activeSubtitle || activeSubtitle.readyState !== 2) return;
      const enabled = activeSubtitle.track.mode === 'showing';
      if (enabled && !$('subtitles').value) {
        const selected = subtitleTracks.find(track => new URL(activeSubtitle.src).pathname.endsWith(`/subtitles/${encodeURIComponent(track.id)}`));
        if (selected) { $('subtitles').value = selected.id; subtitlePreference = {enabled: true, language: selected.language || '', label: selected.label}; }
        try { localStorage.setItem(`${storageKey}.subtitles`, JSON.stringify(subtitlePreference)); } catch (_) { /* Storage is optional. */ }
      }
      if (!enabled && $('subtitles').value) {
        $('subtitles').value = ''; subtitlePreference.enabled = false;
        try { localStorage.setItem(`${storageKey}.subtitles`, JSON.stringify(subtitlePreference)); } catch (_) { /* Storage is optional. */ }
      }
    });
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
