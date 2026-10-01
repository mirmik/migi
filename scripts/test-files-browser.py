"""Chromium checks against the isolated TestBrowserImagePreviewSmoke fixture."""
import argparse
from playwright.sync_api import sync_playwright, expect

parser = argparse.ArgumentParser()
parser.add_argument('--url', required=True)
args = parser.parse_args()

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True)
    page = browser.new_page(viewport={'width': 1400, 'height': 1000})
    errors = []
    page.on('pageerror', lambda error: errors.append(str(error)))
    page.goto(args.url)
    rows = page.locator('.file-row')
    expect(rows).to_have_count(3)
    # Every action must fit in the page, even with long names and expanded metadata.
    for width in (320, 390, 640, 768, 1180, 1400, 1920):
        page.set_viewport_size({'width': width, 'height': 1000})
        for row in rows.all():
            for expanded in (False, True):
                row.locator('details').evaluate('(el, open) => el.open = open', expanded)
                assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'), width
                assert row.evaluate('(el) => el.scrollWidth <= el.clientWidth'), width
                for action in row.locator('.file-actions a').all():
                    expect(action).to_be_visible()
                    box = action.bounding_box()
                    assert box['x'] >= 0 and box['x'] + box['width'] <= width, (width, box)
        rows.locator('details').evaluate_all('(elements) => elements.forEach(el => el.open = false)')
    # Disclosure works without extra JavaScript; downloading a regular file still works.
    rows.last.locator('summary').click()
    expect(rows.last.locator('dd').nth(1)).to_contain_text('agent:long-source')
    rows.last.locator('summary').click()
    with page.expect_download() as download_info:
        rows.last.locator('.file-download').click()
    download = download_info.value
    assert download.failure() is None
    with open(download.path(), 'rb') as downloaded:
        assert downloaded.read() == b'note'
    page.set_viewport_size({'width': 1400, 'height': 1000})
    thumbs = page.locator('.file-thumbnail')
    expect(thumbs).to_have_count(2)
    page.wait_for_function('() => document.querySelector(".file-thumbnail img").naturalWidth === 320')
    expect(thumbs.nth(1).locator('img')).to_be_hidden()
    expect(thumbs.nth(1).locator('span')).to_be_visible()
    thumbs.first.click()
    expect(page.locator('#image-stage img')).to_be_visible()
    page.locator('#image-close').click()
    links = page.locator('.image-preview-link')
    expect(links).to_have_count(2)
    dialog = page.locator('#image-viewer')
    links.first.click()
    expect(dialog).to_be_visible()
    expect(page.locator('#image-title')).to_have_text('Picture <img src=x onerror=alert(1)>.png')
    expect(page.locator('#image-title img')).to_have_count(0)
    picture = page.locator('#image-stage img')
    expect(picture).to_be_visible()
    expect(page.locator('#image-status')).to_be_hidden()
    assert picture.evaluate('(img) => img.naturalWidth') == 1600
    picture.click()
    expect(picture).to_have_class('actual-size')
    picture.click()
    expect(picture).not_to_have_class('actual-size')
    assert page.locator('#image-download').get_attribute('href') == '../files/picture/content'
    page.keyboard.press('Escape')
    expect(dialog).not_to_be_visible()
    expect(picture).to_have_count(0)
    expect(links.first).to_be_focused()
    links.nth(1).click()
    expect(page.locator('#image-status')).to_contain_text('Cannot display')
    page.locator('#image-close').click()
    expect(dialog).not_to_be_visible()
    links.first.click()
    expect(picture).to_be_visible()
    expect(page.locator('#image-status')).to_be_hidden()
    # A pending load can be closed and another image opened without stale events.
    page.locator('#image-close').click()
    page.route('**/picture/preview', lambda route: route.abort())
    links.first.click()
    expect(page.locator('#image-status')).to_contain_text('Cannot display')
    page.locator('#image-close').click()
    page.unroute('**/picture/preview')
    page.set_viewport_size({'width': 390, 'height': 844})
    links.first.click()
    expect(picture).to_be_visible()
    assert dialog.evaluate('(el) => el.getBoundingClientRect().width') <= 390
    assert picture.evaluate('(el) => el.getBoundingClientRect().width') <= 390
    # Close by tapping the backdrop.
    page.mouse.click(2, 2)
    expect(dialog).not_to_be_visible()
    assert not errors, errors
    browser.close()
print('PASS: responsive list 320–1920 px, long name/metadata, visible actions, details, download, thumbnail click/fallback, preview, zoom, escaping, Escape/focus, close, error/recovery, mobile, proxy prefix')
