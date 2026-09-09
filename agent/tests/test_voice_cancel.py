import asyncio
import threading
from aiohttp.test_utils import TestClient, TestServer
from migi_agent.service import create_app, SERVICE
from test_chat import response


def test_voice_cancel_tombstone_restart_and_late_stop(tmp_path):
    started = threading.Event()
    def model(stop_event, **kwargs):
        started.set()
        assert stop_event.wait(4)
        return response('stopped')
    async def scenario():
        owner = 'device:test'
        app = create_app(tmp_path, tmp_path, completion=model)
        async with TestClient(TestServer(app)) as client:
            async def cancel(rid):
                r = await client.post('/migi/voice/cancel', json={'owner': owner, 'request_id': rid})
                assert r.status == 200
                return await r.json()
            async def act(action, rid, thread=owner):
                return await client.post('/migi/chat', json={'owner': owner, 'thread_id': thread, 'action': action, 'request_id': rid, 'text': 'test'})
            assert not (await cancel('before-submit'))['pending']
            assert (await act('voice', 'before-submit')).status == 409
            assert (await act('voice', 'voice-1')).status == 200
            assert await asyncio.to_thread(started.wait, 1)
            assert (await cancel('voice-1'))['pending']
            await app[SERVICE].wait_for_idle()
            assert not (await cancel('voice-1'))['pending']
            new = await (await act('new', 'new')).json()
            started.clear()
            assert (await act('send', 'new-message', new['thread_id'])).status == 200
            assert await asyncio.to_thread(started.wait, 1)
            assert not (await cancel('voice-1'))['pending']  # does not cancel latest run
            assert app[SERVICE].status(new['thread_id'], 'phone-new-message')['status'] == 'running'
            assert (await act('stop', 'stop-new', new['thread_id'])).status == 200
            await app[SERVICE].wait_for_idle()
        restarted = create_app(tmp_path, tmp_path, completion=model)
        async with TestClient(TestServer(restarted)) as client:
            r = await client.post('/migi/chat', json={'owner': owner, 'action': 'voice', 'request_id': 'before-submit', 'text': 'test'})
            assert r.status == 409
    asyncio.run(scenario())
