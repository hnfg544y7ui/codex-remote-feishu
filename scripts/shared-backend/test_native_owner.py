"""Opt-in real CLI smoke test; isolated CODEX_HOME, no prompts/model calls."""
import asyncio
import json
import os
from pathlib import Path
import socket
import shutil
import sys
from http.server import ThreadingHTTPServer
import secrets
import threading
import unittest
import urllib.request
from unittest.mock import AsyncMock, patch

from owner import Owner, digest
from host import handler
import websockets


def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


@unittest.skipUnless(all(os.environ.get(k) for k in ('SHARED_CODEX_TEST_BINARY', 'SHARED_CODEX_TEST_DIRECTORY', 'SHARED_CODEX_TEST_REMOTE')), 'requires explicit CLI, Remote and isolated evidence directory')
class NativeOwnerTests(unittest.IsolatedAsyncioTestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = Path(os.environ['SHARED_CODEX_TEST_DIRECTORY']).resolve()
        cls.directory.mkdir(parents=True, exist_ok=False)

    async def test_exported_bootstrap_restarts_with_complete_committed_selection(self):
        root = self.directory/'bootstrap'
        root.mkdir(mode=0o700)
        binary = os.environ['SHARED_CODEX_TEST_BINARY']
        remote = os.environ['SHARED_CODEX_TEST_REMOTE']
        environment = {k:v for k,v in os.environ.items() if not k.startswith(('CODEX_', 'OPENAI_', 'NOTIFY_SOCKET'))}
        environment['CODEX_HOME'] = str(root/'home')
        (root/'home').mkdir(mode=0o700)
        config = dict(directory=str(root/'state'), stateDirectory=str(root/'remote-state'),
                      initialBinary=binary, remoteBinary=remote, codexHome=environment['CODEX_HOME'])
        ports = set()
        while len(ports) < 3:
            ports.add(free_port())
        public, native, control = ports
        config.update(endpoint='ws://127.0.0.1:'+str(public), nativeEndpoint='ws://127.0.0.1:'+str(native),
                      controlURL='http://127.0.0.1:'+str(control))
        path = root/'config.json'
        path.write_text(json.dumps(config))
        path.chmod(0o600)
        export = await asyncio.create_subprocess_exec(remote, 'codex-runtime', 'host-export', '--directory', str(root/'sources'),
                                                      stdout=asyncio.subprocess.PIPE, env=environment)
        stdout, _ = await export.communicate()
        self.assertEqual(export.returncode, 0)
        manifest = json.loads(stdout)
        previous = None
        for iteration in range(2):
            status_file = root/'state/status.json'
            status_file.unlink(missing_ok=True)
            with (root/('startup-%s.log'%iteration)).open('ab') as log:
                process = await asyncio.create_subprocess_exec(sys.executable, manifest['bootstrap'], '--config', str(path),
                                                               '--source-root', str(root/'sources'), env=environment,
                                                               stdout=log, stderr=log)
                try:
                    for _ in range(200):
                        if status_file.exists() or process.returncode is not None:
                            break
                        await asyncio.sleep(.2)
                    self.assertIsNone(process.returncode)
                    self.assertEqual(json.loads(status_file.read_text())['state'], 'running')
                    selected = json.loads((root/'state/selection.json').read_text())
                    self.assertGreaterEqual(len(selected['components']), 3)
                    if previous:
                        self.assertEqual(selected, previous)
                    previous = selected
                    async with websockets.connect(config['endpoint'], proxy=None) as ws:
                        await ws.send(json.dumps(dict(id=1,method='initialize',params=dict(clientInfo=dict(name='isolated-test',version='1')))))
                        response=json.loads(await asyncio.wait_for(ws.recv(),8))
                        self.assertIn('result',response)
                    self.assertFalse((Path(manifest['directory'])/'__pycache__').exists())
                finally:
                    if process.returncode is None:
                        process.terminate()
                        await asyncio.wait_for(process.wait(), 20)

    async def test_native_health_and_failed_update_restore_without_model_calls(self):
        binary = os.environ['SHARED_CODEX_TEST_BINARY']
        root = self.directory
        env = {k: v for k, v in os.environ.items() if not k.startswith(('CODEX_', 'OPENAI_'))}
        env['CODEX_HOME'] = str(root / 'codex-home')
        (root / 'codex-home').mkdir()
        with patch.dict(os.environ, env, clear=True):
            owner = Owner(dict(directory=str(root / 'owner'), initialBinary=binary,
                               remoteBinary=os.environ['SHARED_CODEX_TEST_REMOTE'],
                               endpoint='ws://127.0.0.1:' + str(free_port()),
                               nativeEndpoint='ws://127.0.0.1:' + str(free_port())))
            try:
                await owner.boot()
                await owner.native_idle()
                original = dict(owner.selection)
                old_pid = owner.child.pid
                old_generation = owner.status()['generation']
                # Wrong version is rejected before stopping the current server.
                target = '0.0.999'
                with patch.object(owner, 'prepare', AsyncMock(return_value=dict(binary=binary, version=target, sha256=digest(binary)))):
                    with self.assertRaises(RuntimeError):
                        await owner.upgrade(dict(version=target, expectedVersion=original['version']))
                self.assertEqual(owner.child.pid, old_pid)
                self.assertEqual(owner.status()['generation'], old_generation)
                self.assertEqual(owner.selection, original)
                self.assertEqual(owner.state, 'running')
                await owner.native_idle()
                await self.assert_launch_failure_restores(owner, original)
                other = os.environ.get('SHARED_CODEX_TEST_OTHER_BINARY')
                if other:
                    await self.assert_repeated_switch_and_helper_integrity(owner, other)
            finally:
                await owner.stop()
                owner.log.close()
                if owner.state != 'running':
                    print((root / 'owner/backend.log').read_text(errors='replace')[-2500:])

    async def assert_launch_failure_restores(self, owner, original):
        runtime = owner.runtime_env.active
        launch = owner.launch
        # Valid manifest/version but readiness fails after the old child stops.
        # The second launch must use the exact previous context and bundle.
        async def fail_once(selected, **kwargs):
            if selected['version'] == '0.0.999':
                raise RuntimeError('injected post-stop readiness failure')
            return await launch(selected, **kwargs)
        target = dict(original, version='0.0.999')
        verify = owner.verify_selection
        async def valid_target(selected, **kwargs):
            if selected['version'] == target['version']:
                return selected
            return await verify(selected, **kwargs)
        with patch.object(owner, 'prepare', AsyncMock(return_value=target)), \
             patch.object(owner, 'verify_selection', side_effect=valid_target), \
             patch.object(owner, 'launch', side_effect=fail_once):
            with self.assertRaisesRegex(RuntimeError, 'post-stop'):
                await owner.upgrade(dict(version=target['version'], expectedVersion=original['version']))
        self.assertEqual(owner.selection, original)
        self.assertIs(owner.runtime_env.active, runtime)
        self.assertFalse(owner.frozen or owner.busy)
        await owner.native_idle()

    async def assert_repeated_switch_and_helper_integrity(self, owner, other):
        version = (await owner.command(other, '--version')).removeprefix('codex-cli ').strip()
        # Exercise the real Go prepare -> Python bundle boundary from a copied
        # official cached release, without downloading or modifying production.
        prefix = next(p for p in Path(other).parents if (p/'remote-release.json').is_file())
        cached = owner.root/'releases/releases'/version
        shutil.copytree(prefix, cached)
        cached_manifest = cached/'remote-release.json'
        release = json.loads(cached_manifest.read_text())
        release['binary'] = str(cached/Path(other).relative_to(prefix))
        cached_manifest.write_text(json.dumps(release))
        cached_manifest.chmod(0o600)
        selected = await owner.prepare(version)
        other = selected['binary']
        self.assertEqual(selected['schemaVersion'], 1)
        self.assertEqual(set(selected['components'].values()), set(release['components'].values()))
        original = dict(owner.selection)
        token = secrets.token_hex(32)
        server = ThreadingHTTPServer(('127.0.0.1', 0), handler(owner, asyncio.get_running_loop(), token))
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        def request(body):
            call = urllib.request.Request('http://127.0.0.1:%s/v1/upgrade' % server.server_port,
                                          data=json.dumps(body).encode(),
                                          headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(call, timeout=180) as response:
                return json.load(response)
        try:
            for _ in range(2):
                with patch.object(owner, 'prepare', AsyncMock(return_value=selected)):
                    result = await asyncio.to_thread(request, dict(version=version, expectedVersion=original['version']))
                self.assertEqual(result['version'], version)
                self.assertEqual(owner.selection['components'], selected['components'])
                self.assertEqual(owner.runtime_env.active['environment']['CODEX_CLI_PATH'], other)
                await owner.native_idle()
                await asyncio.to_thread(request, dict(version=original['version'], expectedVersion=version, rollback=True))
                self.assertEqual(owner.selection['components'], original['components'])
                self.assertEqual(owner.runtime_env.active['environment']['CODEX_CLI_PATH'], original['binary'])
        finally:
            await asyncio.to_thread(server.shutdown)
            server.server_close()
            worker.join(timeout=3)
        # Restart resolves the committed bundle, not an unrelated managed.json.
        await owner.stop()
        await owner.boot()
        self.assertEqual(json.loads(owner.selection_file.read_text())['components'], original['components'])
        # Corrupt only an isolated copy, never the installed production bundle.
        copy_root = self.directory / 'damaged-bundle'
        shutil.copytree(selected['bundleRoot'], copy_root)
        copied = dict(selected, bundleRoot=str(copy_root),
                      binary=str(copy_root / Path(other).relative_to(selected['bundleRoot'])))
        helper = next(k for k in copied['components'] if 'code-mode-host' in k)
        (copy_root/helper).write_bytes(b'corrupted helper')
        old_pid = owner.child.pid
        with patch.object(owner, 'prepare', AsyncMock(return_value=copied)):
            with self.assertRaises(RuntimeError):
                await owner.upgrade(dict(version=version, expectedVersion=original['version']))
        self.assertEqual(owner.child.pid, old_pid)
        self.assertEqual(owner.state, 'running')
        self.assertFalse(owner.frozen)
        await owner.native_idle()


if __name__ == '__main__':
    unittest.main()
