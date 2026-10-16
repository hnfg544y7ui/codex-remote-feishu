import asyncio
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

from runtime_environment import RuntimeEnvironment, remote_idle


def idle_snapshot():
    return dict(activeRemoteTurns=[], pendingRemoteTurns=[], instances=[], surfaces=[])


class RuntimeTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.owner = type('Owner', (), {})()
        self.owner.root = self.root
        self.owner.config = dict(directory=str(self.root), codexHome=str(self.root/'home'),
                                 appEnvironment=dict(mode='linux', required=True))
        self.owner.child = None
        self.owner.selection = dict(binary=str(self.root/'codex'), version='1.0.0')
        self.owner.busy = self.owner.frozen = False
        self.owner.pending = {}
        self.owner.state = 'running'
        self.owner.upgrade_lock = asyncio.Lock()
        self.owner.native_idle = AsyncMock()
        self.owner.verify_selection = AsyncMock(side_effect=lambda value, **kw: value)
        self.owner.stop = AsyncMock()
        self.owner.launch = AsyncMock()
        self.owner.rpc = AsyncMock()
        self.runtime = RuntimeEnvironment(self.owner)
        node=self.root/'node'
        node.write_bytes(b'isolated-node-fixture')
        node.chmod(0o700)
        self.snapshot = dict(pid=123, executable=str(self.root/'app'), resources=str(self.root),
                             environment={'CODEX_APP_TOOLS_PIPE_PATH':'private-pipe',
                                          'CODEX_TASK_WORKSPACE_VERIFYING_IDENTITY':'private-token',
                                          'CODEX_MCP_NODE_PATH':str(node)})
        self.context = dict(snapshot=self.snapshot, fingerprint='old', environment={}, args=[])
        self.runtime.active = self.context

    async def asyncTearDown(self):
        await self.runtime.close()
        self.temp.cleanup()

    def test_remote_idle_accepts_attached_idle_clients_but_rejects_work(self):
        state=idle_snapshot()
        state['surfaces']=[dict(AttachedInstanceID='attached', SelectedThreadID='thread')]
        self.assertTrue(remote_idle(state))
        for key in ('ActiveQueueItemID','PendingHeadless','PendingRequests','QueuedQueueItemIDs','StagedImages','StagedFiles','PendingTextInput'):
            self.assertFalse(remote_idle(dict(state, surfaces=[{key:'pending'}])))
        self.assertFalse(remote_idle({}))
        self.assertFalse(remote_idle(None))
        self.assertFalse(remote_idle(dict(state, instances=[dict(ActiveTurnID='active')])))

    async def test_discovery_error_is_bounded_and_next_iteration_recovers(self):
        with patch('runtime_environment.discover', side_effect=[RuntimeError('private-token'), self.snapshot]):
            await self.runtime.poll()
            self.assertEqual(self.runtime.state, 'discovery_failed')
            self.assertNotIn('private-token', json.dumps(self.runtime.status()))
            self.runtime.active = dict(self.context, fingerprint=None)
            self.runtime.refresh = AsyncMock()
            await self.runtime.poll()
            self.runtime.refresh.assert_awaited_once_with(self.snapshot)

    async def test_discovery_timeouts_share_one_pending_probe_and_retry_after_completion(self):
        self.runtime.settings['discoveryTimeout'] = .01
        completed = asyncio.Event()
        async def probe(*args):
            await completed.wait()
            return self.snapshot
        with patch('runtime_environment.asyncio.to_thread', side_effect=probe) as discovery:
            for _ in range(2):
                with self.assertRaises(asyncio.TimeoutError):
                    await self.runtime.capture()
            self.assertEqual(discovery.call_count, 1)
            completed.set()
            self.assertEqual(await self.runtime.capture(), self.snapshot)
            self.assertIsNone(self.runtime.capture_task)
            self.assertEqual(await self.runtime.capture(), self.snapshot)
            self.assertEqual(discovery.call_count, 2)

    def test_invalid_poll_limits_or_nonlocal_status_are_rejected(self):
        for value in (0, -1, float('nan'), float('inf'), '20', True, 1000):
            with self.subTest(value=value), self.assertRaises(ValueError):
                RuntimeEnvironment(type('Owner', (), {'config':dict(appEnvironment=dict(discoveryTimeout=value))})())
        for url in ('https://127.0.0.1:9501/v1/status','http://example.com:9501/v1/status',
                    'http://127.0.0.1:9501/v1/status?x=1','http://u@127.0.0.1:9501/v1/status'):
            with self.subTest(url=url), self.assertRaises(ValueError):
                RuntimeEnvironment(type('Owner', (), {'config':dict(remoteStatusURL=url)})())

    async def test_active_native_goal_defers_without_stop_and_restores_admission(self):
        self.owner.native_idle.side_effect = RuntimeError('active Goal')
        with patch.object(self.runtime, 'read_remote', return_value=idle_snapshot()):
            await self.runtime.refresh(self.snapshot)
        self.owner.stop.assert_not_awaited()
        self.assertFalse(self.owner.busy or self.owner.frozen)
        self.assertEqual(self.runtime.state, 'waiting_for_idle')

    async def test_remote_work_defers_before_native_or_stop(self):
        state=dict(idle_snapshot(),activeRemoteTurns=[{}])
        with patch.object(self.runtime, 'read_remote', return_value=state):
            await self.runtime.refresh(self.snapshot)
        self.owner.native_idle.assert_not_awaited()
        self.owner.stop.assert_not_awaited()

    async def test_refresh_failure_restores_previous_environment_without_replaying_work(self):
        self.owner.launch.side_effect=[RuntimeError('new launch failed'),None]
        new=dict(self.context,fingerprint='new')
        with patch.object(self.runtime,'read_remote',return_value=idle_snapshot()), patch.object(self.runtime,'prepare',return_value=new):
            await self.runtime.refresh(self.snapshot)
        self.assertEqual(self.owner.launch.await_count,2)
        self.assertIs(self.owner.launch.await_args.kwargs['runtime'],self.context)
        self.assertFalse(self.owner.frozen)
        self.assertEqual(self.runtime.state,'refresh_failed')
        self.assertEqual(self.owner.selection['version'],'1.0.0')

    async def test_refresh_double_failure_closes_admission(self):
        self.owner.launch.side_effect=RuntimeError('cannot launch')
        with patch.object(self.runtime,'read_remote',return_value=idle_snapshot()), patch.object(self.runtime,'prepare',return_value=self.context):
            await self.runtime.refresh(self.snapshot)
        self.assertEqual(self.owner.state,'failed')
        self.assertTrue(self.owner.frozen)

    async def test_missing_current_node_rejected_before_launch(self):
        (self.root/'node').unlink()
        with self.assertRaisesRegex(RuntimeError,'Node'):
            await self.runtime.prepare(self.owner.selection,snapshot=self.snapshot)
        self.owner.stop.assert_not_awaited()

    async def test_health_requires_required_app_server_tools(self):
        self.owner.rpc.return_value=dict(data=[dict(name='codex_app',tools={},toolsError=None)])
        with self.assertRaises(RuntimeError):
            await self.runtime.health(self.context)
        self.owner.rpc.return_value=dict(data=[dict(name='codex_app',tools={'read':{}},toolsError=None,runtimeStatus=None)])
        await self.runtime.health(self.context)

    async def test_app_closed_does_not_stop_native_or_claim_tools_online(self):
        with patch('runtime_environment.discover',return_value=None):
            await self.runtime.poll()
        self.owner.stop.assert_not_awaited()
        self.assertFalse(self.runtime.status()['appToolsOnline'])
        self.assertEqual(self.runtime.state,'app_unavailable')

    async def test_snapshot_secrets_are_never_persisted(self):
        self.runtime.publish()
        self.assertNotIn('private-token',(self.root/'status.json').read_text())
        self.assertNotIn('private-pipe',(self.root/'status.json').read_text())

    async def test_upgrade_winning_refresh_race_cannot_reuse_old_cli_environment(self):
        entered=asyncio.Event()
        release=asyncio.Event()
        async def read_remote():
            entered.set()
            await release.wait()
            return idle_snapshot()
        with patch.object(self.runtime,'read_remote',side_effect=lambda:None):
            # Control the thread-probe await itself without any real HTTP work.
            async def in_thread(function,*args):
                if function==self.runtime.read_remote:return await read_remote()
                return function(*args)
            with patch('runtime_environment.asyncio.to_thread',side_effect=in_thread):
                refresh=asyncio.create_task(self.runtime.refresh(self.snapshot))
                await entered.wait()
                async with self.owner.upgrade_lock:
                    self.owner.selection=dict(binary=str(self.root/'new/codex'),version='2.0.0')
                    release.set()
                    await asyncio.sleep(0)
                await refresh
        self.owner.stop.assert_not_awaited()
        self.owner.launch.assert_not_awaited()
        self.assertEqual(self.runtime.state,'waiting_for_idle')


if __name__=='__main__':
    unittest.main()
