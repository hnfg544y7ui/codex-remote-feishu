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
from immediate_upgrade import ImmediateUpgrade, NativeRPCError, get_goal
from runtime_environment import RuntimeEnvironment
from runtime_selection import verify as verify_runtime_selection
from runtime_selection import prepared_bundle

READ_ONLY = {'initialize', 'thread/read', 'thread/list', 'thread/loaded/list',
             'thread/turns/list', 'thread/items/list', 'config/read', 'model/list',
             'account/read', 'thread/goal/get', 'mcpServerStatus/list'}


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
    read_only_methods = READ_ONLY
    valid_version = staticmethod(version_valid)

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
        self.runtime_env = RuntimeEnvironment(self)
        self.immediate_upgrade = ImmediateUpgrade(self)

    def status(self, problem=''):
        selected = self.selection or {}
        return dict(endpoint=self.config['endpoint'], version=selected.get('version', ''),
                    generation=self.generation, immediateUpgradeSupported=self.immediate_upgrade.supported,
                    previousVersion=selected.get('previous', {}).get('version', ''),
                    state=self.state, busy=self.busy, problem=problem, **self.runtime_env.status())

    async def command(self, *args, timeout=180, env=None):
        options = dict(stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
        if env is not None:
            options['env'] = env
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
            self.log.write(('Runtime command failed (exit %s); raw plugin output omitted\n' % process.returncode).encode())
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
                            error = message['error']
                            raise NativeRPCError(name, error.get('code'), error.get('message'))
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
            goal = await get_goal(self, thread_id)
            if goal and goal.get('status') == 'active':
                raise RuntimeError('An active Goal can continue without new client input; pause it before maintenance')

    async def verify_selection(self, selected, runtime=None):
        return await verify_runtime_selection(self, selected, runtime=runtime)

    async def launch(self, selected, runtime=None):
        runtime = runtime or await self.runtime_env.prepare(selected)
        selected = await self.verify_selection(selected, runtime=runtime)
        if Path(runtime['environment'].get('CODEX_CLI_PATH', '')).resolve() != Path(selected['binary']).resolve():
            raise RuntimeError('Runtime environment does not match the selected CLI')
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
        args = [selected['binary'], *self.config.get('launchArgs', []), *runtime['args'], 'app-server', '--listen', self.config['nativeEndpoint']]
        options = dict(stdin=asyncio.subprocess.DEVNULL, stdout=self.log, stderr=self.log)
        options['env'] = runtime['environment']
        if os.name == 'nt':
            options['creationflags'] = subprocess.CREATE_NO_WINDOW | subprocess.CREATE_NEW_PROCESS_GROUP
        else:
            options['start_new_session'] = True
        self.child = await asyncio.create_subprocess_exec(*args, **options)
        async def ready():
            while self.child.returncode is None:
                try:
                    await self.rpc('thread/loaded/list', {})
                    await self.runtime_env.health(runtime)
                    return
                except Exception:
                    await asyncio.sleep(.25)
            raise RuntimeError('Native backend readiness failed')
        await asyncio.wait_for(ready(), 25)
        self.runtime_env.accept(runtime)

    async def stop(self, force=False):
        if self.child and self.child.returncode is None:
            if os.name == 'nt':
                if force:
                    await self.command('taskkill.exe', '/PID', str(self.child.pid), '/T', '/F', timeout=10)
                else:
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
        if force and self.child and os.name != 'nt':
            # Parent exit alone does not establish descendant exit. The
            # native process owns this isolated process group.
            try:
                os.killpg(self.child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass

    async def boot(self):
        runtime = None
        if self.selection is None:
            binary = str(Path(self.config['initialBinary']).resolve(strict=True))
            runtime = await self.runtime_env.prepare(dict(binary=binary))
            version = (await self.command(binary, '--version', timeout=10, env=runtime['environment'])).removeprefix('codex-cli ').strip()
            if not version_valid(version):
                raise RuntimeError('Initial CLI did not report a supported version')
            self.selection = dict(binary=binary, version=version, sha256=digest(binary))
        runtime = runtime or await self.runtime_env.prepare(self.selection)
        self.selection = await self.verify_selection(self.selection, runtime=runtime)
        await self.launch(self.selection, runtime=runtime)
        atomic_json(self.selection_file, self.selection)
        self.state = 'running'
        self.runtime_env.publish()

    async def prepare(self, version):
        root = self.root / 'releases'
        runtime = await self.runtime_env.prepare(self.selection)
        data = await self.command(self.config['remoteBinary'], 'codex-runtime', 'prepare', '--root', root,
                                  '--version', version, '--retry-failed', env=runtime['environment'])
        release = json.loads(data)
        selected = prepared_bundle(root, version, release)
        target_runtime = await self.runtime_env.prepare(selected, snapshot=runtime['snapshot'])
        return await self.verify_selection(selected, runtime=target_runtime)

    async def upgrade(self, request):
        if self.upgrade_lock.locked():
            raise RuntimeError('Another shared backend maintenance is running')
        async with self.upgrade_lock:
            immediate = self.immediate_upgrade.validate_request(request)
            impact = self.immediate_upgrade.consume(request) if immediate else None
            target, expected = request.get('version'), request.get('expectedVersion')
            if not version_valid(target) or expected != self.selection['version']:
                raise RuntimeError('Version changed; check for updates again')
            if self.state != 'running' or target == expected:
                raise RuntimeError('Backend is not ready or target is already running')
            self.busy = True
            previous_runtime = self.runtime_env.active
            previous = dict(self.selection)
            original = dict(self.selection)
            previous.pop('previous', None)
            stopped = False
            try:
                previous = await self.verify_selection(previous, runtime=previous_runtime)
                previous.pop('previous', None)
                if request.get('rollback'):
                    selected = self.selection.get('previous', {})
                    if selected.get('version') != target:
                        raise RuntimeError('Previous verified release changed')
                else:
                    selected = await self.prepare(target)  # Download before fencing clients.
                runtime = await self.runtime_env.prepare(selected)
                selected = await self.verify_selection(selected, runtime=runtime)
                # Fence the public gateway BEFORE inspecting native state. Every
                # admitted client RPC is tracked until acknowledged; no turn/start
                # can race the idle check, including messages from the desktop App.
                self.frozen = True
                if not immediate and self.pending:
                    if self.uncertain_requests:
                        raise RuntimeError('A disconnected client left unconfirmed work; inspect native tasks, then restart the owner only when idle before retrying')
                    raise RuntimeError('Shared clients have pending requests; retry when idle')
                if immediate:
                    await self.immediate_upgrade.interrupt(impact)
                else:
                    await self.native_idle()
                self.state = 'switching'
                stopped = True
                if immediate:
                    await self.stop(force=True)
                    self.immediate_upgrade.clear_after_exit()
                else:
                    await self.stop()
                await self.launch(selected, runtime=runtime)
                self.selection = dict(selected, previous=previous)
                atomic_json(self.selection_file, self.selection)
                self.state = 'running'
            except BaseException as error:
                if stopped:
                    self.state = 'restoring'
                    try:
                        if immediate:
                            await self.stop(force=True)
                            self.immediate_upgrade.clear_after_exit()
                        else:
                            await self.stop()
                        await self.launch(previous, runtime=previous_runtime)
                        self.selection = original
                        atomic_json(self.selection_file, self.selection)
                        self.state = 'running'
                    except BaseException:
                        self.state = 'failed'
                        suffix = '; confirmed work may already be paused or interrupted' if immediate else ''
                        raise RuntimeError('Upgrade and rollback failed; owner requires maintenance' + suffix)
                if immediate and stopped and isinstance(error, Exception):
                    raise RuntimeError(str(error) + '; confirmed work was interrupted and paused Goals were not resumed') from error
                raise
            finally:
                self.busy = False
                self.frozen = self.state != 'running'
                self.runtime_env.publish()
            return self.status()

    async def proxy(self, client):
        if self.frozen or self.state != 'running':
            await client.close(1013, 'Shared backend maintenance; reconnect later')
            return
        own_pending = set()
        generation = self.generation
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
                        if generation != self.generation:
                            await client.close(1012, 'Shared backend generation changed; reconnect')
                            return
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
