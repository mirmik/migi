"""Real audio/video playback checks for the isolated Go browser fixtures."""
import argparse
import os
import tempfile
from pathlib import Path
from playwright.sync_api import sync_playwright, expect

parser = argparse.ArgumentParser()
parser.add_argument('--url', required=True, help='Isolated Go test fixture URL')
parser.add_argument('--kind', choices=['music', 'video'], default='music')
args = parser.parse_args()
media = 'video' if args.kind == 'video' else 'audio'
error_id = '#video-error' if args.kind == 'video' else '#music-error'
storage_key = f'migi.{args.kind}.v1'
other_key = 'migi.music.v1' if args.kind == 'video' else 'migi.video.v1'
output = Path(tempfile.mkdtemp(prefix=f'migi-{args.kind}-browser-'))
with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, executable_path=os.environ.get("MIGI_BROWSER_CHROMIUM"))
    page = browser.new_page(viewport={'width': 1400, 'height': 1000})
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.goto(args.url)
    page.evaluate('(key) => localStorage.setItem(key, "other-player-state")', other_key)
    expect(page.locator('#library-list button')).to_have_count(2)
    assert page.evaluate(f'document.querySelector("{media}").paused')
    page.get_by_role('button', name='Local album').click()
    expect(page.locator('#queue-list button')).to_have_count(2)
    page.wait_for_function(f'() => !document.querySelector("{media}").paused && document.querySelector("{media}").currentTime > 0.1')
    expect(page.locator('#track-title')).to_have_text('Local track')
    if media == 'video':
        assert page.evaluate('document.querySelector("video").videoWidth') == 160
        page.evaluate('document.querySelector("video").requestFullscreen()')
        page.wait_for_function('() => document.fullscreenElement !== null')
        page.evaluate('document.exitFullscreen()')
    # Native media controls drive these same browser playback operations.
    page.evaluate(f'document.querySelector("{media}").pause()')
    expect(page.locator('#playback-status')).to_have_text('Пауза')
    page.evaluate(f'document.querySelector("{media}").currentTime = 3')
    page.wait_for_function(f'() => Math.abs(document.querySelector("{media}").currentTime - 3) < 0.2')
    page.locator('#next').click()
    expect(page.locator('#track-title')).to_have_text('Second track')
    page.locator('#previous').click()
    expect(page.locator('#track-title')).to_have_text('Local track')
    page.wait_for_function(f'() => document.querySelector("{media}").readyState >= 2')
    page.evaluate(f'document.querySelector("{media}").currentTime = 7.7')
    expect(page.locator('#track-title')).to_have_text('Second track', timeout=10000)
    page.wait_for_function(f'() => document.querySelector("{media}").readyState >= 2')
    page.evaluate(f'document.querySelector("{media}").pause(); document.querySelector("{media}").currentTime = 2')
    page.wait_for_function(f'() => JSON.parse(localStorage.getItem("{storage_key}")).time >= 1.9')
    page.reload()
    expect(page.locator('#track-title')).to_have_text('Second track')
    page.wait_for_function(f'() => document.querySelector("{media}").currentTime >= 1.9')
    assert page.evaluate(f'document.querySelector("{media}").paused')
    # Remote media goes through the actual origin request/upload relay.
    page.get_by_role('button', name='Remote album').click()
    expect(page.locator('#track-title')).to_have_text('Remote track')
    page.wait_for_function(f'() => !document.querySelector("{media}").paused && document.querySelector("{media}").currentTime > 0.1')
    page.evaluate(f'document.querySelector("{media}").currentTime = 5')
    page.wait_for_function(f'() => document.querySelector("{media}").currentTime >= 5')
    page.evaluate(f'document.querySelector("{media}").pause()')
    expect(page.locator(error_id)).to_be_hidden()
    page.screenshot(path=str(output / 'desktop.png'), full_page=True)
    page.locator('#show-tracks').click()
    expect(page.locator('#library-list button')).to_have_count(3)
    page.locator('#search').fill('Second')
    expect(page.locator('#library-list button')).to_have_count(1)
    page.locator('#library-list button').click()
    expect(page.locator('#track-title')).to_have_text('Second track')
    expect(page.locator('#queue-list button')).to_have_count(1)
    page.evaluate(f'document.querySelector("{media}").pause()')
    page.set_viewport_size({'width': 390, 'height': 844})
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
    page.screenshot(path=str(output / 'mobile.png'), full_page=True)
    # A failed stream and failed catalog refresh have visible, recoverable errors.
    page.route('**/api/media/*', lambda route: route.fulfill(status=503, body='offline'))
    page.locator('#queue-list button').click()
    expect(page.locator(error_id)).to_be_visible()
    page.unroute('**/api/media/*')
    page.locator('#queue-list button').click()
    page.wait_for_function(f'() => !document.querySelector("{media}").paused && document.querySelector("{media}").currentTime > 0.1')
    expect(page.locator(error_id)).to_be_hidden()
    page.route('**/api/library', lambda route: route.fulfill(status=503, body='offline'))
    page.locator('#refresh').click()
    expect(page.locator(error_id)).to_contain_text('503')
    page.unroute('**/api/library')
    page.locator('#refresh').click()
    expect(page.locator(error_id)).to_be_hidden()
    assert page.evaluate('(key) => localStorage.getItem(key)', other_key) == 'other-player-state'
    assert not errors, errors
    browser.close()
print(f'PASS: local/origin playback, pause, seek, queue, auto-next, restore, search, errors, mobile. Screenshots: {output}')
