"""Single-use impact confirmation for explicitly disruptive owner upgrades."""
import hashlib
import json
import re
import secrets
import time
from datetime import datetime, timezone


class NativeRPCError(RuntimeError):
    def __init__(self, method, code, message):
        self.method, self.code, self.message = method, code, message
        # Native message may contain private paths or task text. Keep HTTP errors
        # generic; typed fields are used only for precise protocol handling.
        super().__init__('Native backend rejected maintenance probe: ' + method)


async def get_goal(owner, thread_id):
    try:
        result = await owner.rpc('thread/goal/get', dict(threadId=thread_id))
    except NativeRPCError as error:
        if (error.method == 'thread/goal/get' and error.code == -32600 and
                error.message == 'ephemeral thread does not support goals: ' + thread_id):
            return None
        raise
    if not isinstance(result, dict) or 'goal' not in result:
        raise RuntimeError('Cannot establish native Goal state')
    goal = result['goal']
    if goal is not None and (not isinstance(goal, dict) or
            goal.get('status') not in ('active', 'paused', 'complete', 'completed', 'blocked', 'budgetLimited', 'budget_limited')):
        raise RuntimeError('Cannot establish native Goal state')
    if goal and goal.get('threadId', thread_id) != thread_id:
        raise RuntimeError('Native Goal belongs to a different thread')
    return goal


def safe_name(thread, thread_id):
    name = thread.get('name') or thread.get('title') or thread_id
    if not isinstance(name, str):
        name = thread_id
    return ''.join(c for c in name if c.isprintable())[:120]


def goal_signature(goal):
    # Include objective and creation/update identity without exposing them in
    # control status. Pausing a changed Goal requires a new confirmation.
    if (not isinstance(goal.get('objective'), str) or not goal['objective'] or
            type(goal.get('createdAt')) is not int or goal['createdAt'] <= 0):
        raise RuntimeError('Cannot establish stable native Goal identity')
    identity = {key: goal.get(key) for key in ('objective', 'createdAt', 'tokenBudget')}
    return hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()


class ImmediateUpgrade:
    def __init__(self, owner):
        self.owner = owner
        self.plans = {}

    @property
    def supported(self):
        return self.owner.config.get('allowImmediateUpgrade') is True

    def validate_request(self, request):
        if 'immediate' in request and type(request['immediate']) is not bool:
            raise RuntimeError('Invalid immediate upgrade request')
        if 'confirmationToken' in request and not isinstance(request['confirmationToken'], str):
            raise RuntimeError('Invalid immediate upgrade confirmation')
        immediate = request.get('immediate') is True
        if immediate and request.get('rollback'):
            raise RuntimeError('Immediate rollback is not supported')
        if not immediate and 'confirmationToken' in request:
            raise RuntimeError('Immediate confirmation requires immediate upgrade')
        return immediate

    async def snapshot(self):
        owner = self.owner
        generation, version = owner.generation, owner.selection['version']
        loaded = (await owner.rpc('thread/loaded/list', {})).get('data')
        if (not isinstance(loaded, list) or len(loaded) > 256 or
                any(not isinstance(t, str) or not t for t in loaded) or len(set(loaded)) != len(loaded)):
            raise RuntimeError('Cannot establish native thread state')
        turns, goals, names = set(), {}, {}
        for thread_id in loaded:
            thread = (await owner.rpc('thread/read', dict(threadId=thread_id, includeTurns=False))).get('thread')
            if not isinstance(thread, dict):
                raise RuntimeError('Cannot establish native thread state')
            status = thread.get('status', {}).get('type')
            if status not in ('idle', 'systemError', 'active'):
                raise RuntimeError('Cannot establish native thread state')
            if status == 'active':
                page = await owner.rpc('thread/turns/list', dict(threadId=thread_id, limit=1,
                                       sortDirection='desc', itemsView='notLoaded'))
                data = page.get('data')
                if not isinstance(data, list) or len(data) != 1:
                    raise RuntimeError('Cannot establish active native turn')
                turn = data[0]
                if (not isinstance(turn, dict) or not isinstance(turn.get('id'), str) or not turn['id'] or
                        turn.get('status') != 'inProgress'):
                    raise RuntimeError('Cannot establish active native turn')
                turns.add((thread_id, turn['id']))
            goal = await get_goal(owner, thread_id)
            if goal and goal['status'] == 'active':
                goals[thread_id] = goal_signature(goal)
            if status == 'active' or thread_id in goals:
                names[thread_id] = safe_name(thread, thread_id)
        if generation != owner.generation or version != owner.selection['version']:
            raise RuntimeError('Backend changed while inspecting native work; preview again')
        pending = {(key, method) for key, method in owner.pending.items() if method not in owner.read_only_methods}
        return dict(turns=turns, goals=goals, names=names, pending=pending,
                    pending_count=len(owner.pending), uncertain_count=len(owner.uncertain_requests),
                    uncertain={(key, owner.pending.get(key)) for key in owner.uncertain_requests
                               if owner.pending.get(key) not in owner.read_only_methods})

    async def preview(self, request):
        owner = self.owner
        if not self.supported:
            raise RuntimeError('Immediate upgrade is disabled by this owner')
        if owner.busy or owner.upgrade_lock.locked() or owner.state != 'running':
            raise RuntimeError('Shared backend is not ready for preview')
        target, expected = request.get('version'), request.get('expectedVersion')
        if not owner.valid_version(target) or expected != owner.selection['version'] or target == expected:
            raise RuntimeError('Version changed; check for updates again')
        generation = owner.generation
        impact = await self.snapshot()
        if owner.busy or owner.upgrade_lock.locked() or generation != owner.generation:
            raise RuntimeError('Backend changed while inspecting native work; preview again')
        now = time.monotonic()
        self.plans = {k: v for k, v in self.plans.items() if v['deadline'] > now}
        if len(self.plans) >= 64:
            raise RuntimeError('Too many outstanding immediate upgrade previews')
        token = secrets.token_hex(32)
        self.plans[token] = dict(generation=generation, version=expected, target=target,
                                 deadline=now + 120, impact=impact)
        plan = dict(token=token, expiresAt=datetime.fromtimestamp(time.time()+120, timezone.utc).isoformat().replace('+00:00', 'Z'),
                    version=expected, targetVersion=target, generation=generation,
                    activeThreads=len(impact['turns']), activeGoals=len(impact['goals']),
                    uncertainRequests=impact['uncertain_count'], pendingRequests=impact['pending_count'],
                    threadNames=[impact['names'][k] for k in sorted(impact['names'])[:10]])
        return dict(owner.status(), immediatePlan=plan)

    def consume(self, request):
        if not self.supported:
            raise RuntimeError('Immediate upgrade is disabled by this owner')
        token = request.get('confirmationToken')
        if not isinstance(token, str) or not re.fullmatch(r'[0-9a-f]{64}', token):
            raise RuntimeError('Preview and explicitly confirm immediate upgrade first')
        plan = self.plans.pop(token, None)
        owner = self.owner
        if (not plan or plan['deadline'] <= time.monotonic() or plan['generation'] != owner.generation or
                plan['version'] != owner.selection['version'] or plan['target'] != request.get('version') or
                request.get('expectedVersion') != plan['version']):
            raise RuntimeError('Immediate upgrade confirmation expired or changed; preview again')
        return plan['impact']

    @staticmethod
    def within(current, approved):
        if (not current['turns'].issubset(approved['turns']) or
                not current['pending'].issubset(approved['pending']) or
                not current['uncertain'].issubset(approved['uncertain']) or
                any(approved['goals'].get(key) != value for key, value in current['goals'].items())):
            raise RuntimeError('New or changed native work appeared; preview immediate upgrade again')

    async def interrupt(self, approved):
        owner = self.owner
        current = await self.snapshot()
        self.within(current, approved)
        # Pause every continuing Goal before interrupting any turn. Never resume
        # these goals during either successful launch or restoration.
        touched = False
        try:
            for thread_id in current['goals']:
                goal = await get_goal(owner, thread_id)
                if not goal or goal.get('status') != 'active':
                    continue
                if goal_signature(goal) != approved['goals'][thread_id]:
                    raise RuntimeError('New or changed native Goal appeared; preview immediate upgrade again')
                touched = True
                await owner.rpc('thread/goal/set', dict(threadId=thread_id, status='paused'))
                goal = await get_goal(owner, thread_id)
                if not goal or goal.get('status') != 'paused':
                    raise RuntimeError('Immediate upgrade could not confirm Goal pause')
            current = await self.snapshot()
            self.within(current, approved)
            if current['goals']:
                raise RuntimeError('An active Goal remained after pause')
            for thread_id, turn_id in sorted(current['turns']):
                touched = True
                await owner.rpc('turn/interrupt', dict(threadId=thread_id, turnId=turn_id))
            # Interrupt can release a delayed native continuation. Refuse to
            # stop if it starts a different turn or a new Goal in that interval.
            current = await self.snapshot()
            self.within(current, approved)
            if current['goals']:
                raise RuntimeError('An active Goal appeared after interrupt')
        except Exception as error:
            if touched:
                raise RuntimeError(str(error) + '; some work may already be paused or interrupted') from error
            raise

    def clear_after_exit(self):
        child = self.owner.child
        if child is None or child.returncode is None:
            raise RuntimeError('Cannot confirm previous native process exited; unconfirmed work remains fenced')
        # The process exit is evidence that unknown work can no longer continue.
        # This does not invent an RPC response or replay any user prompt.
        self.owner.pending.clear()
        self.owner.uncertain_requests.clear()
