"""Exercise the actual HTTP admission layer, not just Owner.upgrade directly."""
import asyncio
from contextlib import asynccontextmanager
from http.server import ThreadingHTTPServer
import json
import tempfile
import threading
import unittest
from unittest.mock import AsyncMock
import urllib.error
import urllib.request

from host import handler
from owner import Owner


@asynccontextmanager
async def control_server():
    with tempfile.TemporaryDirectory() as directory:
        owner = Owner(dict(directory=directory, endpoint='ws://127.0.0.1:17550'))
        owner.selection = dict(binary='old', version='0.162.1', sha256='old-sha')
        owner.state = 'running'
        candidate = dict(binary='new', version='0.163.0', sha256='new-sha')
        owner.prepare = AsyncMock(return_value=candidate)
        owner.native_idle = AsyncMock()
        owner.stop = AsyncMock()
        owner.launch = AsyncMock()
        owner.verify_selection = AsyncMock(side_effect=lambda selected, **kwargs: dict(selected))
        owner.runtime_env.prepare = AsyncMock(return_value=dict(environment={}, args=[], snapshot=None, fingerprint=None))
        server = ThreadingHTTPServer(('127.0.0.1', 0), handler(owner, asyncio.get_running_loop(), 'a' * 64))
        thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=.01), daemon=True)
        thread.start()
        url = 'http://127.0.0.1:' + str(server.server_port)

        async def request(path, body=None):
            def send():
                req = urllib.request.Request(url + path, headers={'Authorization': 'Bearer ' + 'a' * 64},
                                             data=json.dumps(body).encode() if body is not None else None)
                try:
                    response = urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=5)
                except urllib.error.HTTPError as error:
                    return error.code, json.load(error)
                with response:
                    return response.code, json.load(response)
            return await asyncio.to_thread(send)

        try:
            yield owner, request
        finally:
            await asyncio.to_thread(server.shutdown)
            server.server_close()
            thread.join(timeout=2)
            owner.log.close()


class HostUpgradeProxyTests(unittest.IsolatedAsyncioTestCase):
    async def test_advertised_proxy_support_accepts_and_forwards_client_json(self):
        for mode in ('system', 'direct'):
            with self.subTest(mode=mode):
                async with control_server() as (owner, request):
                    code, status = await request('/v1/status')
                    self.assertEqual(code, 200)
                    self.assertTrue(status['upgradeProxySupported'])
                    # Same fields emitted by sharedcodex.Upgrade, including false rollback.
                    body = dict(version='0.163.0', expectedVersion='0.162.1', rollback=False, upgradeProxyMode=mode)
                    code, result = await request('/v1/upgrade', body)
                    self.assertEqual((code, result.get('version')), (200, '0.163.0'), result.get('problem'))
                    owner.prepare.assert_awaited_once_with('0.163.0', mode)

    async def test_legacy_upgrade_without_proxy_field_is_accepted(self):
        async with control_server() as (owner, request):
            code, result = await request('/v1/upgrade', dict(version='0.163.0', expectedVersion='0.162.1', rollback=False))
            self.assertEqual((code, result.get('version')), (200, '0.163.0'))
            owner.prepare.assert_awaited_once_with('0.163.0')

    async def test_invalid_proxy_values_fail_before_native_or_maintenance(self):
        for mode in ('invalid', True, {}, []):
            with self.subTest(mode=mode):
                async with control_server() as (owner, request):
                    code, result = await request('/v1/upgrade', dict(version='0.163.0', expectedVersion='0.162.1', upgradeProxyMode=mode))
                    self.assertEqual(code, 409)
                    self.assertEqual(result.get('problem'), 'Invalid upgrade proxy mode')
                    owner.prepare.assert_not_awaited()
                    owner.native_idle.assert_not_awaited()
                    owner.stop.assert_not_awaited()
                    self.assertFalse(owner.busy)

    async def test_unknown_fields_and_upgrade_fields_on_preview_remain_rejected(self):
        for path, extra in (('/v1/upgrade', dict(unrecognized=True)),
                            ('/v1/upgrade/immediate/preview', dict(upgradeProxyMode='direct'))):
            with self.subTest(path=path):
                async with control_server() as (owner, request):
                    code, _ = await request(path, dict(version='0.163.0', expectedVersion='0.162.1', **extra))
                    self.assertEqual(code, 409)
                    owner.prepare.assert_not_awaited()
                    owner.stop.assert_not_awaited()
                    self.assertFalse(owner.busy)


if __name__ == '__main__':
    unittest.main()
