"""Verify decoded 440/880 Hz tracks, position, pause, subtitles and preferences."""
import argparse
import os
from playwright.sync_api import sync_playwright, expect

parser = argparse.ArgumentParser()
parser.add_argument('--url', required=True)
args = parser.parse_args()
engine = os.environ.get('MIGI_BROWSER_ENGINE', 'chromium')
with sync_playwright() as p:
    options = {'executable_path': os.environ.get('MIGI_BROWSER_CHROMIUM'), 'args': ['--mute-audio']} if engine == 'chromium' else {'firefox_user_prefs': {'media.volume_scale': '0.0'}}
    browser = getattr(p, engine).launch(headless=True, **options)
    page = browser.new_page()
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.goto(args.url)
    page.get_by_role('button', name='Local album').click()
    page.wait_for_function('() => document.querySelector("video").currentTime > 0.1')
    expect(page.locator('#audio-tracks option')).to_have_count(3)
    page.evaluate('''async () => {
      const video = document.querySelector('video');
      const context = new AudioContext();
      const source = context.createMediaElementSource(video);
      const analyser = context.createAnalyser(); analyser.fftSize = 4096;
      const gain = context.createGain(); gain.gain.value = 0;
      source.connect(analyser); analyser.connect(gain); gain.connect(context.destination);
      window.audioTest = {context, analyser};
      window.audioPeak = () => {
        const bins = new Float32Array(analyser.frequencyBinCount);
        analyser.getFloatFrequencyData(bins);
        let peak = 0;
        for (let i = 1; i < bins.length; i++) if (bins[i] > bins[peak]) peak = i;
        return bins[peak] > -60 ? peak * context.sampleRate / analyser.fftSize : 0;
      };
      await context.resume();
    }''')
    def peak(frequency):
        page.wait_for_function('(f) => Math.abs(window.audioPeak() - f) < 35', arg=frequency)
    def ready():
        page.wait_for_function('() => {const v = document.querySelector("video");return v.readyState >= 2 && !v.seeking;}')
    peak(440)
    page.select_option('#playback-speed', value='1.5')
    assert page.locator('video').evaluate('v => v.playbackRate === 1.5 && v.preservesPitch')
    page.select_option('#subtitles', label='English')
    page.wait_for_function('() => document.querySelector("video track")?.track.activeCues?.[0]?.text === "Subtitle check"')
    page.locator('video').evaluate('v => {v.pause();v.currentTime = 3;}')
    ready()
    page.select_option('#audio-tracks', label='Английский')
    ready()
    assert page.locator('video').evaluate('v => v.paused && Math.abs(v.currentTime - 3) < 0.2 && v.currentSrc.includes("audio=track-3")')
    page.locator('video').evaluate('v => v.play()')
    peak(880)
    assert page.locator('video').evaluate('v => v.playbackRate === 1.5')
    page.wait_for_function('() => document.querySelector("video track")?.track.activeCues?.[0]?.text === "Subtitle check"')
    position = page.locator('video').evaluate('v => v.currentTime')
    page.select_option('#audio-tracks', label='Японский')
    peak(440)
    assert page.locator('video').evaluate('(v,t) => !v.paused && v.currentTime >= t - 0.2', position)
    page.select_option('#subtitles', value='')
    page.select_option('#audio-tracks', label='Английский')
    peak(880)
    assert page.locator('video track').count() == 0
    page.locator('#next').click()
    expect(page.locator('#track-title')).to_have_text('Second track')
    peak(880)
    expect(page.locator('#audio-tracks')).to_have_value('track-3')
    expect(page.locator('#playback-speed')).to_have_value('1.5')
    page.get_by_role('button', name='Remote album').click()
    expect(page.locator('#track-title')).to_have_text('Remote track')
    peak(880)
    page.locator('video').evaluate('v => {v.pause();v.currentTime = 2;}')
    page.wait_for_function('() => JSON.parse(localStorage.getItem("migi.video.v1")).time >= 1.9')
    requests = []
    page.on('request', lambda r: requests.append(r.url))
    page.reload()
    expect(page.locator('#track-title')).to_have_text('Remote track')
    ready()
    assert page.locator('video').evaluate('v => v.paused && v.currentTime >= 1.9')
    assert not any(url.endswith('/audio') for url in requests)
    # Restoring a preferred track replaces the initial source and aborts its play promise.
    page.locator('video').evaluate('v => {v.play().catch(e => {if (e.name !== "AbortError") throw e;});}')
    page.wait_for_function('() => document.querySelector("video").currentSrc.includes("audio=track-3") && document.querySelector("video").currentTime >= 1.9')
    expect(page.locator('#audio-tracks')).to_have_value('track-3')
    page.select_option('#audio-tracks', value='')
    ready()
    assert page.locator('video').evaluate('v => !v.currentSrc.includes("audio=")')
    expect(page.locator('#video-error')).to_be_hidden()
    assert not errors, errors
    browser.close()
print('PASS: actual decoded audio frequencies, local/origin selection, pause/position, subtitles, episode and reload preferences')
