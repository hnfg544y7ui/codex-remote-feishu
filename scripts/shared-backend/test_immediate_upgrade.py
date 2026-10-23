"""Disruptive maintenance requires fresh explicit confirmation of all work."""
import json
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock

from immediate_upgrade import NativeRPCError, get_goal
from owner import Owner


class ImmediateTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.owner = Owner(dict(directory=self.temp.name, endpoint='ws://127.0.0.1:17550', allowImmediateUpgrade=True))
        self.addCleanup(self.owner.log.close)
        self.old = dict(binary='old', version='0.162.1', sha256='old')
        self.new = dict(binary='new', version='0.163.0', sha256='new')
        self.owner.selection = dict(self.old)
        self.owner.state, self.owner.generation = 'running', 4
        self.owner.child = SimpleNamespace(returncode=None, pid=12345)
        self.owner.prepare = AsyncMock(return_value=self.new)
        self.owner.launch = AsyncMock()
        self.owner.native_idle = AsyncMock()
        self.owner.verify_selection = AsyncMock(side_effect=lambda selected, **kw: dict(selected))
        self.owner.runtime_env.prepare = AsyncMock(return_value=dict(environment={}, args=[], snapshot=None, fingerprint=None))
        self.calls, self.threads = [], {}
        self.owner.rpc = AsyncMock(side_effect=self.rpc)
        async def stop(**kwargs):
            self.calls.append(('stop', kwargs))
            self.assertTrue(self.owner.frozen)
            self.owner.child.returncode = 0
        self.owner.stop = AsyncMock(side_effect=stop)

    def request(self, **extra):
        return dict(version='0.163.0', expectedVersion='0.162.1', **extra)

    def work(self, key='one', active=True, goal=True):
        self.threads[key] = dict(name='Work '+key, active=active, turn='turn-'+key,
                                goal=dict(status='active', objective='test', createdAt=1) if goal else None)

    async def rpc(self, method, params):
        self.calls.append((method, params))
        if method == 'thread/loaded/list':
            return dict(data=list(self.threads))
        thread = self.threads[params['threadId']]
        if method == 'thread/read':
            self.assertFalse(params['includeTurns'])
            return dict(thread=dict(name=thread['name'], status=dict(type='active' if thread['active'] else 'idle')))
        if method == 'thread/turns/list':
            self.assertEqual(params, dict(threadId=params['threadId'], limit=1, sortDirection='desc', itemsView='notLoaded'))
            return dict(data=[dict(id=thread['turn'], status='inProgress')])
        if method == 'thread/goal/get':
            return dict(goal=thread['goal'])
        if method == 'thread/goal/set':
            self.assertTrue(self.owner.frozen)
            thread['goal']['status'] = params['status']
            return dict(goal=thread['goal'])
        if method == 'turn/interrupt':
            self.assertTrue(self.owner.frozen)
            self.assertEqual(params['turnId'], thread['turn'])
            thread['active'] = False
            return {}
        raise AssertionError(method)

    async def confirmation(self):
        response = await self.owner.immediate_upgrade.preview(self.request())
        return self.request(immediate=True, confirmationToken=response['immediatePlan']['token'])

    async def test_preview_read_only_full_counts_safe_truncated_names(self):
        for index in range(12):
            self.work(str(index))
        self.owner.pending[(1,'r')] = 'turn/start'
        self.owner.uncertain_requests.add((1,'r'))
        response = await self.owner.immediate_upgrade.preview(self.request())
        plan = response['immediatePlan']
        self.assertEqual((plan['activeThreads'], plan['activeGoals'], plan['pendingRequests'], plan['uncertainRequests']), (12,12,1,1))
        self.assertEqual(len(plan['threadNames']), 10)
        self.assertRegex(plan['token'], r'^[a-f0-9]{64}$')
        self.assertTrue(plan['expiresAt'].endswith('Z'))
        self.owner.stop.assert_not_awaited()
        self.owner.prepare.assert_not_awaited()
        self.assertFalse(self.owner.frozen)
        self.assertFalse(any(method == 'thread/goal/set' for method, _ in self.calls))
        self.assertNotIn('immediatePlan', self.owner.status())

    async def test_configuration_strict_opt_in(self):
        for value in (None, False, 'true', 1):
            self.owner.config['allowImmediateUpgrade'] = value
            self.assertFalse(self.owner.status()['immediateUpgradeSupported'])
            with self.assertRaisesRegex(RuntimeError, 'disabled'):
                await self.owner.immediate_upgrade.preview(self.request())
            with self.assertRaises(RuntimeError):
                await self.owner.upgrade(self.request(immediate=True, confirmationToken='a'*64))
        self.owner.prepare.assert_not_awaited()

    async def test_confirmation_required_types_and_rollback_rejected(self):
        for request in (self.request(immediate=True), self.request(immediate='true'), self.request(immediate=True,rollback=True),
                        self.request(immediate=True,confirmationToken=123), self.request(confirmationToken='a'*64)):
            with self.assertRaises(RuntimeError):
                await self.owner.upgrade(request)
        self.owner.prepare.assert_not_awaited()
        self.owner.stop.assert_not_awaited()

    async def test_expired_generation_current_target_tokens_rejected(self):
        for alteration in ('deadline','generation','version','target'):
            request = await self.confirmation()
            plan = self.owner.immediate_upgrade.plans[request['confirmationToken']]
            plan[alteration] = 0 if alteration in ('deadline','generation') else '0.1.0'
            with self.assertRaises(RuntimeError):
                await self.owner.upgrade(request)
        self.owner.prepare.assert_not_awaited()
        self.owner.stop.assert_not_awaited()

    async def test_token_consumed_on_prepare_failure_no_pause(self):
        self.work()
        request = await self.confirmation()
        self.owner.prepare.side_effect = RuntimeError('prepare failed')
        with self.assertRaisesRegex(RuntimeError, 'prepare failed'):
            await self.owner.upgrade(request)
        with self.assertRaisesRegex(RuntimeError, 'expired or changed'):
            await self.owner.upgrade(request)
        self.assertEqual(self.threads['one']['goal']['status'], 'active')
        self.owner.stop.assert_not_awaited()
        self.assertFalse(self.owner.frozen)

    async def test_restart_loses_confirmation_and_goal_progress_is_not_new_work(self):
        self.work()
        request = await self.confirmation()
        helper = self.owner.immediate_upgrade
        self.owner.immediate_upgrade = type(helper)(self.owner)
        with self.assertRaisesRegex(RuntimeError, 'expired or changed'):
            await self.owner.upgrade(request)
        request = await self.confirmation()
        self.threads['one']['goal'].update(tokensUsed=500,timeUsedSeconds=7,updatedAt=99)
        await self.owner.upgrade(request)
        self.assertEqual(self.threads['one']['goal']['status'],'paused')

    async def test_unknown_thread_or_goal_identity_fails_closed(self):
        self.work()
        self.threads['one']['goal'].pop('createdAt')
        with self.assertRaisesRegex(RuntimeError,'Goal identity'):
            await self.confirmation()
        self.owner.rpc = AsyncMock(return_value=dict(data=['one']*257))
        with self.assertRaisesRegex(RuntimeError,'thread state'):
            await self.confirmation()
        self.owner.stop.assert_not_awaited()

    async def test_pausing_goal_cannot_authorize_a_different_turn(self):
        self.work()
        request = await self.confirmation()
        async def raced(method,params):
            result = await self.rpc(method,params)
            if method == 'thread/goal/set':
                self.threads['one']['turn'] = 'new-turn-after-pause'
            return result
        self.owner.rpc = AsyncMock(side_effect=raced)
        with self.assertRaisesRegex(RuntimeError,'New or changed.*already'):
            await self.owner.upgrade(request)
        self.owner.stop.assert_not_awaited()
        self.assertEqual(self.threads['one']['goal']['status'],'paused')
        self.assertFalse(any(m == 'turn/interrupt' for m,_ in self.calls))

    async def test_new_turn_goal_unknown_request_rejects_expansion(self):
        self.work()
        for alteration in ('turn','goal','pending','uncertain'):
            self.owner.pending.clear()
            self.owner.uncertain_requests.clear()
            self.threads['one']['turn'] = 'turn-one'
            self.threads['one']['goal']['objective'] = 'test'
            request = await self.confirmation()
            if alteration == 'turn':
                self.threads['one']['turn'] = 'new-turn'
            elif alteration == 'goal':
                self.threads['one']['goal']['objective'] = 'new goal'
            else:
                self.owner.pending[(2,'r')] = 'turn/start'
                if alteration == 'uncertain':
                    self.owner.uncertain_requests.add((2,'r'))
            with self.assertRaisesRegex(RuntimeError, 'New or changed'):
                await self.owner.upgrade(request)
            self.assertFalse(self.owner.frozen)
        self.owner.stop.assert_not_awaited()
        self.assertEqual(self.threads['one']['goal']['status'], 'active')

    async def test_completed_work_may_disappear_after_preview(self):
        self.work()
        request = await self.confirmation()
        self.threads.clear()
        await self.owner.upgrade(request)
        self.assertEqual(self.owner.selection['version'], self.new['version'])

    async def test_pause_all_goals_before_interrupt_confirmed_turns(self):
        self.work('one')
        self.work('two')
        request = await self.confirmation()
        self.calls.clear()
        await self.owner.upgrade(request)
        mutations = [(m,p) for m,p in self.calls if m in ('thread/goal/set','turn/interrupt','stop')]
        self.assertEqual([m for m,_ in mutations], ['thread/goal/set','thread/goal/set','turn/interrupt','turn/interrupt','stop'])
        self.assertTrue(all(t['goal']['status'] == 'paused' for t in self.threads.values()))
        self.assertEqual(mutations[-1][1], dict(force=True))
        with self.assertRaisesRegex(RuntimeError, 'expired or changed'):
            await self.owner.upgrade(request)

    async def test_unknown_work_clears_only_after_confirmed_native_exit(self):
        self.owner.pending[(1,'r')] = 'turn/start'
        self.owner.uncertain_requests.add((1,'r'))
        request = await self.confirmation()
        async def stop(**kwargs):
            self.assertTrue(self.owner.pending)
            self.assertTrue(self.owner.uncertain_requests)
            with self.assertRaisesRegex(RuntimeError, 'Cannot confirm'):
                self.owner.immediate_upgrade.clear_after_exit()
            self.owner.child.returncode = 0
        self.owner.stop.side_effect = stop
        async def launch(*args, **kwargs):
            self.assertFalse(self.owner.pending or self.owner.uncertain_requests)
        self.owner.launch.side_effect = launch
        await self.owner.upgrade(request)

    async def test_failed_launch_restores_bundle_without_resuming_goals(self):
        self.work()
        request = await self.confirmation()
        self.owner.launch.side_effect = [RuntimeError('candidate failed'),None]
        with self.assertRaisesRegex(RuntimeError, 'candidate failed'):
            await self.owner.upgrade(request)
        self.assertEqual(self.owner.selection, self.old)
        self.assertEqual(self.owner.state, 'running')
        self.assertFalse(self.owner.frozen)
        self.assertEqual(self.threads['one']['goal']['status'], 'paused')
        self.assertEqual(self.owner.launch.await_args_list[-1].args[0], self.old)
        self.assertFalse(any(m == 'thread/goal/set' and p.get('status') == 'active' for m,p in self.calls))

    async def test_pause_unconfirmed_cannot_stop_or_interrupt(self):
        self.work()
        request = await self.confirmation()
        async def unconfirmed(method, params):
            if method == 'thread/goal/set':
                return {}
            return await self.rpc(method,params)
        self.owner.rpc = AsyncMock(side_effect=unconfirmed)
        with self.assertRaisesRegex(RuntimeError, 'confirm Goal pause'):
            await self.owner.upgrade(request)
        self.owner.stop.assert_not_awaited()
        self.assertFalse(any(m == 'turn/interrupt' for m,_ in self.calls))

    async def test_normal_upgrade_keeps_unknown_work_fence(self):
        self.owner.pending[(1,'r')] = 'turn/start'
        self.owner.uncertain_requests.add((1,'r'))
        with self.assertRaisesRegex(RuntimeError, 'unconfirmed work'):
            await self.owner.upgrade(self.request())
        self.owner.stop.assert_not_awaited()
        self.assertTrue(self.owner.pending)

    async def test_ephemeral_goal_exact_exception_activity_still_checked(self):
        for error, accepted in (
            (NativeRPCError('thread/goal/get',-32600,'ephemeral thread does not support goals: one'),True),
            (NativeRPCError('thread/goal/get',-32600,'ephemeral thread does not support goals: two'),False),
            (NativeRPCError('thread/goal/get',-32601,'ephemeral thread does not support goals: one'),False),
            (NativeRPCError('thread/read',-32600,'ephemeral thread does not support goals: one'),False),
            (RuntimeError('ephemeral thread does not support goals: one'),False)):
            self.owner.rpc = AsyncMock(side_effect=error)
            if accepted:
                self.assertIsNone(await get_goal(self.owner,'one'))
            else:
                with self.assertRaises(RuntimeError):
                    await get_goal(self.owner,'one')
        self.owner.native_idle = Owner.native_idle.__get__(self.owner)
        self.owner.rpc = AsyncMock(side_effect=[dict(data=['one']),dict(thread=dict(status=dict(type='active')))])
        with self.assertRaisesRegex(RuntimeError, 'active work'):
            await self.owner.native_idle()
        self.owner.rpc = AsyncMock(side_effect=[dict(data=['one']),dict(thread=dict(status=dict(type='idle'))),
            NativeRPCError('thread/goal/get',-32600,'ephemeral thread does not support goals: one')])
        await self.owner.native_idle()


if __name__ == '__main__':
    unittest.main()
