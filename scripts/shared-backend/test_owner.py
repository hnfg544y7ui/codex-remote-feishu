import asyncio
import json
from http.server import ThreadingHTTPServer
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import AsyncMock, patch
import urllib.error
import urllib.request

import websockets
from owner import Owner, atomic_json, digest
from host import address, handler


class UpgradeTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.owner = Owner(dict(directory=str(self.root), endpoint='ws://127.0.0.1:17550'))
        self.old = dict(binary='old', version='0.162.1', sha256='old-sha')
        self.new = dict(binary='new', version='0.163.0', sha256='new-sha')
        self.owner.selection = dict(self.old)
        self.owner.state = 'running'
        self.owner.prepare = AsyncMock(return_value=self.new)
        self.owner.native_idle = AsyncMock()
        self.owner.stop = AsyncMock()
        self.owner.launch = AsyncMock()
        self.owner.verify_selection = AsyncMock(side_effect=lambda selected, **kwargs: dict(selected))
        self.owner.runtime_env.prepare = AsyncMock(return_value=dict(environment={}, args=[], snapshot=None, fingerprint=None))

    def tearDown(self):
        self.owner.log.close()
        self.temp.cleanup()

    def request(self, **extra):
        return dict(version='0.163.0', expectedVersion='0.162.1', **extra)

    async def test_success_is_fenced_until_health_and_selection_commit(self):
        async def launch(selected, **kwargs):
            self.assertTrue(self.owner.frozen)
            self.assertEqual(selected, self.new)
        self.owner.launch.side_effect = launch
        result = await self.owner.upgrade(self.request())
        self.assertEqual(result['version'], '0.163.0')
        self.assertEqual(result['previousVersion'], '0.162.1')
        self.assertFalse(self.owner.frozen)
        self.assertEqual(json.loads(self.owner.selection_file.read_text())['version'], '0.163.0')

    async def test_active_native_or_pending_client_cannot_stop_backend(self):
        for pending in (False, True):
            self.owner.pending = {('client', '1'): 'turn/start'} if pending else {}
            self.owner.native_idle.side_effect = RuntimeError('native active')
            with self.assertRaises(RuntimeError):
                await self.owner.upgrade(self.request())
            self.owner.stop.assert_not_awaited()
            self.assertFalse(self.owner.frozen)
            self.assertEqual(self.owner.selection, self.old)

    async def test_startup_failure_restores_verified_previous_release(self):
        self.owner.launch.side_effect = [RuntimeError('new startup failed'), None]
        with self.assertRaisesRegex(RuntimeError, 'new startup failed'):
            await self.owner.upgrade(self.request())
        self.assertEqual(self.owner.launch.await_args_list[-1].args[0], self.old)
        self.assertEqual(self.owner.selection, self.old)
        self.assertEqual(self.owner.state, 'running')
        self.assertFalse(self.owner.frozen)

    async def test_double_failure_keeps_admission_closed(self):
        self.owner.launch.side_effect = RuntimeError('cannot start')
        with self.assertRaisesRegex(RuntimeError, 'rollback failed'):
            await self.owner.upgrade(self.request())
        self.assertEqual(self.owner.state, 'failed')
        self.assertTrue(self.owner.frozen)

    async def test_stale_confirmation_and_unverified_rollback_do_not_download(self):
        for request in (dict(version='0.163.0', expectedVersion='0.161.0'), self.request(rollback=True)):
            with self.assertRaises(RuntimeError):
                await self.owner.upgrade(request)
        self.owner.prepare.assert_not_awaited()
        self.owner.stop.assert_not_awaited()

    async def test_concurrent_operation_is_rejected(self):
        async with self.owner.upgrade_lock:
            with self.assertRaisesRegex(RuntimeError, 'Another'):
                await self.owner.upgrade(self.request())
        self.owner.prepare.assert_not_awaited()

    async def test_rollback_uses_previous_without_registry_access(self):
        self.owner.selection['previous'] = dict(self.new)
        await self.owner.upgrade(self.request(rollback=True))
        self.owner.prepare.assert_not_awaited()
        self.assertEqual(self.owner.selection['version'], '0.163.0')

    async def test_real_gateway_rejects_racing_app_turn_without_forwarding(self):
        received = []
        async def native(ws):
            async for frame in ws:
                request = json.loads(frame)
                received.append(request)
                await ws.send(json.dumps(dict(id=request['id'], result={})))
        async with websockets.serve(native, '127.0.0.1', 0) as backend:
            self.owner.config['nativeEndpoint'] = 'ws://127.0.0.1:' + str(backend.sockets[0].getsockname()[1])
            async with websockets.serve(self.owner.proxy, '127.0.0.1', 0) as gateway:
                url = 'ws://127.0.0.1:' + str(gateway.sockets[0].getsockname()[1])
                async with websockets.connect(url, proxy=None) as client:
                    await client.send(json.dumps(dict(id=1, method='thread/read', params={})))
                    await client.recv()
                    self.owner.frozen = True
                    await client.send(json.dumps(dict(id=2, method='turn/start', params={})))
                    response = json.loads(await client.recv())
                    self.assertEqual(response['error']['code'], -32010)
                    self.assertEqual([r['method'] for r in received], ['thread/read'])

    async def test_native_active_thread_blocks_upgrade_even_if_remote_is_idle(self):
        self.owner.native_idle = Owner.native_idle.__get__(self.owner)
        self.owner.rpc = AsyncMock(side_effect=[dict(data=['app-only-thread']), dict(thread=dict(status=dict(type='active')))])
        with self.assertRaisesRegex(RuntimeError, 'active work'):
            await self.owner.upgrade(self.request())
        self.owner.stop.assert_not_awaited()

    async def test_active_goal_on_idle_thread_also_blocks_switch(self):
        self.owner.native_idle = Owner.native_idle.__get__(self.owner)
        self.owner.rpc = AsyncMock(side_effect=[dict(data=['goal-thread']), dict(thread=dict(status=dict(type='idle'))), dict(goal=dict(status='active'))])
        with self.assertRaisesRegex(RuntimeError, 'active Goal'):
            await self.owner.upgrade(self.request())
        self.owner.stop.assert_not_awaited()


class ValidationTests(unittest.TestCase):
    def test_control_address_rejects_remote_and_url_credentials(self):
        for raw in ('http://192.0.2.1:1234', 'http://localhost:1234', 'http://u@127.0.0.1:1234', 'http://127.0.0.1:1234/path'):
            with self.assertRaises(ValueError):
                address(raw, 'http')
        self.assertEqual(address('http://127.0.0.1:1234', 'http'), ('127.0.0.1', 1234))


class ControlTests(unittest.IsolatedAsyncioTestCase):
    async def test_http_control_requires_token_rejects_browser_and_stale_confirmation(self):
        with tempfile.TemporaryDirectory() as directory:
            owner = Owner(dict(directory=directory, endpoint='ws://127.0.0.1:17550'))
            owner.selection = dict(version='0.162.1')
            owner.state = 'running'
            owner.prepare = AsyncMock()
            server = ThreadingHTTPServer(('127.0.0.1', 0), handler(owner, asyncio.get_running_loop(), 'a' * 64))
            threading.Thread(target=server.serve_forever, daemon=True).start()
            url = 'http://127.0.0.1:' + str(server.server_port)

            def request(path, authorized=True, origin=False, body=None):
                headers = {'Authorization': 'Bearer ' + 'a' * 64} if authorized else {}
                if origin:
                    headers['Origin'] = 'https://example.invalid'
                r = urllib.request.Request(url + path, headers=headers,
                                           data=json.dumps(body).encode() if body is not None else None)
                try:
                    response = urllib.request.build_opener(urllib.request.ProxyHandler({})).open(r, timeout=5)
                except urllib.error.HTTPError as response:
                    return response.code, json.load(response)
                with response:
                    return response.code, json.load(response)
            try:
                self.assertEqual((await asyncio.to_thread(request, '/v1/status', False))[0], 403)
                self.assertEqual((await asyncio.to_thread(request, '/v1/status', True, True))[0], 403)
                code, status = await asyncio.to_thread(request, '/v1/status')
                self.assertEqual((code, status['version']), (200, '0.162.1'))
                code, _ = await asyncio.to_thread(request, '/v1/upgrade', body=dict(version='0.163.0', expectedVersion='0.161.0'))
                self.assertEqual(code, 409)
                owner.prepare.assert_not_awaited()
            finally:
                await asyncio.to_thread(server.shutdown)
                server.server_close()
                owner.log.close()


if __name__ == '__main__':
    unittest.main()
