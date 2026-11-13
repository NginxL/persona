#!/usr/bin/env python3
"""Owned loopback TLS fixture for the native transport tests; no external services."""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import socket
import ssl
import struct
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HUB_ID = '00000000-0000-4000-8000-000000000001'
TOKEN = 'synthetic-fixture-token'


def certificate(directory, name):
    key, cert = directory / (name + '.key'), directory / (name + '.crt')
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                    '-keyout', str(key), '-out', str(cert), '-subj', '/CN=localhost',
                    '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1,IP:0:0:0:0:0:0:0:1'],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    key.chmod(0o600)
    public = subprocess.check_output(['openssl', 'x509', '-in', str(cert), '-pubkey', '-noout'])
    der = subprocess.check_output(['openssl', 'pkey', '-pubin', '-outform', 'DER'], input=public)
    pin = base64.urlsafe_b64encode(hashlib.sha256(der).digest()).decode().rstrip('=')
    return key, cert, pin


def frame(opcode, data, final=True):
    prefix = bytes([(0x80 if final else 0) | opcode])
    if len(data) < 126:
        return prefix + bytes([len(data)]) + data
    if len(data) < 65536:
        return prefix + b'\x7e' + struct.pack('!H', len(data)) + data
    return prefix + b'\x7f' + struct.pack('!Q', len(data)) + data


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--runtime', type=Path, required=True)
    parser.add_argument('--port', type=int, default=38664)
    parser.add_argument('--base-prefix', default='')
    parser.add_argument('--expected-host', default='')
    args = parser.parse_args()
    prefix = args.base_prefix.rstrip('/')
    if prefix and (not prefix.startswith('/') or '//' in prefix or any(part in ('.', '..') for part in prefix.split('/')) or any(char in prefix for char in '?\\#')):
        parser.error('base prefix must be a plain absolute path')
    args.runtime.mkdir(parents=True, exist_ok=True, mode=0o700)
    args.runtime.chmod(0o700)
    first, second = certificate(args.runtime, 'first'), certificate(args.runtime, 'rotated')
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(str(first[1]), str(first[0]))
    lock = threading.Lock()
    def server_name(sock, name, _context):
        sock.persona_sni = name or ''
        if args.expected_host and name != args.expected_host:
            return ssl.ALERT_DESCRIPTION_UNRECOGNIZED_NAME
    context.set_servername_callback(server_name)
    counters = {'forbidden': 0, 'ws': 0, 'data': 0, 'close': 0, 'anonymous_health': 0, 'authenticated_health': 0}

    client_hello = {}
    websocket_identities = []

    class Handler(BaseHTTPRequestHandler):
        protocol_version = 'HTTP/1.1'

        def log_message(self, *_args):
            pass

        def reply(self, status, value):
            data = value if isinstance(value, bytes) else json.dumps(value).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.send_header('Connection', 'close')
            self.end_headers()
            self.wfile.write(data)
            self.close_connection = True

        def authorized(self):
            if self.headers.get('Authorization') != 'Bearer ' + TOKEN:
                self.reply(401, {'code': 'unauthorized'})
                return False
            return True

        def identity(self):
            return {'host': self.headers.get('Host', ''), 'path': self.path,
                    'sni': getattr(self.connection, 'persona_sni', '')}

        def route(self):
            if args.expected_host and self.headers.get('Host') != args.expected_host + ':' + str(args.port):
                self.reply(421, {'code': 'wrong_host'})
                return None
            if prefix and not self.path.startswith(prefix + '/'):
                self.reply(404, {'code': 'wrong_prefix'})
                return None
            return self.path[len(prefix):].split('?', 1)[0]

        def do_POST(self):
            path = self.route()
            if path is None:
                return
            if not self.authorized():
                return
            size = min(65536, max(0, int(self.headers.get('Content-Length', '0'))))
            self.rfile.read(size)
            if path == '/api/v1/auth/tokens':
                self.reply(200, {'id': 'fixture-device', 'token': TOKEN})
            elif path == '/api/v1/test/pending':
                self.reply(202, {'state': 'pending'})
            elif path == '/api/v1/files/transfer':
                if 'invalid_utf8=1' in self.path:
                    self.reply(202, b'\xff')
                else:
                    self.reply(202, {'id': 'synthetic-transfer', 'state': 'running', 'from': 'source', 'to': 'target', 'name': 'example.txt', 'bytes': 0, 'total': 12})
            elif path == '/api/v1/test/rotate':
                context.load_cert_chain(str(second[1]), str(second[0]))
                self.reply(200, {'state': 'rotated'})
            else:
                self.reply(404, {'code': 'not_found'})

        def do_PUT(self):
            path = self.route()
            if path is None or not self.authorized():
                return
            size = min(65536, max(0, int(self.headers.get('Content-Length', '0'))))
            self.rfile.read(size)
            if path == '/api/v1/test/invalid-utf8':
                self.reply(200, b'\xff')
            else:
                self.reply(404, {'code': 'not_found'})

        do_DELETE = do_PUT

        def do_GET(self):
            path = self.route()
            if path is None:
                return
            if path == '/api/v1/health':
                with lock:
                    counters['authenticated_health' if self.headers.get('Authorization') else 'anonymous_health'] += 1
                self.reply(200, {'ok': True, 'hub_id': HUB_ID, 'hub_name': 'Synthetic Hub'})
                return
            if not self.authorized():
                return
            if path == '/api/v1/test/stats':
                with lock:
                    values = dict(counters)
                self.reply(200, values)
            elif path == '/api/v1/test/identity':
                with lock:
                    identities = list(websocket_identities)
                self.reply(200, {'http': self.identity(), 'websocket': identities})
            elif path == '/api/v1/test/client':
                with lock:
                    hello = dict(client_hello)
                self.reply(200, {'client': self.headers.get('X-Persona-Client', ''), 'legacy': self.headers.get('X-Jarvis-Client', ''), 'source': self.headers.get('X-Persona-Source', ''), 'locale': self.headers.get('Accept-Language', ''), 'hello': hello})
            elif path == '/api/v1/test/forbidden':
                with lock:
                    counters['forbidden'] += 1
                self.reply(200, {'unexpected': True})
            elif path == '/api/v1/test/oversized':
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(4 * 1024 * 1024 + 1))
                self.send_header('Connection', 'close')
                self.end_headers()
                try:
                    for _ in range(65):
                        self.wfile.write(b'x' * 65536)
                except (BrokenPipeError, ConnectionResetError, ssl.SSLError):
                    pass
                self.close_connection = True
            elif path in ['/api/v1/ws', '/api/v1/test/large-ws']:
                self.websocket(path)
            else:
                self.reply(404, {'code': 'not_found'})

        def websocket(self, path):
            key = self.headers.get('Sec-WebSocket-Key', '')
            accept = base64.b64encode(hashlib.sha1((key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
            self.send_response(101)
            self.send_header('Upgrade', 'websocket')
            self.send_header('Connection', 'Upgrade')
            self.send_header('Sec-WebSocket-Accept', accept)
            self.end_headers()
            with lock:
                counters['ws'] += 1
                websocket_identities.append(self.identity())
                del websocket_identities[:-16]
            self.connection.settimeout(3)
            try:
                if path.endswith('large-ws'):
                    self.wfile.write(b'\x81\x7f' + struct.pack('!Q', 2 * 1024 * 1024))
                else:
                    hello = json.dumps({'type': 'hello', 'hub_id': HUB_ID, 'features': []}).encode()
                    half = len(hello) // 2
                    self.wfile.write(frame(1, hello[:half], False) + frame(0, hello[half:]))
                while True:
                    head = self.rfile.read(2)
                    if len(head) != 2:
                        break
                    opcode, size = head[0] & 15, head[1] & 127
                    if size == 126:
                        size = struct.unpack('!H', self.rfile.read(2))[0]
                    elif size == 127:
                        size = struct.unpack('!Q', self.rfile.read(8))[0]
                    if size > 1024 * 1024:
                        break
                    mask = self.rfile.read(4) if head[1] & 128 else b''
                    data = self.rfile.read(size)
                    if mask:
                        data = bytes(value ^ mask[index % 4] for index, value in enumerate(data))
                    if len(data) != size:
                        break
                    if opcode == 1:
                        try:
                            message = json.loads(data)
                            if message.get('type') == 'client_hello':
                                with lock:
                                    client_hello.clear()
                                    client_hello.update({key: message.get(key) for key in ('platform', 'version', 'build', 'features', 'locale')})
                        except (ValueError, AttributeError):
                            pass
                    with lock:
                        counters['close' if opcode == 8 else 'data'] += 1
                    if opcode == 8:
                        self.wfile.write(frame(8, b'\x03\xe8'))
                        break
            except (OSError, ssl.SSLError, struct.error):
                pass
            self.close_connection = True

    class Server(ThreadingHTTPServer):
        daemon_threads = True

        def handle_error(self, *_args):
            pass

    server = Server(('127.0.0.1', args.port), Handler)
    server.socket = context.wrap_socket(server.socket, server_side=True)
    config = {'address': 'https://localhost:' + str(args.port) + prefix, 'pin': first[2], 'rotatedPin': second[2], 'hubId': HUB_ID}
    config_path = args.runtime / 'fixture.json'
    config_path.write_text(json.dumps(config))
    config_path.chmod(0o600)
    print('Native transport fixture ready on owned loopback port', flush=True)
    server.serve_forever(poll_interval=0.2)


if __name__ == '__main__':
    main()
