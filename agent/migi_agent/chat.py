"""Device chat selection, immutable reading history and explicit context maintenance."""
import asyncio
import hashlib
import json
import os
from pathlib import Path
import threading
from datetime import datetime, timezone

from aiohttp import web
from nemor.agent.ag_ui import AgentService, AgentBusyError
from nemor.agent.turns import TurnConflictError, request_fingerprint


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_suffix('.new')
    with temporary.open('w') as stream:
        os.chmod(temporary, 0o600)
        json.dump(value, stream, ensure_ascii=False)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


class ChatAgentService(AgentService):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.maintenance = {}
        self.prepare_session = None

    def submit(self, payload):
        thread = payload.get('threadId') if isinstance(payload, dict) else None
        if thread in self.maintenance:
            raise AgentBusyError('Context maintenance is in progress')
        record, created = super().submit(payload)
        if created and self.prepare_session is not None:
            self.prepare_session(self._thread(thread)[0].session)
        return record, created


class Chats:
    def __init__(self, service, directory, *, model_name='', context_window=None):
        self.service = service
        self.directory = Path(directory) / 'chats'
        self.model_name = model_name
        self.context_window = context_window
        self.owners = {}
        self.histories = {}
        self.views = {}
        self.tasks = set()
        original_save = service.save_session

        def save(session):
            self.history(session)
            original_save(session)
        service.save_session = save

    def path(self, key, kind):
        return self.directory / kind / (hashlib.sha256(key.encode()).hexdigest() + '.json')

    def owner(self, owner):
        if not isinstance(owner, str) or not owner.startswith('device:') or len(owner) > 160:
            raise ValueError('Authenticated device owner required')
        if owner not in self.owners:
            path = self.path(owner, 'owners')
            value = json.loads(path.read_text()) if path.exists() else {
                'current': owner, 'threads': [owner], 'operations': {}}
            for op in value['operations'].values():
                if op.get('status') == 'compacting':
                    op.update(status='failed', error='Сжатие прервано перезапуском; автоматически не повторяется')
            self.owners[owner] = value
            write_json(path, value)
        return self.owners[owner]

    def history(self, session):
        path = self.path(session.key, 'history')
        if session.key not in self.histories:
            self.histories[session.key] = json.loads(path.read_text()) if path.exists() else []
        rows = self.histories[session.key]
        known = {r['id'] for r in rows}
        changed = False
        # The runtime publishes complete message batches. Do not wait on its
        # full-turn lock: the UI and stop endpoint must remain responsive.
        for message in list(session.messages):
            if message.role not in ('user', 'assistant') or not message.content or message.annotations.get('migi.compaction') or message.annotations.get('nemor.compaction'):
                continue
            key = hashlib.sha256((message.role + message.timestamp).encode()).hexdigest()[:24]
            if key not in known:
                rows.append({'id': key, 'role': message.role, 'text': message.content,
                             'time': message.timestamp})
                known.add(key)
                changed = True
        if changed:
            write_json(path, rows)
        return rows

    def view(self, thread):
        agent, ledger, _, _ = self.service._thread(thread)
        view = self.views.setdefault(thread, {'cursor': 0, 'runs': [], 'texts': {}, 'tools': {}, 'documents': {}})
        while True:
            rows = ledger.events_after(view['cursor'])
            if not rows:
                break
            for row in rows:
                view['cursor'] = row['event_id']
                run = row['turn_id']
                if run not in view['runs']:
                    view['runs'].append(run)
                event = row.get('event', {})
                if event.get('type') == 'TEXT_MESSAGE_CONTENT':
                    view['texts'].setdefault(run, {})[event['messageId']] = view['texts'].setdefault(run, {}).get(event['messageId'], '') + event['delta']
                if event.get('type') == 'TOOL_CALL_START':
                    view['tools'].setdefault(run, {})[event['toolCallId']] = event['toolCallName']
                if event.get('type') == 'TOOL_CALL_RESULT':
                    name = view['tools'].setdefault(run, {}).pop(event['toolCallId'], None)
                    if name == 'migi_show_document':
                        try:
                            event_id = json.loads(event['content']).get('event_id')
                            if type(event_id) is int and event_id > 0:
                                view['documents'][run] = event_id
                        except (ValueError, TypeError, AttributeError, KeyError):
                            pass
        record = ledger.get(view['runs'][-1]) if view['runs'] else None
        return agent, ledger, view, record

    def busy(self, thread):
        return bool(self.service._pending.get(thread) or thread in self.service.maintenance)

    def snapshot(self, owner, limit=40):
        selected = self.owner(owner)
        thread = selected['current']
        agent, _, view, record = self.view(thread)
        history = self.history(agent.session)
        messages = list(agent.session.messages)
        estimate = len(json.dumps({'messages': [m.to_api() for m in messages],
            'tools': agent.tools.get_tool_specs(agent.config)}, ensure_ascii=False)) // 3
        maintenance = self.service.maintenance.get(thread)
        status = self.service.status(thread, record.external_turn_id)['status'] if record else 'idle'
        live = ''
        tools = []
        elapsed = 0
        if record and not record.terminal:
            live = '\n'.join(view['texts'].get(record.turn_id, {}).values())
            tools = list(view['tools'].get(record.turn_id, {}).values())
            if record.started_at:
                elapsed = max(0, int((datetime.now(timezone.utc) - datetime.fromisoformat(record.started_at)).total_seconds()))
        operations = list(selected['operations'].values())
        maintenance_op = next((op for op in reversed(operations)
            if op.get('action') == 'compact' and op.get('thread_id') == thread), {})
        if maintenance and maintenance_op.get('started_at'):
            elapsed = max(0, int((datetime.now(timezone.utc) - datetime.fromisoformat(maintenance_op['started_at'])).total_seconds()))
        notice = next((op.get('error') or op.get('message') for op in reversed(operations)
                       if op.get('action') == 'compact' and op.get('thread_id') == thread), None)
        if maintenance:
            progress = maintenance_op.get('progress', {})
            phases = {'preparing': 'Подготовка сжатия.', 'generating': 'Ожидание модели.',
                      'reasoning': 'Модель обдумывает сводку.', 'summarizing': 'Модель формирует сводку.',
                      'committing': 'Сохранение контекста.'}
            notice = 'Останавливаю сжатие…' if maintenance.is_set() else phases.get(progress.get('phase'), 'Сжатие контекста…')
            if progress.get('summary_chars'):
                notice += f" Получено {progress['summary_chars']} символов сводки."
        reply = None
        if record and status == 'completed' and record.result and record.result.strip():
            reply = {'thread_id': thread, 'run_id': record.external_turn_id,
                     'text': record.result.strip(), 'document_event_id': view['documents'].get(record.turn_id, 0)}
        return {'compaction_progress': maintenance_op.get('progress') if maintenance else None, 'reply': reply, 'thread_id': thread, 'chat_number': selected['threads'].index(thread) + 1,
            'chat_count': len(selected['threads']), 'chats': [{'thread_id': t, 'title': f'Чат {i+1}'} for i, t in enumerate(selected['threads'])], 'model': self.model_name,
            'status': ('stopping' if maintenance.is_set() else 'compacting') if maintenance else status, 'busy': self.busy(thread),
            'run_id': record.external_turn_id if record else None, 'elapsed_seconds': elapsed,
            'active_tools': tools, 'live_text': live, 'notice': notice,
            'context_tokens_estimate': estimate, 'context_window': self.context_window,
            'context_messages': len(messages), 'message_total': len(history),
            'messages': history[-limit:]}

    def action(self, payload):
        owner = payload['owner']
        selected = self.owner(owner)
        action = payload['action']
        request_id = payload['request_id']
        if not isinstance(request_id, str) or not 1 <= len(request_id) <= 128:
            raise ValueError('request_id required')
        if action == 'voice' and request_id in selected.get('cancelled_voice', {}):
            raise TurnConflictError('Voice request was stopped')
        fingerprint = request_fingerprint(payload)
        previous = selected['operations'].get(request_id)
        if previous:
            if previous['fingerprint'] != fingerprint:
                raise TurnConflictError('Request ID belongs to another action')
            return previous
        thread = selected['current']
        if action not in ('voice', 'voice_stop') and payload.get('thread_id') != thread:
            raise TurnConflictError('Активный чат изменился. Обновите экран.')
        agent, _, _, record = self.view(thread)
        result = {'action': action, 'thread_id': thread, 'fingerprint': fingerprint, 'status': 'completed'}
        if action in ('stop', 'voice_stop'):
            maintenance = self.service.maintenance.get(thread)
            if maintenance:
                maintenance.set()
            elif record and not record.terminal:
                self.service.cancel(thread, record.external_turn_id)
        elif action in ('send', 'voice'):
            text = payload.get('text')
            if not isinstance(text, str) or not text.strip():
                raise ValueError('Message is empty')
            self.service.submit({'threadId': thread, 'runId': request_id if action == 'voice' else 'phone-' + request_id,
                'messages': [{'id': request_id, 'role': 'user', 'content': text}]})
        elif action in ('new', 'select', 'compact'):
            if self.busy(thread):
                raise AgentBusyError('Дождитесь завершения запроса или остановите его')
            if action == 'select':
                target = payload.get('target_thread')
                if target not in selected['threads']:
                    raise ValueError('Unknown conversation')
                if self.busy(target):
                    raise AgentBusyError('Selected conversation is busy')
                selected['current'] = target
                result['thread_id'] = target
            elif action == 'new':
                thread = owner + ':chat:' + hashlib.sha256(request_id.encode()).hexdigest()[:24]
                selected['threads'].append(thread)
                selected['current'] = thread
                result['thread_id'] = thread
            else:
                result['status'] = 'compacting'
                result['started_at'] = datetime.now(timezone.utc).isoformat()
                self.service.maintenance[thread] = threading.Event()
        else:
            raise ValueError('Unknown chat action')
        selected['operations'][request_id] = result
        write_json(self.path(owner, 'owners'), selected)
        if action == 'compact':
            task = asyncio.create_task(self.compact(owner, thread, request_id, agent))
            self.tasks.add(task)
            task.add_done_callback(self.tasks.discard)
        return result

    async def compact(self, owner, thread, request_id, agent):
        selected = self.owner(owner)
        op = selected['operations'][request_id]
        stop = self.service.maintenance[thread]
        try:
            self.history(agent.session)  # host owns the full reading archive
            loop = asyncio.get_running_loop()
            def progress(value):
                loop.call_soon_threadsafe(op.update, {'progress': value})
            result = await asyncio.to_thread(agent.compact_context,
                keep_recent_turns=2, stop_event=stop, on_progress=progress,
                save_session=self.service.save_session)
            messages = {
                'completed': 'Контекст сжат. Полная переписка сохранена в истории.',
                'cancelled': 'Сжатие остановлено; контекст сохранён.',
                'noop': 'Контекст оставлен прежним: нечего сжимать или сводка не короче исходного текста.',
            }
            op.update(status='cancelled' if result.status == 'cancelled' else 'completed',
                      message=messages[result.status], elapsed_seconds=result.elapsed_seconds)
        except Exception as exc:
            if stop.is_set():
                op.update(status='cancelled', message='Сжатие остановлено; контекст сохранён.')
            else:
                op.update(status='failed', error=str(exc))
        finally:
            self.service.maintenance.pop(thread, None)
            write_json(self.path(owner, 'owners'), selected)

    def cancel_voice(self, payload):
        owner, request_id = payload['owner'], payload['request_id']
        if not isinstance(request_id, str) or not 1 <= len(request_id) <= 128:
            raise ValueError('request_id required')
        selected = self.owner(owner)
        # Durable tombstone also wins against an upload/submit whose acknowledgement
        # was lost. Never resolve cancellation against the current/newest run.
        selected.setdefault('cancelled_voice', {})[request_id] = True
        write_json(self.path(owner, 'owners'), selected)
        pending = False
        for thread in selected['threads']:
            _, ledger, _, _ = self.service._thread(thread)
            record = ledger.get_external(request_id)
            if record and not record.terminal:
                self.service.cancel(thread, request_id)
                pending = True
        return {'pending': pending}

    def mount(self, app):
        async def thread(request):
            return web.json_response({'thread_id': self.owner(request.query['owner'])['current']})
        async def get(request):
            return web.json_response(self.snapshot(request.query['owner'], min(10000, max(1, int(request.query.get('limit', 40))))))
        async def post(request):
            result = self.action(await request.json())
            return web.json_response({k: v for k, v in result.items() if k != 'fingerprint'})
        async def cancel_voice(request):
            return web.json_response(self.cancel_voice(await request.json()))
        def guarded(handler):
            async def wrapped(request):
                try:
                    return await handler(request)
                except (AgentBusyError, TurnConflictError) as exc:
                    raise web.HTTPConflict(text=str(exc)) from exc
                except (ValueError, KeyError, TypeError) as exc:
                    raise web.HTTPBadRequest(text=str(exc)) from exc
            return wrapped
        app.router.add_get('/migi/chat/thread', guarded(thread))
        app.router.add_get('/migi/chat', guarded(get))
        app.router.add_post('/migi/chat', guarded(post))
        app.router.add_post('/migi/voice/cancel', guarded(cancel_voice))
        async def shutdown(_app):
            if self.tasks:
                await asyncio.gather(*self.tasks)
        app.on_shutdown.append(shutdown)
