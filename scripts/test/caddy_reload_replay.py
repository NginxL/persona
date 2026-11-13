#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = ["websockets==15.0.1"]
# ///
"""Exercise a real Caddy binary with loopback WebSockets and isolated reloads."""
import argparse
import asyncio
import json
import os
import socket
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path
from websockets.asyncio.server import serve
from websockets.asyncio.client import connect
from websockets.exceptions import ConnectionClosed


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def request(url, config=None):
    body = None if config is None else json.dumps(config).encode()
    req = urllib.request.Request(url, body, {'Content-Type': 'application/json'})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=3) as response:
        return response.read()


async def scenario(binary, delay):
    ports = set()
    while len(ports) < 3:
        ports.add(port())
    admin, front, upstream = ports
    peers = set()
    async def echo(ws):
        peers.add(ws)
        try:
            async for message in ws:
                await ws.send(message)
        except ConnectionClosed:
            return  # Expected when the replay closes or aborts a client.
        finally:
            peers.discard(ws)
    def config(generation):
        handler = {'handler': 'reverse_proxy', 'upstreams': [{'dial': f'127.0.0.1:{upstream}'}]}
        if delay:
            handler['stream_close_delay'] = int(delay * 1e9)
        return {'admin': {'listen': f'127.0.0.1:{admin}', 'config': {'persist': False}}, 'apps': {'http': {'servers': {'test': {
            'listen': [f'127.0.0.1:{front}'], 'routes': [
                {'match': [{'path': [f'/generation-{generation}']}], 'handle': [{'handler': 'static_response', 'body': str(generation)}]},
                {'handle': [handler]}]}}}}}
    async with serve(echo, '127.0.0.1', upstream, close_timeout=.1):
        with tempfile.TemporaryDirectory(prefix='caddy-ws-') as directory:
            source = Path(directory) / 'config.json'
            source.write_text(json.dumps(config(0)))
            with (Path(directory) / 'stderr').open('w') as log:
                proc = subprocess.Popen([str(binary), 'run', '--config', str(source)], stdout=log, stderr=log, cwd=directory,
                                        env=dict(os.environ, XDG_CONFIG_HOME=directory, XDG_DATA_HOME=directory))
                try:
                    for _ in range(100):
                        if proc.poll() is not None:
                            raise RuntimeError('Caddy exited before readiness')
                        try:
                            await asyncio.to_thread(request, f'http://127.0.0.1:{admin}/config/')
                            break
                        except (OSError, TimeoutError):
                            await asyncio.sleep(.05)
                    else:
                        raise TimeoutError('Caddy admin endpoint did not become ready')
                    async with connect(f'ws://127.0.0.1:{front}/echo', proxy=None) as ws:
                        await ws.send('before'); assert await ws.recv() == 'before'
                        started = time.monotonic()
                        await asyncio.to_thread(request, f'http://127.0.0.1:{admin}/load', config(1))
                        received = 0
                        closed = None
                        while time.monotonic() - started < (delay + 1 if delay else 2):
                            try:
                                await ws.send(str(received))
                                assert await asyncio.wait_for(ws.recv(), 1) == str(received)
                                received += 1
                                if delay and received == 20:
                                    await asyncio.to_thread(request, f'http://127.0.0.1:{admin}/load', config(2))
                                await asyncio.sleep(.05)
                            except ConnectionClosed:
                                closed = time.monotonic() - started
                                break
                        assert closed is not None
                        if delay:
                            assert delay - .15 <= closed < delay + .5, closed
                            assert received >= 20, received
                        else:
                            assert closed < 1, closed
                    recovered = time.monotonic()
                    async with connect(f'ws://127.0.0.1:{front}/echo', proxy=None) as fresh:
                        await fresh.send('reconnected'); assert await fresh.recv() == 'reconnected'
                        reconnect = time.monotonic() - recovered
                        if delay:
                            await asyncio.to_thread(request, f'http://127.0.0.1:{admin}/load', config(3))
                            await fresh.send('retained'); assert await fresh.recv() == 'retained'
                        failed = time.monotonic()
                        for peer in list(peers):
                            peer.transport.abort()
                        try:
                            await asyncio.wait_for(fresh.recv(), 1)
                            raise AssertionError('upstream failure hidden')
                        except ConnectionClosed:
                            detected = time.monotonic() - failed
                    async with connect(f'ws://127.0.0.1:{front}/echo', proxy=None, ping_interval=.1, ping_timeout=.25, close_timeout=.1) as silent:
                        await silent.send('ready'); assert await silent.recv() == 'ready'
                        if delay:
                            await asyncio.to_thread(request, f'http://127.0.0.1:{admin}/load', config(4))
                            await silent.send('draining'); assert await silent.recv() == 'draining'
                        for peer in list(peers):
                            peer.transport.pause_reading()
                        failed = time.monotonic()
                        await asyncio.wait_for(silent.wait_closed(), 1.5)
                        blackhole = time.monotonic() - failed
                        assert blackhole < 1.5 and silent.close_code == 1006
                        for peer in list(peers):
                            peer.transport.abort()
                    return {'client_heartbeat_blackhole_detection_seconds': blackhole, 'delay_seconds': delay, 'reload_close_seconds': closed, 'ordered_messages_during_old_config_retention': received, 'reconnect_seconds': reconnect, 'upstream_failure_detected_seconds': detected}
                finally:
                    proc.terminate()
                    try:
                        proc.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        proc.kill(); proc.wait()


async def main(args):
    results = []
    for delay in [0, 2]:
        for _ in range(args.repeats):
            results.append(await asyncio.wait_for(scenario(args.binary, delay), 30))
    output = {'version': subprocess.check_output([str(args.binary), 'version'], text=True, timeout=5).strip(), 'isolated_loopback_only': True, 'results': results, 'passed': True}
    args.output.write_text(json.dumps(output, indent=2) + '\n')
    print(json.dumps(output, indent=2))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--repeats', type=int, default=3)
    args = parser.parse_args()
    if args.repeats < 1:
        parser.error("--repeats must be positive")
    args.binary = args.binary.expanduser().resolve()
    if not args.binary.is_file():
        parser.error("--binary must name an existing Caddy executable")
    asyncio.run(main(args))
