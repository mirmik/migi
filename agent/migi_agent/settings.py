"""Persisted global prompt; edits take effect at the next accepted turn."""
import hashlib
import json
from pathlib import Path

from aiohttp import web
from nemor.core.message import Message
from migi_agent.chat import write_json


class PromptSettings:
    def __init__(self, directory, default):
        self.path = Path(directory) / 'prompt.json'
        self.default = default
        self.text = json.loads(self.path.read_text())['text'] if self.path.exists() else default

    def snapshot(self):
        return {'text': self.text, 'revision': hashlib.sha256(self.text.encode()).hexdigest(),
                'is_default': self.text == self.default}

    def apply(self, session):
        # Called on the event loop before an accepted turn can start, never mid-run.
        session.messages = [Message('system', self.text)] + [m for m in session.messages if m.role != 'system']

    def mount(self, app):
        async def get(_request):
            return web.json_response(self.snapshot())

        async def save(request):
            try:
                value = await request.json()
                if not isinstance(value, dict) or set(value) - {'text', 'revision', 'reset'}:
                    raise ValueError('Invalid prompt settings')
                if 'reset' in value and not isinstance(value['reset'], bool):
                    raise ValueError('reset must be boolean')
                text = self.default if value.get('reset') else value.get('text')
                if not isinstance(text, str) or not text.strip() or len(text.encode()) > 64 << 10:
                    raise ValueError('Prompt must be non-empty and at most 64 KiB')
                current = self.snapshot()
                if value.get('revision') != current['revision'] and text != self.text:
                    raise web.HTTPConflict(text='Промпт уже изменён. Загрузите сохранённую версию перед повторной записью.')
                write_json(self.path, {'text': text})
                self.text = text
                return web.json_response(self.snapshot())
            except (ValueError, TypeError, KeyError) as exc:
                raise web.HTTPBadRequest(text=str(exc)) from exc

        app.router.add_get('/migi/prompt', get)
        app.router.add_post('/migi/prompt', save)
