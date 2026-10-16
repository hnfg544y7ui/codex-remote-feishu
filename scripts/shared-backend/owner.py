"""Local shared Codex owner. All user clients must use its public WS gateway."""
import asyncio
import csv
import functools
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import time
from urllib.parse import urlsplit

import websockets

READ_ONLY = {'initialize', 'thread/read', 'thread/list', 'thread/loaded/list',
             'thread/turns/list', 'thread/items/list', 'config/read', 'model/list',
             'account/read', 'thread/goal/get'}


@functools.lru_cache(maxsize=1)
def windows_owner_sid():
    output = subprocess.check_output(['whoami.exe', '/user', '/fo', 'csv', '/nh'],
                                     creationflags=subprocess.CREATE_NO_WINDOW, text=True)
    return next(csv.reader(output.strip().splitlines()))[1]


def private_directory(path):
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if os.name == 'nt':
        subprocess.run(['icacls.exe', str(path), '/inheritance:r', '/grant:r',
                        '*' + windows_owner_sid() + ':(OI)(CI)F', '*S-1-5-18:(OI)(CI)F',
                        '*S-1-5-32-544:(OI)(CI)F'], check=True, capture_output=True,
                       creationflags=subprocess.CREATE_NO_WINDOW)
    else:
        os.chmod(path, 0o700)

def atomic_json(path, value):
    path = Path(path)
    private_directory(path.parent)
    temporary = path.with_name(path.name + '.tmp')
    with temporary.open('w', encoding='utf-8') as stream:
        os.chmod(temporary, 0o600)
        json.dump(value, stream, ensure_ascii=False)
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)


def digest(binary):
    checksum = hashlib.sha256()
    with Path(binary).open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            checksum.update(chunk)
    return checksum.hexdigest()


def version_valid(version):
    return isinstance(version, str) and re.fullmatch(r'\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?', version) is not None


class Owner:
    def __init__(self, config):
        self.config = config
        self.root = Path(config['directory']).resolve()
        private_directory(self.root)
        self.selection_file = self.root / 'selection.json'
        self.selection = json.loads(self.selection_file.read_text(encoding='utf-8')) if self.selection_file.exists() else None
        self.child = None
        self.busy = False
        self.frozen = False
        self.state = 'starting'
        self.pending = {}
        self.uncertain_requests = set()
        self.next_connection_id = 0
        self.upgrade_lock = asyncio.Lock()
        self.generation = 0
        self.log = (self.root / 'backend.log').open('ab')

    def status(self, problem=''):
        selected = self.selection or {}
        return dict(endpoint=self.config['endpoint'], version=selected.get('version', ''),
                    generation=self.generation,
                    previousVersion=selected.get('previous', {}).get('version', ''),
                    state=self.state, busy=self.busy, problem=problem)

    async def command(self, *args, timeout=180):
        options = dict(stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
        if os.name == 'nt':
            options['creationflags'] = subprocess.CREATE_NO_WINDOW
        process = await asyncio.create_subprocess_exec(*map(str, args), **options)
        try:
            stdout, stderr = await asyncio.wait_for(process.communicate(), timeout)
        except BaseException:
            process.kill()
            await process.wait()
            raise
        if process.returncode:
            self.log.write(stderr[-16384:])
            self.log.flush()
            # Never return raw plugin/config output, which can contain secrets.
            raise RuntimeError('CLI validation/preparation failed (exit %s); inspect owner logs' % process.returncode)
        return stdout.decode('utf-8').strip()

    async def rpc(self, method, params):
        async with websockets.connect(self.config['nativeEndpoint'], proxy=None, open_timeout=4, max_size=32 << 20) as ws:
            async def call(n, name, body):
                await ws.send(json.dumps(dict(id=n, method=name, params=body)))
                while True:
                    message = json.loads(await asyncio.wait_for(ws.recv(), 8))
                    if message.get('id') == n and 'method' not in message:
                        if 'error' in message:
                            raise RuntimeError('Native backend rejected maintenance probe')
                        return message['result']
            await call(1, 'initialize', dict(clientInfo=dict(name='codex-remote-owner', version='1'),
                                           capabilities=dict(experimentalApi=True)))
            await ws.send(json.dumps(dict(method='initialized')))
            return await call(2, method, params)

    async def native_idle(self):
        loaded = (await self.rpc('thread/loaded/list', {})).get('data')
        if not isinstance(loaded, list) or len(loaded) > 256:
            raise RuntimeError('Cannot establish native thread state')
        for thread_id in loaded:
            thread = (await self.rpc('thread/read', dict(threadId=thread_id, includeTurns=False))).get('thread', {})
            # A terminal systemError is not active work. Unknown states fail closed.
            if thread.get('status', {}).get('type') not in ('idle', 'systemError'):
                raise RuntimeError('Shared backend has active work; retry when all clients are idle')
            goal_result = await self.rpc('thread/goal/get', dict(threadId=thread_id))
            if 'goal' not in goal_result:
                raise RuntimeError('Cannot establish native Goal state')
            goal = goal_result['goal']
            if goal and goal.get('status') == 'active':
                raise RuntimeError('An active Goal can continue without new client input; pause it before maintenance')

    async def launch(self, selected):
        if digest(selected['binary']) != selected['sha256']:
            raise RuntimeError('Selected CLI integrity check failed')
        actual = await self.command(selected['binary'], '--version', timeout=10)
        if actual.removeprefix('codex-cli ').strip() != selected['version']:
            raise RuntimeError('Selected CLI version changed')
        if self.generation:
            # The public endpoint never changes. A new internal port avoids
            # Windows TIME_WAIT preventing both the upgrade and its rollback.
            host = urlsplit(self.config['nativeEndpoint']).hostname
            family = socket.AF_INET6 if ':' in host else socket.AF_INET
            with socket.socket(family) as reservation:
                reservation.bind((host, 0))
                port = reservation.getsockname()[1]
            literal = '[' + host + ']' if ':' in host else host
            self.config['nativeEndpoint'] = 'ws://' + literal + ':' + str(port)
        self.generation += 1
        args = [selected['binary'], *self.config.get('launchArgs', []), 'app-server', '--listen', self.config['nativeEndpoint']]
        options = dict(stdin=asyncio.subprocess.DEVNULL, stdout=self.log, stderr=self.log)
        env = dict(os.environ)
        env.pop('CODEX_APP_SERVER_WS_URL', None)
        env.pop('CODEX_APP_SERVER_FORCE_CLI', None)
        env.pop('NOTIFY_SOCKET', None)
        options['env'] = env
        if os.name == 'nt':
            options['creationflags'] = subprocess.CREATE_NO_WINDOW | subprocess.CREATE_NEW_PROCESS_GROUP
        else:
            options['start_new_session'] = True
        self.child = await asyncio.create_subprocess_exec(*args, **options)
        async def ready():
            while self.child.returncode is None:
                try:
                    await self.rpc('thread/loaded/list', {})
                    return
                except Exception:
                    await asyncio.sleep(.25)
            raise RuntimeError('Native backend readiness failed')
        await asyncio.wait_for(ready(), 25)

    async def stop(self):
        if self.child and self.child.returncode is None:
            if os.name == 'nt':
                self.child.terminate()
            else:
                os.killpg(self.child.pid, signal.SIGTERM)
            try:
                await asyncio.wait_for(self.child.wait(), 10)
            except asyncio.TimeoutError:
                if os.name == 'nt':
                    await self.command('taskkill.exe', '/PID', str(self.child.pid), '/T', '/F', timeout=10)
                else:
                    os.killpg(self.child.pid, signal.SIGKILL)
                await self.child.wait()

    async def boot(self):
        if self.selection is None:
            binary = str(Path(self.config['initialBinary']).resolve(strict=True))
            version = (await self.command(binary, '--version', timeout=10)).removeprefix('codex-cli ').strip()
            if not version_valid(version):
                raise RuntimeError('Initial CLI did not report a supported version')
            self.selection = dict(binary=binary, version=version, sha256=digest(binary))
        await self.launch(self.selection)
        atomic_json(self.selection_file, self.selection)
        self.state = 'running'

    async def prepare(self, version):
        root = self.root / 'releases'
        data = await self.command(self.config['remoteBinary'], 'codex-runtime', 'prepare', '--root', root,
                                  '--version', version, '--retry-failed')
        release = json.loads(data)
        binary = Path(release['binary']).resolve(strict=True)
        binary.relative_to(root.resolve())
        if release['version'] != version:
            raise RuntimeError('Prepared version changed')
        return dict(binary=str(binary), version=version, sha256=digest(binary))

    async def upgrade(self, request):
        if self.upgrade_lock.locked():
            raise RuntimeError('Another shared backend maintenance is running')
        async with self.upgrade_lock:
            target, expected = request.get('version'), request.get('expectedVersion')
            if not version_valid(target) or expected != self.selection['version']:
                raise RuntimeError('Version changed; check for updates again')
            if self.state != 'running' or target == expected:
                raise RuntimeError('Backend is not ready or target is already running')
            self.busy = True
            previous = dict(self.selection)
            original = dict(self.selection)
            previous.pop('previous', None)
            stopped = False
            try:
                if request.get('rollback'):
                    selected = self.selection.get('previous', {})
                    if selected.get('version') != target:
                        raise RuntimeError('Previous verified release changed')
                else:
                    selected = await self.prepare(target)  # Download before fencing clients.
                # Fence the public gateway BEFORE inspecting native state. Every
                # admitted client RPC is tracked until acknowledged; no turn/start
                # can race the idle check, including messages from the desktop App.
                self.frozen = True
                if self.pending:
                    if self.uncertain_requests:
                        raise RuntimeError('A disconnected client left unconfirmed work; inspect native tasks, then restart the owner only when idle before retrying')
                    raise RuntimeError('Shared clients have pending requests; retry when idle')
                await self.native_idle()
                self.state = 'switching'
                stopped = True
                await self.stop()
                await self.launch(selected)
                self.selection = dict(selected, previous=previous)
                atomic_json(self.selection_file, self.selection)
                self.state = 'running'
            except BaseException:
                if stopped:
                    self.state = 'restoring'
                    try:
                        await self.stop()
                        await self.launch(previous)
                        self.selection = original
                        atomic_json(self.selection_file, self.selection)
                        self.state = 'running'
                    except BaseException:
                        self.state = 'failed'
                        raise RuntimeError('Upgrade and rollback failed; owner requires maintenance')
                raise
            finally:
                self.busy = False
                self.frozen = self.state != 'running'
            return self.status()

    async def proxy(self, client):
        if self.frozen or self.state != 'running':
            await client.close(1013, 'Shared backend maintenance; reconnect later')
            return
        own_pending = set()
        self.next_connection_id += 1
        connection_id = self.next_connection_id
        try:
            async with websockets.connect(self.config['nativeEndpoint'], proxy=None, max_size=32 << 20) as native:
                send_lock = asyncio.Lock()

                async def send_client(frame):
                    async with send_lock:
                        await client.send(frame)

                async def incoming():
                    async for frame in client:
                        message = json.loads(frame)
                        if 'method' in message and self.frozen:
                            if 'id' in message:
                                await send_client(json.dumps(dict(id=message['id'], error=dict(code=-32010, message='Shared backend maintenance; retry when ready'))))
                            continue
                        if 'id' in message and 'method' in message:
                            key = (connection_id, json.dumps(message['id']))
                            self.pending[key] = message['method']
                            own_pending.add(key)
                        await native.send(frame)

                async def outgoing():
                    async for frame in native:
                        message = json.loads(frame)
                        if 'id' in message and 'method' not in message:
                            key = (connection_id, json.dumps(message['id']))
                            self.pending.pop(key, None)
                            own_pending.discard(key)
                        await send_client(frame)

                tasks = [asyncio.create_task(incoming()), asyncio.create_task(outgoing())]
                try:
                    await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
                finally:
                    for task in tasks:
                        task.cancel()
                    await asyncio.gather(*tasks, return_exceptions=True)
        except Exception:
            pass
        finally:
            # Unacknowledged work on a disconnected client remains uncertain.
            # Keep its fence: loss of a socket is not proof that native work ended.
            for key in own_pending:
                if self.pending.get(key) in READ_ONLY:
                    self.pending.pop(key, None)
                elif key in self.pending:
                    self.uncertain_requests.add(key)
            await client.close(1012, 'Shared backend connection changed')
