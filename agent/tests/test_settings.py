import asyncio
import threading

from aiohttp.test_utils import TestClient, TestServer
from migi_agent.service import create_app, SERVICE, PROMPT
from test_chat import response


def test_prompt_persists_applies_to_existing_chat_and_conflicts(tmp_path):
    seen = []
    def model(messages, **kwargs):
        seen.append(messages[0]['content'])
        return response('ok')
    async def scenario():
        app = create_app(tmp_path, tmp_path, completion=model)
        async with TestClient(TestServer(app)) as client:
            original = await (await client.get('/migi/prompt')).json()
            assert original['text'] == PROMPT and original['is_default']
            async def send(rid):
                r = await client.post('/migi/chat', json={'owner': 'device:test', 'thread_id': 'device:test', 'action': 'send', 'request_id': rid, 'text': 'hi'})
                assert r.status == 200
                await app[SERVICE].wait_for_idle()
            await send('one')
            payload = {'text': 'Новая инструкция', 'revision': original['revision']}
            r = await client.post('/migi/prompt', json=payload)
            assert r.status == 200
            changed = await r.json()
            assert not changed['is_default']
            assert (await client.post('/migi/prompt', json=payload)).status == 200  # lost ack
            assert (await client.post('/migi/prompt', json={**payload, 'text': 'Устаревшая правка'})).status == 409
            assert (await client.post('/migi/prompt', json={**payload, 'text': ' '})).status == 400
            assert (await client.post('/migi/prompt', json=payload, headers={'Origin': 'http://other.test'})).status == 403
            await send('two')
            assert seen == [PROMPT, payload['text']]
        restarted = create_app(tmp_path, tmp_path, completion=model)
        async with TestClient(TestServer(restarted)) as client:
            assert (await (await client.get('/migi/prompt')).json()) == changed
            for owner in ('device:test', 'device:new'):
                assert (await client.post('/migi/chat', json={'owner': owner, 'thread_id': owner, 'action': 'send', 'request_id': 'three', 'text': 'hi'})).status == 200
                await restarted[SERVICE].wait_for_idle()
                assert seen[-1] == payload['text']
            r = await client.post('/migi/prompt', json={'reset': True, 'revision': changed['revision']})
            assert r.status == 200 and (await r.json()) == original
        assert (tmp_path / 'prompt.json').stat().st_mode & 0o777 == 0o600
    asyncio.run(scenario())


def test_prompt_save_during_run_does_not_mutate_current_turn(tmp_path):
    started, release = threading.Event(), threading.Event()
    seen = []
    def model(messages, **kwargs):
        seen.append(messages[0]['content'])
        started.set()
        assert release.wait(3)
        return response('ok')
    async def scenario():
        app = create_app(tmp_path, tmp_path, completion=model)
        async with TestClient(TestServer(app)) as client:
            try:
                original = await (await client.get('/migi/prompt')).json()
                async def send(rid):
                    return await client.post('/migi/chat', json={'owner': 'device:test', 'thread_id': 'device:test', 'action': 'send', 'request_id': rid, 'text': 'hi'})
                assert (await send('one')).status == 200
                assert await asyncio.to_thread(started.wait, 1)
                r = await asyncio.wait_for(client.post('/migi/prompt', json={'text': 'Changed', 'revision': original['revision']}), .5)
                assert r.status == 200
                session = app[SERVICE]._thread('device:test')[0].session
                assert session.messages[0].content == PROMPT
                assert (await send('one')).status == 200  # replay must not rewrite prompt mid-run
                assert session.messages[0].content == PROMPT
                release.set()
                await app[SERVICE].wait_for_idle()
                assert (await send('two')).status == 200
                await app[SERVICE].wait_for_idle()
                assert seen == [PROMPT, 'Changed']
            finally:
                release.set()
    asyncio.run(scenario())
