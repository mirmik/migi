import asyncio
import threading

from aiohttp.test_utils import TestClient, TestServer
from migi_agent.service import create_app, SERVICE


def response(text):
    return {'choices': [{'message': {'role': 'assistant', 'content': text}, 'finish_reason': 'stop'}]}


def test_chat_history_compaction_new_and_restart(tmp_path):
    count = []
    def model(messages, config, **kwargs):
        assert config.get('max_tokens') is None
        count.append(messages)
        return response('Ключевой факт: барсук.' if config.get('tool_choice') == 'none' else 'Ответ ' + 'Подробности. ' * 80)
    async def scenario():
        app = create_app(tmp_path, tmp_path, completion=model, model_name='test-qwen')
        owner = 'device:test'
        async with TestClient(TestServer(app)) as client:
            async def get():
                r = await client.get('/migi/chat', params={'owner': owner})
                assert r.status == 200, await r.text()
                return await r.json()
            async def act(action, rid, thread=owner, **kwargs):
                return await client.post('/migi/chat', json={'owner': owner, 'action': action,
                    'request_id': rid, 'thread_id': thread, **kwargs})
            assert (await get())['thread_id'] == owner
            for i in range(4):
                r = await act('send', f'message-{i}', text='Сообщение ' + str(i))
                assert r.status == 200, await r.text()
                await app[SERVICE].wait_for_idle()
            before = await get()
            assert len(before['messages']) == 8
            assert before['reply']['run_id'] == 'phone-message-3'
            assert before['reply']['text'].startswith('Ответ ')
            assert before['reply']['document_event_id'] == 0
            assert (await act('send', 'message-3', text='Сообщение 3')).status == 200
            assert len(count) == 4
            assert (await act('send', 'message-3', text='changed')).status == 409
            assert (await act('compact', 'compact-1')).status == 200
            for _ in range(100):
                after = await get()
                if not after['busy']:
                    break
                await asyncio.sleep(.01)
            assert after['messages'] == before['messages']
            assert after['context_messages'] < before['context_messages']
            assert after['context_tokens_estimate'] < before['context_tokens_estimate']
            r = await act('new', 'new-1')
            new = (await r.json())['thread_id']
            assert new != owner and (await get())['messages'] == []
            assert (await act('new', 'new-1')).status == 200  # lost response retry
            assert (await get())['chat_count'] == 2
            assert (await act('send', 'stale', text='old screen')).status == 409
            assert (await act('select', 'select-1', thread=new, target_thread=owner)).status == 200
            assert len((await get())['messages']) == 8
        restarted = create_app(tmp_path, tmp_path, completion=model)
        async with TestClient(TestServer(restarted)) as client:
            r = await client.get('/migi/chat', params={'owner': owner})
            restored = await r.json()
            assert restored['messages'] == before['messages']
            assert restored['context_messages'] == after['context_messages']
            other = await client.get('/migi/chat', params={'owner': 'device:other'})
            assert (await other.json())['messages'] == []
    asyncio.run(scenario())


def test_monitor_and_stop_while_runtime_lock_is_held(tmp_path):
    started = threading.Event()
    def model(stop_event, **kwargs):
        started.set()
        assert stop_event.wait(4)
        return response('stopped')
    async def scenario():
        app = create_app(tmp_path, tmp_path, completion=model)
        async with TestClient(TestServer(app)) as client:
            owner = 'device:test'
            async def act(action, rid):
                return await client.post('/migi/chat', json={'owner': owner, 'thread_id': owner,
                    'request_id': rid, 'action': action, 'text': 'hello'})
            assert (await act('send', 'one')).status == 200
            assert await asyncio.to_thread(started.wait, 2)
            r = await asyncio.wait_for(client.get('/migi/chat', params={'owner': owner}), .5)
            busy = await r.json()
            assert busy['busy'] and busy['reply'] is None
            assert (await act('new', 'new')).status == 409
            assert (await act('compact', 'compact')).status == 409
            assert (await act('send', 'two')).status == 409
            assert (await act('stop', 'stop')).status == 200
            await app[SERVICE].wait_for_idle()
            r = await client.get('/migi/chat', params={'owner': owner})
            assert (await r.json())['status'] == 'cancelled'
    asyncio.run(scenario())


def test_compaction_blocks_voice_and_can_be_stopped(tmp_path):
    started = threading.Event()
    def model(messages, config, stop_event=None, **kwargs):
        if config.get("tool_choice") == "none":
            kwargs["thinking_callback"]("hidden reasoning")
            started.set()
            assert stop_event.wait(4)
            return response('summary')
        return response('Long answer. ' * 80)
    async def scenario():
        app = create_app(tmp_path, tmp_path, completion=model)
        async with TestClient(TestServer(app)) as client:
            owner = 'device:test'
            async def act(action, rid, **kwargs):
                return await client.post('/migi/chat', json={'owner': owner, 'thread_id': owner,
                    'request_id': rid, 'action': action, **kwargs})
            for i in range(3):
                assert (await act('voice', 'voice-' + str(i), text='message')).status == 200
                await app[SERVICE].wait_for_idle()
            before = await (await client.get('/migi/chat', params={'owner': owner})).json()
            assert (await act('compact', 'compact')).status == 200
            assert await asyncio.to_thread(started.wait, 2)
            progress = await (await client.get('/migi/chat', params={'owner': owner})).json()
            assert progress['compaction_progress']['reasoning_chars'] == len('hidden reasoning')
            assert 'hidden reasoning' not in str(progress)
            assert 'обдумывает' in progress['notice']
            assert (await act('voice', 'new-voice', text='not queued')).status == 409
            assert (await act('new', 'new')).status == 409
            assert (await act('stop', 'stop')).status == 200
            for _ in range(100):
                after = await (await client.get('/migi/chat', params={'owner': owner})).json()
                if not after['busy']:
                    break
                await asyncio.sleep(.01)
            assert after['context_messages'] == before['context_messages']
            assert after['messages'] == before['messages']
            new = await (await act('new', 'new')).json()
            assert new['thread_id'] != owner
            # A lost voice acknowledgement is bound to the ORIGINAL chat.
            old = await (await act('voice', 'voice-2', text='message')).json()
            assert old['thread_id'] == owner
            assert (await (await client.get('/migi/chat', params={'owner': owner})).json())['messages'] == []
    asyncio.run(scenario())
