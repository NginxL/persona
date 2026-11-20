#!/usr/bin/env python3
"""Synthetic loopback Hub for exercising the native Harmony UI in a production HAP."""
from __future__ import annotations

import argparse
import base64
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import ssl
import struct
import sys
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, quote, urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts"))
from persona_identity import LEGACY_OP_HEADER


HUB_ID = '8a18eac0-435d-4e42-907a-015ce56de2ba'
PAIRING_TOKEN = 'synthetic-known'
DEVICE_TOKEN = 'synthetic-device-token'
MANAGER = 'assistant'
MACHINE = 'fixture-computer'
MAIN = 'fixture-main'
LEAD_SESSION = 'fixture-followup'
SCENARIOS = ('normal', 'question', 'pending', 'failed', 'held')
BASE_TIME = '2026-11-14T03:00:00.000Z'
INBOX_ITEM_ID = 'fixture-inbox'
TRANSCRIPT_HISTORY = 'fixture-transcript-v1'


def load_transport_helpers():
    source = Path(__file__).with_name('native-transport-fixture.py')
    spec = importlib.util.spec_from_file_location('native_transport_fixture', source)
    if spec is None or spec.loader is None:
        raise RuntimeError('cannot load native transport fixture helpers')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.certificate, module.frame


certificate, frame = load_transport_helpers()


def fixture_certificate(runtime: Path):
    key_path = runtime / 'native-ui.key'
    cert_path = runtime / 'native-ui.crt'
    if not key_path.exists() or not cert_path.exists():
        return certificate(runtime, 'native-ui')

    public_key = subprocess.run(
        ['openssl', 'x509', '-pubkey', '-noout', '-in', str(cert_path)],
        check=True, capture_output=True,
    ).stdout
    der_key = subprocess.run(
        ['openssl', 'pkey', '-pubin', '-outform', 'DER'],
        input=public_key, check=True, capture_output=True,
    ).stdout
    pin = base64.urlsafe_b64encode(hashlib.sha256(der_key).digest()).decode().rstrip('=')
    return key_path, cert_path, pin


def interaction(long_form=False):
    questions = [{
        'question': 'Which synthetic view should open next?',
        'header': 'Next view', 'multi_select': False,
        'options': [{'label': 'Plan card', 'description': 'Open the editable synthetic plan'},
                    {'label': 'Receipt card', 'description': 'Open a synthetic receipt'}],
    }]
    if long_form:
        questions = [{
            'question': question, 'multi_select': index == 0,
            'options': [
                {'label': 'Keep every existing conversation draft and return to the original source',
                 'description': 'Use the native layout while preserving the selected request and its local answers.'},
                {'label': 'Review a long alternative before explicitly confirming the current request',
                 'description': 'This synthetic option checks wrapping and the native scroll area at large text sizes.'},
            ],
        } for index, question in enumerate(('First fixture question', 'Second fixture question', 'Last fixture question'))]
    return {
        'id': 'fixture-question', 'machine_id': MACHINE, 'session_id': MAIN,
        'manager': MANAGER, 'status': 'open', 'kind': 'question', 'tool_name': 'ask_user',
        'resumable': True, 'can_cancel': True, 'questions': questions,
    }


def plan(state='open'):
    return {
        'id': 'fixture-plan', 'conversation': MAIN, 'manager': MANAGER,
        'state': state, 'created_at': BASE_TIME, 'updated_at': BASE_TIME,
        'goal': 'Exercise the plan review screen',
        'out_of_bounds': ['Do not contact external services'],
        'done_when': ['The edited plan is explicitly reconfirmed'],
        'brief': {
            'goal': 'Review the fixture UI workflow',
            'out_of_bounds': ['No real Hub data or external services'],
            'criteria': ['The draft remains local until send', 'The plan is reviewed before start'],
        },
        'tasks': [{
            'title': 'Inspect the native fixture', 'prompt': 'Review the synthetic plan card',
            'machine': MACHINE, 'cwd': '/synthetic/persona-ui-fixture', 'permission': 'plan', 'readonly': True,
            'goal': 'Confirm the review and edit flow',
            'out_of_bounds': ['Do not modify a real project'],
            'done_when': ['A user explicitly starts the edited plan'],
            'brief': {
                'goal': 'Review the synthetic plan card',
                'out_of_bounds': ['No external writes'],
                'criteria': ['Use only synthetic fixture content'],
            },
        }],
    }


def receipt(item_id, todo_id, state, title, summary, criteria, **extra):
    body = {
        'state': state, 'title': title, 'summary': summary,
        'criteria': criteria, 'criteria_more': 0,
    }
    body.update(extra)
    return {
        'id': item_id, 'kind': 'receipt', 'ref': todo_id, 'conversation': MAIN,
        'manager': MANAGER, 'at': BASE_TIME, 'body': body,
    }


def feed_items(recovered_todos=()):
    return [
        {
            'id': 'fixture-feed-plan', 'kind': 'plan', 'ref': 'fixture-plan',
            'conversation': MAIN, 'manager': MANAGER, 'at': BASE_TIME,
            'body': {'title': 'Review the synthetic plan', 'summary': 'One editable task'},
        },
        receipt('fixture-receipt-done', 'fixture-todo-done', 'done', 'Completed fixture task',
                'The synthetic task completed.', [
                    {'criterion': 'Synthetic-only result', 'state': 'met', 'evidence': 'Fixture response'},
                ]),
        receipt('fixture-receipt-stopped', 'fixture-todo-stopped', 'done', 'Stopped fixture task',
                'The task stopped before completion.', [
                    {'criterion': 'Completion recorded', 'state': 'not_met', 'evidence': ''},
                ]),
        receipt('fixture-receipt-failed', 'fixture-todo-failed', 'failed', 'Failed fixture start',
                'A synthetic task failed to start.', [
                    {'criterion': 'Agent session started', 'state': 'not_met', 'evidence': 'Synthetic start error'},
                ], error='Synthetic fixture start failure'),
        receipt('fixture-receipt-interrupted', 'fixture-todo-interrupted', 'interrupted', 'Interrupted fixture task',
                'The listed synthetic lead session stopped with an error.', [
                    {'criterion': 'Work resumed', 'state': 'unchecked', 'evidence': ''},
                ], resolution='resumed' if 'fixture-todo-interrupted' in recovered_todos else 'pending'),
    ]


def todos():
    lead = {
        'machine_id': MACHINE, 'session_id': LEAD_SESSION, 'state': 'idle',
        'turn_error': 'Synthetic run stopped for UI recovery review',
        'turn_error_kind': 'execution_error', 'updated_at': BASE_TIME,
    }
    return {
        'fixture-todo-done': {
            'id': 'fixture-todo-done', 'manager': MANAGER, 'origin': MAIN,
            'plan_id': 'fixture-plan', 'title': 'Completed fixture task',
            'done_at': BASE_TIME, 'start_error': '', 'agents': [],
        },
        'fixture-todo-stopped': {
            'id': 'fixture-todo-stopped', 'manager': MANAGER, 'origin': MAIN,
            'plan_id': 'fixture-plan', 'title': 'Stopped fixture task',
            'done_at': '', 'start_error': '', 'agents': [],
        },
        'fixture-todo-failed': {
            'id': 'fixture-todo-failed', 'manager': MANAGER, 'origin': MAIN,
            'plan_id': 'fixture-plan', 'title': 'Failed fixture start',
            'done_at': BASE_TIME, 'start_error': 'Synthetic fixture start failure', 'agents': [],
        },
        'fixture-todo-interrupted': {
            'id': 'fixture-todo-interrupted', 'manager': MANAGER, 'origin': MAIN,
            'plan_id': 'fixture-plan', 'title': 'Interrupted fixture task',
            'done_at': '', 'start_error': '', 'agents': [dict(lead, linked_at=BASE_TIME)],
        },
    }


def optional_bool(control, key):
    value = control.get(key)
    return value if type(value) is bool else None


def _lead_working(recovered_todos=(), agent_working=None):
    if type(agent_working) is bool:
        return agent_working
    return 'fixture-todo-interrupted' in recovered_todos


def held_message(operation, text, state='waiting', line='Will send when Test computer is back.'):
    return dict(id=operation, text=text, computer='Test computer', state=state, error=line, at=BASE_TIME)


def session_rows(recovered_todos=(), agent_working=None, held=None):
    capabilities = {
        'read': True, 'send': True, 'approve': True, 'interrupt': True,
        'terminal': True, 'attach': True, 'modes': True,
    }
    lead = {
        'id': LEAD_SESSION, 'session_id': LEAD_SESSION, 'machine_id': MACHINE,
        'manager': MANAGER, 'title': 'Interrupted fixture lead', 'state': 'idle',
        'source_kind': 'native', 'capabilities': capabilities,
        'turn_error': 'Synthetic run stopped for UI recovery review',
        'turn_error_kind': 'execution_error', 'updated_at': BASE_TIME,
    }
    working = _lead_working(recovered_todos, agent_working)
    lead['state'] = 'working' if working else 'idle'
    if 'fixture-todo-interrupted' in recovered_todos:
        lead.pop('turn_error', None)
        lead.pop('turn_error_kind', None)
    rows = [
        {
            'id': MAIN, 'session_id': MAIN, 'machine_id': MACHINE,
            'manager': MANAGER, 'title': 'Persona UI fixture', 'state': 'idle',
            'source_kind': 'native', 'capabilities': capabilities,
            'permission_mode': 'default', 'effort': 'medium',
            'permission_modes': ['default', 'plan'], 'efforts': ['low', 'medium', 'high'],
        },
        lead,
    ]
    if held is not None:
        lead.update(title='Field notes', status='offline', state='offline', undelivered=held)
        lead.pop('turn_error', None)
        lead.pop('turn_error_kind', None)
        for identifier, title, fields in (
            ('fixture-stopped', 'Stopped Agent', {'status': 'interrupted', 'capabilities': {'read': True, 'terminal': True}}),
            ('fixture-readonly', 'Read-only Agent', {'read_only': True, 'source_kind': 'history', 'capabilities': {'read': True}}),
        ):
            rows.append(dict(id=identifier, session_id=identifier, machine_id=MACHINE, manager=MANAGER, title=title, **fields))
    return rows


def conversations(recovered_todos=(), agent_working=None):
    capabilities = {
        'read': True, 'send': True, 'approve': True, 'interrupt': True,
        'terminal': True, 'attach': True, 'modes': True,
    }
    return [
        {'id': MAIN, 'session_id': MAIN, 'machine_id': MACHINE, 'manager': MANAGER,
         'title': 'Persona UI fixture', 'main': True, 'state': 'idle',
         'source_kind': 'native', 'capabilities': capabilities,
         'permission_mode': 'default', 'effort': 'medium',
         'permission_modes': ['default', 'plan'], 'efforts': ['low', 'medium', 'high']},
        {'id': LEAD_SESSION, 'session_id': LEAD_SESSION, 'machine_id': MACHINE,
         'manager': MANAGER, 'title': 'Second synthetic conversation',
         'state': 'working' if _lead_working(recovered_todos, agent_working) else 'idle',
         'source_kind': 'native', 'capabilities': capabilities},
    ]


def _reading_item(seq):
    return {
        'id': f'fixture-reading-{seq}#0', 'seq': seq,
        'role': 'user' if seq % 2 == 0 else 'assistant', 'kind': 'text',
        'text': f'Fixture reading segment {seq}: this synthetic paragraph stays fixed across every response. '
                'It is intentionally long enough to wrap over several native transcript lines and contains no personal information.',
    }


def transcript(after_seq=None, control=None, before_seq=None):
    control = control if isinstance(control, dict) else {}
    reading_case = optional_bool(control, 'reading_case') is True
    if reading_case:
        all_items = [_reading_item(seq) for seq in range(40, 60)]
        if optional_bool(control, 'reading_prepend') is True:
            all_items = [_reading_item(seq) for seq in range(20, 40)] + all_items
        if optional_bool(control, 'reading_append') is True:
            all_items += [_reading_item(seq) for seq in range(60, 65)]
    else:
        all_items = [
            {'id': 'fixture-user-1#0', 'seq': 1, 'role': 'user', 'kind': 'text',
             'text': 'Show the synthetic UI fixture overview.'},
            {'id': 'fixture-assistant-2#0', 'seq': 2, 'role': 'assistant', 'kind': 'text',
             'text': 'This conversation contains synthetic plan and receipt examples.'},
        ]

    paged = reading_case and optional_bool(control, 'reading_paged') is True and optional_bool(control, 'reading_prepend') is True
    items = [item for item in all_items if item['seq'] >= 40] if paged and before_seq is None else all_items
    if after_seq is not None:
        items = [item for item in items if item['seq'] > after_seq]
    elif before_seq is not None:
        items = [item for item in items if item['seq'] < before_seq]
    items.sort(key=lambda item: item['seq'])
    if after_seq is not None:
        to_seq = max(after_seq, items[-1]['seq'] if items else after_seq)
    else:
        to_seq = items[-1]['seq'] if items else 0
    result = {
        'items': items,
        'history': TRANSCRIPT_HISTORY,
        'to_seq': to_seq,
        'backlog': False, 'has_more': paged and before_seq is None, 'more_after': False,
    }
    # Like the Hub span, an after cursor is the lower continuation boundary,
    # even when the first returned row is the following sequence or is absent.
    if after_seq is not None:
        result['from_seq'] = after_seq
    elif before_seq is not None and items:
        result['from_seq'] = items[0]['seq']
    return result


def inbox_item(done=False, detail=False):
    item = {
        'id': INBOX_ITEM_ID, 'title': 'Review the native layout',
        'preview': 'Confirm the synthetic acceptance checklist.',
        'source': 'manual', 'account_name': 'UI review', 'manager': MANAGER,
        'done_at': BASE_TIME if done else '',
    }
    if detail:
        item['text'] = 'Synthetic review content for checking the Inbox detail and handoff flow.'
    return item


def projected_todos(recovered_todos=()):
    result = todos()
    for todo_id in recovered_todos:
        item = result.get(todo_id)
        if item is None:
            continue
        item['done_at'] = ''
        item['start_error'] = ''
        if todo_id == 'fixture-todo-interrupted':
            item['agents'] = [{
                'machine_id': MACHINE, 'session_id': LEAD_SESSION,
                'state': 'working', 'linked_at': BASE_TIME,
            }]
    return result


class FixtureState:
    def __init__(self, runtime: Path, default_scenario: str):
        self.runtime = runtime
        self.control_path = runtime / 'control.json'
        self.state_path = runtime / 'state.json'
        self.requests_path = runtime / 'requests.jsonl'
        self.lock = threading.RLock()
        self.held_listeners = set()
        if not self.control_path.exists():
            self.write_private(self.control_path, {
                'scenario': default_scenario,
                'receipts': {},
            })
        try:
            self.data = json.loads(self.state_path.read_text())
            if not isinstance(self.data, dict):
                raise ValueError('state must be an object')
        except (OSError, ValueError, json.JSONDecodeError):
            self.data = {}
        self.data.setdefault('requests', 0)
        self.data.setdefault('writes', 0)
        self.data.setdefault('replays', 0)
        self.data.setdefault('counts', {})
        self.data.setdefault('operations', {})
        self.data.setdefault('plan_state', 'open')
        self.data.setdefault('question_resolved', False)
        self.data.setdefault('inbox_done', False)
        self.data.setdefault('recovered_todos', [])
        self.data.setdefault('held', [
            held_message('waiting', 'Pack the field notes.'),
            held_message('expired', 'File the receipts.', 'failed', 'Not sent. Test computer was away for 24 hours.'),
            held_message('uncertain', 'Send the revised map.', 'failed', 'Might not have arrived. Retry checks the chat first.'),
            dict(held_message('signed-out', 'Seal the envelope.', 'failed', 'The sender signed out. You can delete this message.'), delete_only=True),
        ])
        self.save()

    @staticmethod
    def write_private(path: Path, value):
        path.write_text(json.dumps(value, indent=2) + '\n')
        path.chmod(0o600)

    def save(self):
        with self.lock:
            temporary = self.state_path.with_suffix('.tmp')
            self.write_private(temporary, self.data)
            temporary.replace(self.state_path)
            self.state_path.chmod(0o600)

    def snapshot(self):
        with self.lock:
            return copy.deepcopy(self.data)

    def control(self):
        try:
            value = json.loads(self.control_path.read_text())
            if isinstance(value, dict):
                return value
        except (OSError, ValueError, json.JSONDecodeError):
            pass
        return {}

    def scenario(self):
        selected = self.control().get('scenario')
        return selected if selected in SCENARIOS else 'normal'

    def record(self, method, path, operation_id='', status=200, replay=False):
        with self.lock:
            self.data['requests'] += 1
            key = method + ' ' + path
            self.data['counts'][key] = self.data['counts'].get(key, 0) + 1
            row = {
                'n': self.data['requests'], 'method': method, 'path': path,
                'status': status, 'operation_id': operation_id, 'replay': replay,
            }
            with self.requests_path.open('a', encoding='utf-8') as stream:
                stream.write(json.dumps(row, separators=(',', ':')) + '\n')
            self.requests_path.chmod(0o600)
            self.save()

    def begin_operation(self, operation_id, path, response, status=200, apply=None,
                        admit=None, denied_status=409, denied_response=None, method='POST'):
        with self.lock:
            if operation_id and operation_id in self.data['operations']:
                self.data['replays'] += 1
                saved = self.data['operations'][operation_id]
                self.record(method, path, operation_id, saved.get('status', 200), replay=True)
                self.save()
                return saved.get('status', 200), saved.get('response', response), True
            if admit is not None and not admit():
                denied_response = denied_response or {'code': 'request_updated'}
                self.record(method, path, operation_id, denied_status, replay=False)
                return denied_status, denied_response, False
            if operation_id:
                self.data['operations'][operation_id] = {'path': path, 'status': status, 'response': response}
            self.data['writes'] += 1
            if apply is not None:
                apply()
            self.record(method, path, operation_id, status, replay=False)
            self.save()
            return status, response, False

    def operation(self, operation_id):
        with self.lock:
            saved = self.data['operations'].get(operation_id)
            if not saved:
                return {'state': 'unknown'}
            state = 'pending' if saved.get('status') == 202 and saved.get('response', {}).get('delivered') != 'later' else 'done'
            configured = self.control().get('receipts', {})
            if isinstance(configured, dict) and configured.get(operation_id) in ('done', 'failed', 'unknown', 'pending'):
                state = configured[operation_id]
            return {'state': state}

    def announce_held(self):
        row = next(row for row in session_rows(held=self.data['held']) if row['id'] == LEAD_SESSION)
        payload = frame(1, json.dumps(dict(type='session', session=row)).encode())
        for listener in self.held_listeners.copy():
            try:
                listener(payload)
            except OSError:
                self.held_listeners.discard(listener)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime', type=Path, required=True, help='private directory for synthetic fixture state')
    parser.add_argument('--port', type=int, default=38663)
    parser.add_argument('--scenario', choices=SCENARIOS, default='normal')
    args = parser.parse_args()
    if not 1 <= args.port <= 65535:
        parser.error('--port must be between 1 and 65535')
    args.runtime.mkdir(parents=True, exist_ok=True, mode=0o700)
    args.runtime.chmod(0o700)
    state = FixtureState(args.runtime, args.scenario)
    key_path, cert_path, pin = fixture_certificate(args.runtime)

    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.minimum_version = ssl.TLSVersion.TLSv1_2
    tls.load_cert_chain(str(cert_path), str(key_path))

    pair_url = 'persona://pair?url=' + quote('https://localhost:' + str(args.port), safe='') + \
        '&token=' + PAIRING_TOKEN + '&hub=' + HUB_ID + '&fp=' + pin
    descriptor = {
        'address': 'https://localhost:' + str(args.port), 'hub_id': HUB_ID,
        'hub_name': 'Persona UI fixture', 'pin': pin, 'pair_url': pair_url,
        'disclosure': 'Synthetic UI regression data only; not an actual Codex session or production Hub.',
    }
    FixtureState.write_private(args.runtime / 'descriptor.json', descriptor)

    class Handler(BaseHTTPRequestHandler):
        protocol_version = 'HTTP/1.1'

        def log_message(self, *_args):
            pass

        def parsed_path(self):
            return urlsplit(self.path).path

        def read_body(self):
            try:
                size = min(1024 * 1024, max(0, int(self.headers.get('Content-Length', '0'))))
            except ValueError:
                size = 0
            return self.rfile.read(size)

        def reply(self, status, value):
            payload = value if isinstance(value, bytes) else json.dumps(value, separators=(',', ':')).encode('utf-8')
            self.send_response(status)
            self.send_header('Content-Type', 'application/json; charset=utf-8')
            self.send_header('Content-Length', str(len(payload)))
            self.send_header('Cache-Control', 'no-store')
            self.send_header('Connection', 'close')
            self.end_headers()
            try:
                self.wfile.write(payload)
            except (BrokenPipeError, ConnectionResetError, ssl.SSLError):
                pass
            self.close_connection = True

        def operation_id(self):
            return self.headers.get('X-Persona-Op-Id', '') or self.headers.get(LEGACY_OP_HEADER, '')

        def authorized(self):
            return self.headers.get('Authorization') == 'Bearer ' + DEVICE_TOKEN

        def tracked_reply(self, method, path, status, value, op_id=''):
            state.record(method, path, op_id, status)
            self.reply(status, value)

        def do_GET(self):
            path = self.parsed_path()
            if path == '/api/v1/ws':
                with state.lock:
                    if not self.authorized():
                        return self.tracked_reply('GET', path, 401, {'code': 'unauthorized'})
                return self.websocket(path)
            # Keep the read snapshot, route result, journal entry, and state
            # replacement within one critical section. Concurrent GET and POST
            # handlers otherwise share the same temporary state filename.
            with state.lock:
                snapshot = state.snapshot()
                scenario = state.scenario()
                if path == '/api/v1/health':
                    return self.tracked_reply('GET', path, 200, {
                        'ok': True, 'hub_id': HUB_ID, 'hub_name': 'Persona UI fixture',
                    })
                if not self.authorized():
                    return self.tracked_reply('GET', path, 401, {'code': 'unauthorized'})
                return self.get_route(path, snapshot, scenario)

        def get_route(self, path, snapshot, scenario):
            method = 'GET'
            if path == '/api/v1/managers':
                return self.tracked_reply(method, path, 200, {'default': MANAGER, 'managers': [
                    {'id': MANAGER, 'name': 'Assistant', 'default': True, 'role': 'assistant'},
                ]})
            if path in ('/api/v1/pins', '/api/v1/mutes'):
                return self.tracked_reply(method, path, 200, {'spaces': [], 'sessions': [], 'allowed': []})
            if path == '/api/v1/sessions':
                agent_working = optional_bool(state.control(), 'agent_working')
                return self.tracked_reply(method, path, 200, {
                    'sessions': session_rows(snapshot['recovered_todos'], agent_working, snapshot['held'] if scenario == 'held' else None),
                })
            if path == '/api/v1/machines':
                return self.tracked_reply(method, path, 200, {'machines': [
                    {'id': MACHINE, 'name': 'Test computer', 'title': 'Test computer',
                     'manager': MANAGER, 'os': 'mac', 'online': scenario != 'held', 'trust': 'trusted'},
                ]})
            if path == '/api/v1/assistant':
                agent_working = optional_bool(state.control(), 'agent_working')
                return self.tracked_reply(method, path, 200, {
                    'main': MAIN, 'main_machine_id': MACHINE, 'machine_id': MACHINE,
                    'conversations': conversations(snapshot['recovered_todos'], agent_working),
                })
            if path == '/api/v1/interactions':
                values = [] if snapshot.get('question_resolved') or scenario != 'question' else [interaction(state.control().get('question_case') == 'long')]
                return self.tracked_reply(method, path, 200, {'interactions': values})
            if path == '/api/v1/interactions/history':
                return self.tracked_reply(method, path, 200, {'interactions': []})
            if path == '/api/v1/inbox':
                items = [] if snapshot.get('inbox_done') else [inbox_item()]
                return self.tracked_reply(method, path, 200, {'items': items})
            if path == '/api/v1/inbox/' + INBOX_ITEM_ID:
                return self.tracked_reply(method, path, 200, {
                    'item': inbox_item(snapshot.get('inbox_done', False), detail=True),
                })
            if path == '/api/v1/todos':
                values = list(projected_todos(snapshot['recovered_todos']).values())
                return self.tracked_reply(method, path, 200, {'todos': values})
            if path == '/api/v1/task-plans/fixture-plan':
                return self.tracked_reply(method, path, 200, {'plan': plan(snapshot.get('plan_state', 'open'))})
            if path.startswith('/api/v1/assistant/conversations/') and path.endswith('/feed'):
                conversation_id = path.split('/')[-2]
                if conversation_id != MAIN:
                    return self.tracked_reply(method, path, 200, {
                        'items': [], 'plans': {}, 'task_cards': [], 'views': {},
                        'view_actions': {}, 'view_blocks': [], 'next_cursor': '',
                    })
                items = feed_items(snapshot['recovered_todos'])
                plan_state = snapshot.get('plan_state', 'open')
                if plan_state == 'dismissed':
                    items = [item for item in items if item['kind'] != 'plan']
                return self.tracked_reply(method, path, 200, {
                    'items': items, 'plans': {'fixture-plan': plan(plan_state)},
                    'task_cards': [], 'views': {}, 'view_actions': {}, 'view_blocks': [], 'next_cursor': '',
                })
            if path.startswith('/api/v1/sessions/') and path.endswith('/items'):
                query = parse_qs(urlsplit(self.path).query)
                after_values = query.get('after_seq', [])
                before_values = query.get('before_seq', [])
                after_seq = None
                before_seq = None
                if after_values:
                    try:
                        after_seq = max(0, int(after_values[0]))
                    except ValueError:
                        after_seq = None
                elif before_values:
                    try:
                        before_seq = max(0, int(before_values[0]))
                    except ValueError:
                        before_seq = None
                return self.tracked_reply(method, path, 200, transcript(
                    after_seq, state.control(), before_seq,
                ))
            if path.startswith('/api/v1/ops/'):
                op_id = path.rsplit('/', 1)[-1]
                return self.tracked_reply(method, path, 200, state.operation(op_id))
            if path.startswith('/api/v1/todos/') and path.count('/') == 4:
                todo_id = path.rsplit('/', 1)[-1]
                item = projected_todos(snapshot['recovered_todos']).get(todo_id)
                if item is None:
                    return self.tracked_reply(method, path, 404, {'code': 'not_found'})
                return self.tracked_reply(method, path, 200, {'todo': item})
            if path.startswith('/api/v1/kb-search'):
                entry = {'id': 'fixture-project', 'kind': 'projects', 'manager': MANAGER,
                         'title': 'Synthetic project context', 'summary': 'A local-only picker item'}
                hit = {'entry': entry, 'path': 'projects/fixture-project', 'line': '1'}
                return self.tracked_reply(method, path, 200, {'results': [hit], 'hits': [hit]})
            if path.startswith('/api/v1/kb/'):
                kind = path.rstrip('/').rsplit('/', 1)[-1]
                if kind in ('projects', 'chats', 'people', 'docs', 'meetings', 'events', 'recordings'):
                    return self.tracked_reply(method, path, 200, {'items': [{
                        'id': 'fixture-' + kind, 'kind': kind, 'manager': MANAGER,
                        'title': 'Synthetic ' + kind[:-1] if kind.endswith('s') else 'Synthetic ' + kind,
                        'summary': 'Local synthetic context entry', 'updated_at': BASE_TIME,
                    }]})
            return self.tracked_reply(method, path, 404, {'code': 'not_found'})

        def do_POST(self):
            # Serialize dispatch with GET snapshots, operation replay caching,
            # request journaling, and state-file replacement.
            with state.lock:
                return self.post_request()

        def post_request(self):
            path = self.parsed_path()
            payload = self.read_body()
            scenario = state.scenario()
            if path == '/api/v1/auth/tokens':
                if self.headers.get('Authorization') != 'Bearer ' + PAIRING_TOKEN:
                    return self.tracked_reply('POST', path, 401, {'code': 'invalid_pairing_token'})
                return self.tracked_reply('POST', path, 200, {
                    'id': 'fixture-device', 'token': DEVICE_TOKEN, 'role': 'full',
                })
            if not self.authorized():
                return self.tracked_reply('POST', path, 401, {'code': 'unauthorized'})
            if path == '/api/v1/interactions/fixture-question/resolve':
                operation_id = self.operation_id()
                try:
                    body = json.loads(payload)
                except (ValueError, UnicodeDecodeError):
                    return self.tracked_reply('POST', path, 400, {'code': 'invalid_json'})
                if not isinstance(body, dict):
                    return self.tracked_reply('POST', path, 400, {'code': 'invalid_json'})
                resolution_kind = 'cancel' if body.get('cancel') is True else 'answer'
                if resolution_kind == 'answer':
                    expected = [item['question'] for item in interaction(state.control().get('question_case') == 'long')['questions']]
                    answers = body.get('answers')
                    if not isinstance(answers, dict) or set(answers) != set(expected) or any(not isinstance(answers[key], str) or not answers[key].strip() for key in expected):
                        return self.tracked_reply('POST', path, 400, {'code': 'required'}, self.operation_id())
                response = {'interaction': {'id': 'fixture-question', 'status': 'resolved'}}
                def resolve_question():
                    state.data['question_resolved'] = True
                    state.data.setdefault('question_resolution_kinds', []).append(resolution_kind)
                status, response, _replay = state.begin_operation(
                    operation_id, path, response,
                    apply=resolve_question,
                    admit=lambda: scenario == 'question' and not state.data.get('question_resolved'),
                    denied_status=409, denied_response={'code': 'request_updated'},
                )
                self.reply(status, response)
                return
            if path == '/api/v1/task-plans/fixture-plan/start':
                response = {'plan': plan('started')}
                status, response, _replay = state.begin_operation(
                    self.operation_id(), path, response,
                    apply=lambda: state.data.__setitem__('plan_state', 'started'),
                )
                self.reply(status, response)
                return
            if path == '/api/v1/task-plans/fixture-plan/dismiss':
                response = {'plan': plan('dismissed')}
                status, response, _replay = state.begin_operation(
                    self.operation_id(), path, response,
                    apply=lambda: state.data.__setitem__('plan_state', 'dismissed'),
                )
                self.reply(status, response)
                return
            if path == '/api/v1/inbox/' + INBOX_ITEM_ID + '/handoff':
                response = {
                    'item': inbox_item(done=True),
                    'session': {
                        'id': MAIN, 'session_id': MAIN, 'machine_id': MACHINE,
                        'manager': MANAGER, 'title': 'Persona fixture',
                    },
                }
                status, response, _replay = state.begin_operation(
                    self.operation_id(), path, response,
                    apply=lambda: state.data.__setitem__('inbox_done', True),
                )
                self.reply(status, response)
                return
            if path.startswith('/api/v1/todos/') and path.endswith('/retry'):
                todo_id = path.split('/')[-2]
                if todo_id not in ('fixture-todo-failed', 'fixture-todo-interrupted'):
                    return self.tracked_reply('POST', path, 404, {'code': 'not_found'}, self.operation_id())
                todo = projected_todos([todo_id])[todo_id]
                response = {'todo': todo}
                def recover_todo():
                    recovered = state.data['recovered_todos']
                    if todo_id not in recovered:
                        recovered.append(todo_id)
                status, response, _replay = state.begin_operation(
                    self.operation_id(), path, response, apply=recover_todo,
                    admit=lambda: scenario != 'failed', denied_status=503,
                    denied_response={'code': 'synthetic_retry_failure'},
                )
                self.reply(status, response)
                return
            held_root = f'/api/v1/sessions/{MACHINE}/{LEAD_SESSION}'
            if scenario == 'held' and path == held_root + '/messages':
                operation = self.operation_id()
                try:
                    body = json.loads(payload)
                except (ValueError, UnicodeDecodeError):
                    body = None
                if not isinstance(body, dict):
                    return self.tracked_reply('POST', path, 400, {'code': 'invalid_json'}, operation)
                features = [value.strip() for value in self.headers.get('X-Persona-Features', '').split(',')]
                if not operation or body.get('client_msg_id') != operation or 'agent_message_hold' not in features:
                    return self.tracked_reply('POST', path, 409, {'delivered': 'none'}, operation)
                held = held_message(operation, body.get('text', ''))
                held['images'] = len(body.get('images', []))
                status, response, replay = state.begin_operation(
                    operation, path, {'delivered': 'later', 'undelivered': held}, 202,
                    apply=lambda: state.data['held'].append(held),
                )
                self.reply(status, response)
                if not replay:
                    state.announce_held()
                return
            if scenario == 'held' and path.startswith(held_root + '/held/') and path.endswith('/retry'):
                return self.act_on_held(path, True)
            if path.startswith('/api/v1/sessions/') and path.endswith('/messages'):
                operation_id = self.operation_id()
                status = 202 if scenario == 'pending' else 200
                response = {'state': 'pending' if status == 202 else 'done'}
                status, response, replay = state.begin_operation(operation_id, path, response, status)
                if status == 202:
                    response = {'state': 'pending'}
                self.reply(status, response)
                return
            if path.startswith('/api/v1/sessions/') and path.endswith('/interrupt'):
                response = {'state': 'done'}
                status, response, _replay = state.begin_operation(self.operation_id(), path, response)
                self.reply(status, response)
                return
            return self.tracked_reply('POST', path, 404, {'code': 'not_found'}, self.operation_id())

        def do_PATCH(self):
            self.read_body()
            state.scenario()
            path = self.parsed_path()
            if not self.authorized():
                return self.tracked_reply('PATCH', path, 401, {'code': 'unauthorized'})
            return self.tracked_reply('PATCH', path, 404, {'code': 'not_found'})

        def do_PUT(self):
            self.read_body()
            state.scenario()
            path = self.parsed_path()
            if not self.authorized():
                return self.tracked_reply('PUT', path, 401, {'code': 'unauthorized'})
            return self.tracked_reply('PUT', path, 404, {'code': 'not_found'})

        def do_DELETE(self):
            self.read_body()
            path = self.parsed_path()
            if not self.authorized():
                return self.tracked_reply('DELETE', path, 401, {'code': 'unauthorized'})
            if state.scenario() == 'held' and path.startswith(f'/api/v1/sessions/{MACHINE}/{LEAD_SESSION}/held/'):
                with state.lock:
                    return self.act_on_held(path, False)
            return self.tracked_reply('DELETE', path, 404, {'code': 'not_found'})

        def act_on_held(self, path, retry):
            method = 'POST' if retry else 'DELETE'
            identifier = path.split('/')[-2 if retry else -1]
            row = next((row for row in state.data['held'] if row['id'] == identifier), None)

            def apply():
                if retry:
                    row.update(state='waiting', error=('Will check the chat when Test computer is back.' if identifier == 'uncertain'
                                                       else 'Will send when Test computer is back.'))
                else:
                    state.data['held'].remove(row)
            status, response, replay = state.begin_operation(
                self.operation_id(), path, {'ok': True}, apply=apply, method=method,
                admit=lambda: row and (not retry or row['state'] == 'failed' and not row.get('delete_only')),
                denied_status=409 if row else 404,
            )
            self.reply(status, response)
            if status == 200 and not replay:
                state.announce_held()

        def websocket(self, path):
            key = self.headers.get('Sec-WebSocket-Key', '')
            if not key or self.headers.get('Upgrade', '').lower() != 'websocket':
                return self.tracked_reply('GET', path, 400, {'code': 'invalid_websocket'})
            accept = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
            self.send_response(101)
            self.send_header('Upgrade', 'websocket')
            self.send_header('Connection', 'Upgrade')
            self.send_header('Sec-WebSocket-Accept', accept)
            self.end_headers()
            state.record('GET', path, '', 101)
            hello = json.dumps({
                'type': 'hello', 'hub_id': HUB_ID,
                'features': ['managers', 'client_ops'] + (['agent_message_hold'] if state.scenario() == 'held' else []),
            }, separators=(',', ':')).encode()
            try:
                with state.lock:
                    if state.scenario() == 'held':
                        state.held_listeners.add(self.connection.sendall)
                    self.wfile.write(frame(1, hello))
                    self.wfile.flush()
                while True:
                    header = self.rfile.read(2)
                    if len(header) != 2:
                        break
                    opcode, size = header[0] & 0x0f, header[1] & 0x7f
                    if size == 126:
                        size = struct.unpack('!H', self.rfile.read(2))[0]
                    elif size == 127:
                        size = struct.unpack('!Q', self.rfile.read(8))[0]
                    if size > 1024 * 1024:
                        break
                    mask = self.rfile.read(4) if header[1] & 0x80 else b''
                    data = self.rfile.read(size)
                    if len(data) != size:
                        break
                    if mask:
                        data = bytes(byte ^ mask[index % 4] for index, byte in enumerate(data))
                    if opcode == 8:
                        self.wfile.write(frame(8, data[:125]))
                        self.wfile.flush()
                        break
                    if opcode == 9:
                        self.wfile.write(frame(10, data[:125]))
                        self.wfile.flush()
                    elif opcode == 1:
                        try:
                            message = json.loads(data)
                        except (ValueError, UnicodeDecodeError):
                            continue
                        # client_hello is an announcement; echoing a hello would create a reconnect loop.
                        if isinstance(message, dict) and message.get('type') == 'get' and message.get('resource') == 'hello':
                            self.wfile.write(frame(1, hello))
                            self.wfile.flush()
            except (OSError, ssl.SSLError, struct.error, ConnectionError):
                pass
            finally:
                with state.lock:
                    state.held_listeners.discard(self.connection.sendall)
            self.close_connection = True

    class Server(ThreadingHTTPServer):
        daemon_threads = True

        def handle_error(self, *_args):
            pass

    server = Server(('127.0.0.1', args.port), Handler)
    server.socket = tls.wrap_socket(server.socket, server_side=True)
    print('Persona UI fixture ready; all responses are synthetic.', flush=True)
    server.serve_forever(poll_interval=0.2)


if __name__ == '__main__':
    main()
