import asyncio
import json
import threading

from aiohttp.test_utils import TestClient, TestServer
from migi_agent.service import create_app, SERVICE


def payload(run, content='hello'):
    return {'threadId': 'device:phone', 'runId': run,
            'messages': [{'id': run, 'role': 'user', 'content': content}]}


def response(text='', calls=None):
    return {'choices': [{'message': {'role': 'assistant', 'content': text,
        **({'tool_calls': calls} if calls else {})}, 'finish_reason': 'stop'}]}


def test_tool_history_replay_restart(tmp_path):
    (tmp_path / 'example.txt').write_text('The secret number is 42.')
    calls = []
    def model(messages, config, **kwargs):
        assert config.get('max_tokens') is None
        calls.append(messages)
        if messages[-1]['role'] == 'user' and messages[-1]['content'] == 'read':
            return response(calls=[{'id': 'file', 'type': 'function', 'function': {
                'name': 'read_file', 'arguments': '{"path":"example.txt"}'}}])
        return response('42')

    async def scenario():
        app = create_app(tmp_path / 'state', tmp_path, completion=model)
        async with TestClient(TestServer(app)) as client:
            assert (await client.post('/migi/submit', json=payload('bad'),
                headers={'Origin': 'https://untrusted.example'})).status == 403
            assert (await client.post('/migi/submit', data=json.dumps(payload('bad')),
                headers={'Content-Type': 'text/plain'})).status == 415
            r = await client.post('/migi/submit', json=payload('one', 'read'))
            assert r.status == 202
            await app[SERVICE].wait_for_idle()
            r = await client.get('/agent/events', params={'threadId': 'device:phone', 'runId': 'one'})
            replay = await r.text()
            assert 'TOOL_CALL_RESULT' in replay and 'RUN_FINISHED' in replay
            assert len(calls) == 2 and '42' in calls[-1][-1]['content']
            assert (await client.post('/migi/submit', json=payload('one', 'read'))).status == 200
            assert len(calls) == 2
        restored = create_app(tmp_path / 'state', tmp_path, completion=model)
        restored[SERVICE].submit(payload('two', 'remember?'))
        await restored[SERVICE].wait_for_idle()
        assert len([m for m in calls[-1] if m['role'] == 'user']) == 2
        assert restored[SERVICE].status('device:phone', 'one')['result'] == '42'
    asyncio.run(scenario())


def test_busy_and_stop(tmp_path):
    started = threading.Event()
    def model(stop_event, **kwargs):
        started.set()
        assert stop_event.wait(5)
        return response('stopped')
    async def scenario():
        app = create_app(tmp_path, tmp_path, completion=model)
        async with TestClient(TestServer(app)) as client:
            assert (await client.post('/migi/submit', json=payload('one'))).status == 202
            assert await asyncio.to_thread(started.wait, 2)
            assert (await client.post('/migi/submit', json=payload('two'))).status == 409
            assert (await client.post('/migi/submit', json=payload('one'))).status == 200
            r = await client.post('/agent/cancel', json={'threadId': 'device:phone', 'runId': 'one'})
            assert (await r.json())['cancelRequested']
            await app[SERVICE].wait_for_idle()
            assert app[SERVICE].status('device:phone', 'one')['status'] == 'cancelled'
    asyncio.run(scenario())


def test_document_tool(tmp_path):
    from contextlib import contextmanager
    from io import BytesIO
    sent = []
    class Client:
        @contextmanager
        def open(self, method, path, body, headers):
            sent.append((method, path, json.loads(body)))
            r = BytesIO(b'{"created":true,"event_id":42}')
            r.status = 201
            yield r
    def model(messages, **kwargs):
        if messages[-1]['role'] == 'user':
            return response(calls=[{'id': 'doc', 'type': 'function', 'function': {
                'name': 'migi_show_document', 'arguments': json.dumps({'title': 'Physics',
                'blocks': [{'latex': 'E=mc^2'}]})}}])
        return response('Sent')
    async def scenario():
        app = create_app(tmp_path, tmp_path, completion=model, client=Client())
        app[SERVICE].submit(payload('one'))
        await app[SERVICE].wait_for_idle()
        assert sent[0][1] == '/v1/documents'
        assert sent[0][2]['blocks'][0] == {'type': 'math', 'latex': 'E=mc^2'}
        assert sent[0][2]['document_id'].startswith('agent-')
        async with TestClient(TestServer(app)) as client:
            r = await client.get('/migi/result', params={'threadId': 'device:phone', 'runId': 'one'})
            assert (await r.json())['document_event_id'] == 42
            r = await client.get('/migi/chat', params={'owner': 'device:phone'})
            reply = (await r.json())['reply']
            assert reply['document_event_id'] == 42
            assert reply['text'] == 'Sent'
    asyncio.run(scenario())
