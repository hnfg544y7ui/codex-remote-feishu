"""Opt-in native maintenance test: isolated HOME and a local Responses fixture."""
import asyncio
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import threading
import unittest
from unittest.mock import AsyncMock, patch

from owner import Owner, digest
from test_native_owner import free_port


REQUIRED = ('SHARED_CODEX_TEST_BINARY', 'SHARED_CODEX_TEST_OTHER_BINARY',
            'SHARED_CODEX_TEST_DIRECTORY', 'SHARED_CODEX_TEST_REMOTE')


@unittest.skipUnless(all(os.environ.get(key) for key in REQUIRED),
                     'requires two complete CLI bundles, Remote and isolated evidence directory')
class NativeImmediateTests(unittest.IsolatedAsyncioTestCase):
    async def test_active_turn_goal_preview_interrupt_full_switch_and_no_replay(self):
        # No auth/config is copied from the user. Both model URL and HOME are
        # explicitly local. The fixture holds one response in progress.
        root = Path(os.environ['SHARED_CODEX_TEST_DIRECTORY']).resolve()/'immediate'
        root.mkdir(parents=True, mode=0o700, exist_ok=False)
        home = root/'home'
        home.mkdir(mode=0o700)
        seen, close = threading.Event(), threading.Event()
        calls = []
        class Mock(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def do_POST(self):
                self.rfile.read(int(self.headers.get('Content-Length', '0')))
                calls.append(self.path)
                self.send_response(200)
                self.send_header('Content-Type', 'text/event-stream')
                self.end_headers()
                response = dict(type='response.created', response=dict(
                    id='resp_isolated', object='response', created_at=1,
                    status='in_progress', output=[], model='gpt-4.1'))
                self.wfile.write(('event: response.created\ndata: '+json.dumps(response)+'\n\n').encode())
                self.wfile.flush()
                seen.set()
                close.wait(120)
        server = ThreadingHTTPServer(('127.0.0.1', 0), Mock)
        server.daemon_threads = True
        threading.Thread(target=server.serve_forever, daemon=True).start()
        environment = {key: value for key, value in os.environ.items()
                       if not key.startswith(('CODEX_', 'OPENAI_', 'NOTIFY_SOCKET'))}
        environment['CODEX_HOME'] = str(home)
        config = dict(directory=str(root/'owner'), codexHome=str(home),
            initialBinary=os.environ['SHARED_CODEX_TEST_BINARY'],
            remoteBinary=os.environ['SHARED_CODEX_TEST_REMOTE'],
            endpoint='ws://127.0.0.1:'+str(free_port()),
            nativeEndpoint='ws://127.0.0.1:'+str(free_port()), allowImmediateUpgrade=True,
            launchArgs=['-c', 'model_provider="isolated"', '-c', 'model="gpt-4.1"', '-c',
                'model_providers.isolated={name="isolated",base_url="http://127.0.0.1:%d/v1",wire_api="responses",requires_openai_auth=false}'%server.server_port])
        evidence = {}
        with patch.dict(os.environ, environment, clear=True):
            owner = Owner(config)
            try:
                await owner.boot()
                other = os.environ['SHARED_CODEX_TEST_OTHER_BINARY']
                version = (await owner.command(other, '--version')).removeprefix('codex-cli ').strip()
                target = await owner.verify_selection(dict(binary=other, version=version, sha256=digest(other)))
                thread = await owner.rpc('thread/start', dict(cwd=str(root), model='gpt-4.1',
                    modelProvider='isolated', approvalPolicy='never', experimentalRawEvents=False))
                thread_id = thread['thread']['id']
                await owner.rpc('turn/start', dict(threadId=thread_id,
                    input=[dict(type='text', text='Reply only OK.')]))
                self.assertTrue(await asyncio.to_thread(seen.wait, 15), 'Native must use only the isolated local mock')
                status = await owner.rpc('thread/read', dict(threadId=thread_id, includeTurns=False))
                self.assertEqual(status['thread']['status']['type'], 'active')
                await owner.rpc('thread/goal/set', dict(threadId=thread_id,
                    objective='Isolated immediate-maintenance test', status='active'))
                original_pid, original_generation = owner.child.pid, owner.generation
                owner.prepare = AsyncMock(return_value=target)
                request = dict(version=version, expectedVersion=owner.selection['version'])
                with self.assertRaisesRegex(RuntimeError, 'active work'):
                    await owner.upgrade(request)
                self.assertEqual(owner.child.pid, original_pid)
                preview = await owner.immediate_upgrade.preview(request)
                plan = preview['immediatePlan']
                self.assertEqual((plan['activeThreads'], plan['activeGoals']), (1, 1))
                self.assertEqual(owner.child.pid, original_pid)
                self.assertEqual((await owner.rpc('thread/goal/get',dict(threadId=thread_id)))['goal']['status'], 'active')
                calls_before = len(calls)
                result = await owner.upgrade(dict(request, immediate=True, confirmationToken=plan['token']))
                self.assertEqual(result['version'], version)
                self.assertGreater(owner.generation, original_generation)
                self.assertNotEqual(owner.child.pid, original_pid)
                self.assertGreaterEqual(len(owner.selection['components']), 3)
                self.assertEqual(owner.selection['components'], target['components'])
                await owner.rpc('thread/resume', dict(threadId=thread_id))
                goal_after = await owner.rpc('thread/goal/get', dict(threadId=thread_id))
                self.assertEqual(goal_after['goal']['status'], 'paused')
                await asyncio.sleep(.5)
                self.assertEqual(len(calls), calls_before, 'Maintenance must not replay a prompt')
                evidence = dict(success=True, originalVersion=request['expectedVersion'],
                    targetVersion=version, activeThreads=plan['activeThreads'], activeGoals=plan['activeGoals'],
                    goalAfter=goal_after['goal']['status'], modelCallsBefore=calls_before,
                    modelCallsAfter=len(calls), components=len(owner.selection['components']),
                    generation=owner.generation)
            finally:
                close.set()
                await owner.stop(force=True)
                owner.log.close()
                await asyncio.to_thread(server.shutdown)
                server.server_close()
                (root/'result.json').write_text(json.dumps(evidence, indent=2))


if __name__ == '__main__':
    unittest.main()
