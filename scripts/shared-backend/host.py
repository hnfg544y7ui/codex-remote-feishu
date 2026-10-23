"""Run with a trusted, owner-only config; never expose the native port to clients."""
import argparse
import asyncio
import hashlib
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ipaddress
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import threading
from urllib.parse import urlsplit

import websockets
from owner import Owner, atomic_json


def notify_ready():
    address = os.environ.get('NOTIFY_SOCKET')
    if os.name == 'posix' and address:
        with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as channel:
            channel.connect('\0' + address[1:] if address.startswith('@') else address)
            channel.sendall(b'READY=1\nSTATUS=Shared Codex backend ready')


def address(raw, scheme):
    u = urlsplit(raw)
    if u.scheme != scheme or not ipaddress.ip_address(u.hostname).is_loopback or not u.port or u.username or u.query or u.fragment or u.path not in ('', '/'):
        raise ValueError('Only literal local loopback endpoints are supported')
    return u.hostname, u.port


def handler(owner, loop, token):
    class Control(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass  # Never log bearer credentials or file paths.

        def respond(self, code, value):
            raw = json.dumps(value).encode()
            self.send_response(code)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def authorized(self):
            return not self.headers.get('Origin') and hmac.compare_digest(self.headers.get('Authorization', ''), 'Bearer ' + token)

        def do_GET(self):
            if not self.authorized():
                return self.respond(403, {})
            if self.path != '/v1/status':
                return self.respond(404, {})
            async def snapshot():
                return owner.status()
            self.respond(200, asyncio.run_coroutine_threadsafe(snapshot(), loop).result(5))

        def do_POST(self):
            if not self.authorized():
                return self.respond(403, {})
            if self.path not in ('/v1/upgrade', '/v1/upgrade/immediate/preview'):
                return self.respond(404, {})
            try:
                length = int(self.headers.get('Content-Length', '0'))
                if not 0 < length < 4096:
                    raise ValueError('Invalid request size')
                self.connection.settimeout(5)
                request = json.loads(self.rfile.read(length))
                if not isinstance(request, dict) or set(request) - {'version', 'expectedVersion', 'rollback', 'immediate', 'confirmationToken'}:
                    raise ValueError('Invalid maintenance request')
                if 'rollback' in request and type(request['rollback']) is not bool:
                    raise ValueError('Invalid rollback request')
                if self.path.endswith('/preview'):
                    if set(request) - {'version', 'expectedVersion'}:
                        raise ValueError('Invalid immediate upgrade preview')
                    operation = owner.immediate_upgrade.preview(request)
                else:
                    owner.immediate_upgrade.validate_request(request)
                    operation = owner.upgrade(request)
                result = asyncio.run_coroutine_threadsafe(operation, loop).result(300)
                self.respond(200, result)
            except Exception as error:
                # A client disconnect must not cancel a switch/rollback in flight.
                self.respond(409, owner.status(str(error)))
    return Control


async def serve(config):
    public = address(config['endpoint'], 'ws')
    private = address(config['nativeEndpoint'], 'ws')
    control = address(config['controlURL'], 'http')
    if len({public[1], private[1], control[1]}) != 3:
        raise ValueError('Public, native and maintenance ports must differ')
    owner = Owner(config)
    lock = (owner.root / 'owner.lock').open('a+b')
    if os.name == 'nt':
        import msvcrt
        lock.seek(0)
        msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
    else:
        import fcntl
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    token = secrets.token_hex(32)
    stopped = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        try:
            loop.add_signal_handler(sig, stopped.set)
        except NotImplementedError:
            signal.signal(sig, lambda *_: loop.call_soon_threadsafe(stopped.set))
    server = None
    server_started = False
    watcher = None
    try:
        # Fail before launching a second server if either public port is occupied.
        async with websockets.serve(owner.proxy, *public, max_size=32 << 20):
            server = ThreadingHTTPServer(control, handler(owner, loop, token))
            await owner.boot()
            path = Path(config['stateDirectory']) / 'shared-backend-owners' / (hashlib.sha256(config['endpoint'].encode()).hexdigest() + '.json')
            atomic_json(path, dict(schemaVersion=1, endpoint=config['endpoint'], controlURL=config['controlURL'], token=token))
            threading.Thread(target=server.serve_forever, daemon=True).start()
            server_started = True
            watcher = asyncio.create_task(owner.runtime_env.monitor())
            notify_ready()
            while not stopped.is_set():
                if watcher.done():
                    if not watcher.cancelled():
                        watcher.exception()  # Retrieve without exposing runtime data.
                    owner.runtime_env.state = 'monitor_restarting'
                    watcher = asyncio.create_task(owner.runtime_env.monitor())
                if not owner.busy and owner.child.returncode is not None:
                    owner.state, owner.frozen = 'failed', True
                    raise RuntimeError('Backend exited unexpectedly; refusing silent restart during user work')
                try:
                    await asyncio.wait_for(stopped.wait(), 1)
                except asyncio.TimeoutError:
                    pass
    finally:
        if watcher:
            watcher.cancel()
            await asyncio.gather(watcher, return_exceptions=True)
        await owner.runtime_env.close()
        if server:
            if server_started:
                await asyncio.to_thread(server.shutdown)
            server.server_close()
        await owner.stop()
        owner.log.close()
        lock.close()


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', required=True)
    args = parser.parse_args()
    config_path = Path(args.config)
    if os.name != 'nt' and config_path.stat().st_mode & 0o077:
        raise RuntimeError('Owner config must have mode 0600')
    asyncio.run(serve(json.loads(config_path.read_text(encoding='utf-8'))))
