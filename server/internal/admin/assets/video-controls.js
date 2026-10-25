(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const video = $('video'), stage = $('video-stage'), toolbar = $('video-toolbar'), settings = $('video-settings');
  if (!video || !stage || !toolbar || !settings) return;
  let hideTimer, dragging = false, keyboard = false;
  const cuePositions = new WeakMap();
  function placeSubtitles() {
    const hidden = stage.classList.contains('controls-hidden');
    // Keep automatic bottom captions above the playback bar while it is visible.
    const line = Math.max(10, Math.min(90, 100 * (1 - (toolbar.offsetHeight + 12) / Math.max(1, video.clientHeight))));
    for (const track of video.textTracks) {
      for (const cue of track.activeCues || []) {
        if (!cuePositions.has(cue)) cuePositions.set(cue, {line: cue.line, snap: cue.snapToLines, align: cue.lineAlign});
        const original = cuePositions.get(cue);
        if (original.line !== 'auto') continue;
        cue.snapToLines = hidden ? original.snap : false;
        cue.lineAlign = hidden ? original.align : 'end';
        cue.line = hidden ? original.line : line;
      }
    }
  }
  video.textTracks.addEventListener('addtrack', event => { event.track.addEventListener('cuechange', placeSubtitles); placeSubtitles(); });
  video.addEventListener('load', placeSubtitles, true);
  new ResizeObserver(placeSubtitles).observe(video);
  const time = seconds => {
    const value = Number.isFinite(seconds) ? Math.max(0, Math.floor(seconds)) : 0;
    const hours = Math.floor(value / 3600), minutes = Math.floor(value / 60) % 60, remainder = value % 60;
    return `${hours ? `${hours}:${String(minutes).padStart(2, '0')}` : minutes}:${String(remainder).padStart(2, '0')}`;
  };
  function showControls() {
    stage.classList.remove('controls-hidden');
    placeSubtitles();
    clearTimeout(hideTimer);
    if (!video.paused && settings.hidden) hideTimer = setTimeout(() => {
      if (video.paused || !settings.hidden || toolbar.matches(':hover') || keyboard && stage.contains(document.activeElement)) return;
      stage.classList.add('controls-hidden');
      placeSubtitles();
    }, 3000);
  }
  function closeSettings(focus = false) {
    settings.hidden = true; $('video-settings-button').setAttribute('aria-expanded', 'false');
    if (focus) $('video-settings-button').focus();
    showControls();
  }
  function toggleSettings() {
    if (!settings.hidden) { closeSettings(true); return; }
    settings.hidden = false; $('video-settings-button').setAttribute('aria-expanded', 'true');
    showControls(); $('playback-speed').focus();
  }
  function updatePlayback() {
    const playing = !video.paused && !video.ended;
    $('video-play').setAttribute('aria-label', playing ? 'Пауза' : 'Воспроизвести');
    $('video-play-icon').setAttribute('d', playing ? 'M8 5v14M16 5v14' : 'M8 5v14l11-7z');
    showControls();
  }
  function updateTime() {
    const duration = Number.isFinite(video.duration) ? video.duration : 0;
    $('video-seek').disabled = duration <= 0;
    $('video-seek').max = String(duration);
    if (!dragging) $('video-seek').value = String(video.currentTime || 0);
    const position = dragging ? Number($('video-seek').value) : video.currentTime;
    $('video-time').textContent = `${time(position)} / ${time(duration)}`;
    $('video-seek').setAttribute('aria-valuetext', `${time(position)} из ${time(duration)}`);
  }
  function updateVolume() {
    const muted = video.muted || video.volume === 0;
    $('video-mute').setAttribute('aria-label', muted ? 'Включить звук' : 'Выключить звук');
    $('video-mute').setAttribute('aria-pressed', String(muted));
    $('video-volume').value = String(muted ? 0 : video.volume);
    $('video-volume-icon').setAttribute('d', muted ? 'm16 9 6 6m0-6-6 6' : 'M16 8a6 6 0 0 1 0 8M19 5a10 10 0 0 1 0 14');
  }
  function togglePlay() {
    if (video.paused || video.ended) {
      if (video.ended) video.currentTime = 0;
      video.play().catch(() => { /* Media-player owns load/autoplay errors. */ });
    } else video.pause();
    showControls();
  }
  async function toggleFullscreen() {
    try {
      if (document.fullscreenElement === stage) await document.exitFullscreen();
      else if (stage.requestFullscreen) await stage.requestFullscreen();
      else if (video.webkitEnterFullscreen) video.webkitEnterFullscreen();
    } catch (_) { showControls(); }
  }
  $('video-play').onclick = togglePlay;
  $('video-mute').onclick = () => { video.muted = !video.muted; if (!video.muted && video.volume === 0) video.volume = 1; };
  $('video-volume').oninput = () => { video.volume = Number($('video-volume').value); video.muted = video.volume === 0; };
  $('video-seek').oninput = () => { dragging = true; updateTime(); showControls(); };
  $('video-seek').onchange = () => {
    if (Number.isFinite(video.duration)) video.currentTime = Math.min(Number($('video-seek').value), video.duration);
    dragging = false; updateTime(); showControls();
  };
  $('video-seek').addEventListener('pointercancel', () => { dragging = false; updateTime(); });
  $('video-settings-button').onclick = toggleSettings;
  $('video-settings-close').onclick = () => closeSettings(true);
  $('video-fullscreen').onclick = toggleFullscreen;
  video.addEventListener('click', () => {
    if (!settings.hidden) closeSettings();
    else if (matchMedia('(pointer: coarse)').matches && !video.paused) showControls();
    else togglePlay();
  });
  video.addEventListener('dblclick', event => { event.preventDefault(); toggleFullscreen(); });
  for (const event of ['play', 'pause', 'ended']) video.addEventListener(event, updatePlayback);
  for (const event of ['loadedmetadata', 'durationchange', 'timeupdate', 'emptied']) video.addEventListener(event, updateTime);
  video.addEventListener('volumechange', updateVolume);
  video.addEventListener('loadstart', () => { dragging = false; updateTime(); showControls(); });
  stage.addEventListener('pointermove', showControls);
  stage.addEventListener('pointerdown', () => { keyboard = false; showControls(); });
  stage.addEventListener('focusin', showControls);
  stage.addEventListener('keydown', event => {
    keyboard = true; showControls();
    if (event.key === 'Escape' && !settings.hidden) { event.preventDefault(); closeSettings(true); return; }
    if (event.target.matches('input,select,button')) return;
    if (event.key === ' ' || event.key.toLowerCase() === 'k') { event.preventDefault(); togglePlay(); }
    else if (event.key.toLowerCase() === 'f') { event.preventDefault(); toggleFullscreen(); }
    else if (event.key.toLowerCase() === 'm') { event.preventDefault(); $('video-mute').click(); }
    else if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      if (!Number.isFinite(video.duration)) return;
      event.preventDefault(); video.currentTime = Math.max(0, Math.min(video.duration, video.currentTime + (event.key === 'ArrowRight' ? 5 : -5)));
    }
  });
  document.addEventListener('pointerdown', event => { if (!settings.hidden && !settings.contains(event.target) && !$('video-settings-button').contains(event.target)) closeSettings(); });
  document.addEventListener('focusin', event => { if (!settings.hidden && !stage.contains(event.target)) closeSettings(); });
  document.addEventListener('fullscreenchange', () => {
    const fullscreen = document.fullscreenElement === stage;
    $('video-fullscreen').setAttribute('aria-label', fullscreen ? 'Выйти из полного экрана' : 'Полный экран');
    $('video-fullscreen').setAttribute('aria-pressed', String(fullscreen));
    closeSettings();
  });
  updateTime(); updateVolume(); updatePlayback();
  toolbar.hidden = false; video.controls = false; stage.classList.add('custom-controls');
})();
