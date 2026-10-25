"""Original H.264/E-AC-3 MKV -> automatic cached browser playback."""
import argparse
import os
from playwright.sync_api import sync_playwright, expect

parser=argparse.ArgumentParser()
parser.add_argument('--url',required=True)
args=parser.parse_args()
engine=os.environ.get('MIGI_BROWSER_ENGINE','firefox')
with sync_playwright() as p:
    options={'firefox_user_prefs':{'media.volume_scale':'0.0'}} if engine=='firefox' else {'executable_path':os.environ.get('MIGI_BROWSER_CHROMIUM'),'args':['--mute-audio']}
    browser=getattr(p,engine).launch(headless=True,**options)
    page=browser.new_page()
    errors=[]
    page.on('pageerror',lambda e:errors.append(str(e)))
    page.goto(args.url)
    page.select_option('#playback-speed', value='1.75')
    page.get_by_role('button',name='Original album').click()
    page.wait_for_function('()=>{const v=document.querySelector("video");return v.currentSrc.includes("compatible=1") && v.currentTime>0.1;}',timeout=40000)
    page.wait_for_function('()=>{const v=document.querySelector("video");return v.mozHasAudio===true || v.webkitAudioDecodedByteCount>0;}')
    expect(page.locator('#video-preparation')).to_be_hidden()
    assert page.locator('video').evaluate('v => v.playbackRate === 1.75')
    expect(page.locator('#audio-tracks option')).to_have_count(3)
    page.wait_for_function('()=>document.querySelector("video track")?.track.activeCues?.[0]?.text === "Проверка субтитров"')
    page.select_option('#audio-tracks',label='Английский')
    page.wait_for_function('()=>{const v=document.querySelector("video");return v.currentSrc.includes("audio=track-3") && v.currentTime>0.1 && !v.paused;}')
    page.locator('video').evaluate('v=>{v.pause();v.currentTime=3;}')
    page.wait_for_function('()=>document.querySelector("video").currentTime>=3 && !document.querySelector("video").seeking')
    page.select_option('#audio-tracks',label='Японский')
    page.wait_for_function('()=>{const v=document.querySelector("video");return v.readyState>=2 && !v.seeking && v.currentSrc.includes("audio=track-2");}')
    assert page.locator('video').evaluate('v=>v.paused && Math.abs(v.currentTime-3)<0.2')
    assert page.locator('video').evaluate('v=>v.playbackRate===1.75')
    page.select_option('#subtitles',label='English')
    page.wait_for_function('()=>document.querySelector("video track")?.track.activeCues?.[0]?.text === "Subtitle check"')
    page.select_option('#subtitles',value='')
    expect(page.locator('video track')).to_have_count(0)
    # Re-selecting the original catalog entry must reuse the prepared view.
    page.locator('#queue-list button').click()
    page.wait_for_function('()=>document.querySelector("video").currentSrc.includes("compatible=1") && document.querySelector("video").currentTime>0.1')
    expect(page.locator('#video-preparation')).to_be_hidden()
    expect(page.locator('#video-error')).to_be_hidden()
    assert not errors,errors
    browser.close()
print('PASS: original remote E-AC-3 MKV automatically prepares, plays with audio, switches languages/subtitles, seeks and reuses cache')
