"""Real control/gateway protocol and platform-specific stop boundary tests."""
import asyncio
from http.server import ThreadingHTTPServer
import json
import os
import subprocess
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch
import urllib.error
import urllib.request

import websockets
from host import handler
from immediate_upgrade import NativeRPCError
from owner import Owner


class ImmediateProtocolTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.owner = Owner(dict(directory=self.temp.name, endpoint='ws://127.0.0.1:17550', allowImmediateUpgrade=True))
        self.addCleanup(self.owner.log.close)
        self.owner.selection = dict(version='0.162.1')
        self.owner.state = 'running'

    async def test_preview_http_requires_bearer_rejects_origin_and_unknown_fields(self):
        self.owner.rpc = AsyncMock(return_value=dict(data=[]))
        server = ThreadingHTTPServer(('127.0.0.1',0), handler(self.owner,asyncio.get_running_loop(),'a'*64))
        threading.Thread(target=server.serve_forever,daemon=True).start()
        def post(body, token=True, origin=False, path='/v1/upgrade/immediate/preview'):
            headers = {'Authorization':'Bearer '+'a'*64} if token else {}
            if origin:
                headers['Origin'] = 'https://example.invalid'
            request = urllib.request.Request('http://127.0.0.1:%d%s'%(server.server_port,path),
                data=json.dumps(body).encode() if body is not None else b'',headers=headers)
            try:
                response = urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                return response.code,json.load(response)
        body = dict(version='0.163.0',expectedVersion='0.162.1')
        try:
            self.assertEqual((await asyncio.to_thread(post,None,False))[0],403)
            self.assertEqual((await asyncio.to_thread(post,None,True,True))[0],403)
            self.owner.rpc.assert_not_awaited()
            self.assertEqual((await asyncio.to_thread(post,dict(body,rollback=True)))[0],409)
            code,result = await asyncio.to_thread(post,body)
            self.assertEqual(code,200)
            self.assertTrue(result['immediateUpgradeSupported'])
            self.assertEqual(result['immediatePlan']['targetVersion'],'0.163.0')
            for extra in (dict(immediate='true'),dict(immediate=True,confirmationToken=3),dict(immediate=True,rollback=True)):
                self.assertEqual((await asyncio.to_thread(post,dict(body,**extra),path='/v1/upgrade'))[0],409)
        finally:
            await asyncio.to_thread(server.shutdown)
            server.server_close()

    async def test_real_rpc_error_preserves_typed_fields_without_public_message(self):
        private = 'ephemeral thread does not support goals: test-one'
        async def native(ws):
            async for frame in ws:
                request = json.loads(frame)
                if 'id' not in request:
                    continue
                result = dict(id=request['id'])
                if request['method'] == 'initialize':
                    result['result'] = {}
                else:
                    result['error'] = dict(code=-32600,message=private)
                await ws.send(json.dumps(result))
        async with websockets.serve(native,'127.0.0.1',0) as server:
            self.owner.config['nativeEndpoint'] = 'ws://127.0.0.1:%d'%server.sockets[0].getsockname()[1]
            with self.assertRaises(NativeRPCError) as raised:
                await self.owner.rpc('thread/goal/get',dict(threadId='test-one'))
            error = raised.exception
            self.assertEqual((error.method,error.code,error.message),('thread/goal/get',-32600,private))
            self.assertNotIn(private,str(error))

    async def test_windows_immediate_stop_uses_taskkill_tree_force_and_waits(self):
        child = SimpleNamespace(returncode=None,pid=777,terminate=lambda: self.fail('parent-only terminate'))
        async def wait():
            child.returncode = 0
        child.wait = AsyncMock(side_effect=wait)
        self.owner.child = child
        self.owner.command = AsyncMock(return_value='')
        with patch('owner.os.name','nt'):
            await self.owner.stop(force=True)
        self.owner.command.assert_awaited_once_with('taskkill.exe','/PID','777','/T','/F',timeout=10)
        child.wait.assert_awaited_once()
        self.assertEqual(child.returncode,0)

    async def test_old_gateway_cannot_register_requests_in_new_generation(self):
        received = []
        async def native(ws):
            async for frame in ws:
                request = json.loads(frame)
                received.append(request)
                await ws.send(json.dumps(dict(id=request['id'],result={})))
        async with websockets.serve(native,'127.0.0.1',0) as backend:
            self.owner.config['nativeEndpoint'] = 'ws://127.0.0.1:%d'%backend.sockets[0].getsockname()[1]
            async with websockets.serve(self.owner.proxy,'127.0.0.1',0) as gateway:
                async with websockets.connect('ws://127.0.0.1:%d'%gateway.sockets[0].getsockname()[1],proxy=None) as client:
                    await client.send(json.dumps(dict(id=1,method='thread/read')))
                    await client.recv()
                    self.owner.generation += 1
                    await client.send(json.dumps(dict(id=2,method='turn/start')))
                    with self.assertRaises(websockets.exceptions.ConnectionClosed):
                        await client.recv()
                    self.assertEqual([r['method'] for r in received],['thread/read'])
                    self.assertFalse(self.owner.pending or self.owner.uncertain_requests)

    @unittest.skipUnless(os.name == 'posix','requires real independent POSIX process group')
    async def test_force_stop_kills_descendant_group_after_parent_exits(self):
        # The descendant ignores TERM. Parent shutdown must not leave it running
        # after the shared process has been replaced.
        inner = 'import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); print("ready",flush=True); time.sleep(90)'
        code = ('import subprocess,sys,time; '
                'p=subprocess.Popen([sys.executable,"-c",'+repr(inner)+'],stdout=subprocess.PIPE,text=True); '
                'p.stdout.readline(); print(p.pid,flush=True); time.sleep(90)')
        process = await asyncio.create_subprocess_exec(sys.executable,'-c',code,
            stdout=asyncio.subprocess.PIPE,start_new_session=True)
        self.owner.child = process
        try:
            descendant = int(await asyncio.wait_for(process.stdout.readline(),5))
            await self.owner.stop(force=True)
            self.assertIsNotNone(process.returncode)
            for _ in range(50):
                try:
                    with open('/proc/%d/stat'%descendant) as stream:
                        stat = stream.read().split(') ',1)[1].split()[0]
                except FileNotFoundError:
                    break
                if stat == 'Z':
                    break
                await asyncio.sleep(.02)
            else:
                self.fail('native descendant still running after forced group stop')
        finally:
            try:
                os.killpg(process.pid,9)
            except ProcessLookupError:
                pass
            await process.wait()


if __name__ == '__main__':
    unittest.main()
