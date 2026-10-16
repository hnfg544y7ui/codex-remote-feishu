"""Bounded App discovery and coordinated environment refresh for one owner."""
import asyncio
import ipaddress
import json
import math
import os
from pathlib import Path
import time
import urllib.request
from urllib.parse import urlsplit

from app_environment import compose, discover, fingerprint

AUTO = object()


def remote_idle(status):
    if not isinstance(status, dict):
        return False
    for key in ('activeRemoteTurns', 'pendingRemoteTurns', 'instances', 'surfaces'):
        if not isinstance(status.get(key), list):
            return False
    if status['activeRemoteTurns'] or status['pendingRemoteTurns']:
        return False
    if any(not isinstance(i, dict) or i.get('ActiveTurnID') for i in status['instances']):
        return False
    return not any(not isinstance(s, dict) or any(s.get(k) for k in (
        'ActiveQueueItemID', 'QueuedQueueItemIDs', 'PendingHeadless',
        'PendingRequests', 'ActiveRequestCapture', 'Abandoning',
        'StagedImages', 'StagedFiles', 'PendingTextInput')) for s in status['surfaces'])


class RuntimeEnvironment:
    def __init__(self, owner):
        self.owner = owner
        self.config = owner.config
        self.settings = self.config.get('appEnvironment')
        if self.settings is not None and not isinstance(self.settings, dict):
            raise ValueError('App environment settings must be an object')
        if self.settings:
            for key, default, minimum, maximum in (('discoveryTimeout', 20, .1, 120), ('pollInterval', 15, 1, 300)):
                value = self.settings.get(key, default)
                if type(value) not in (int, float) or not math.isfinite(value) or not minimum <= value <= maximum:
                    raise ValueError('App environment polling limits are invalid')
        self.remote_url = self.config.get('remoteStatusURL', 'http://127.0.0.1:9501/v1/status')
        endpoint = urlsplit(self.remote_url)
        if (endpoint.scheme != 'http' or not ipaddress.ip_address(endpoint.hostname).is_loopback
                or not endpoint.port or endpoint.username or endpoint.password or endpoint.query
                or endpoint.fragment or endpoint.path != '/v1/status'):
            raise ValueError('Remote status must use a literal local loopback endpoint')
        self.base = dict(os.environ)
        self.active = None
        self.capture_task = None
        self.last_checked = None
        self.latest_fingerprint = None
        self.state = 'starting' if self.settings else 'disabled'

    async def capture(self):
        if not self.settings:
            return None
        # A timeout cannot kill a Python thread. Reuse its single pending
        # discovery instead of accumulating a thread on every retry.
        if self.capture_task is None:
            self.capture_task = asyncio.create_task(asyncio.to_thread(discover, self.settings))
        task = self.capture_task
        try:
            return await asyncio.wait_for(asyncio.shield(task), self.settings.get('discoveryTimeout', 20))
        finally:
            if task.done():
                self.capture_task = None

    async def prepare(self, selected, snapshot=AUTO):
        if snapshot is AUTO:
            snapshot = await self.capture()
        if self.settings and self.settings.get('required', True) and not snapshot:
            raise RuntimeError('Current App runtime is unavailable; existing backend is unchanged')
        environment = compose(self.base, self.config, snapshot, selected['binary'])
        args = []
        if self.settings:
            if snapshot and not environment.get('CODEX_MCP_NODE_PATH'):
                raise RuntimeError('Current App Node runtime is unavailable')
            args = ['-c', 'features.code_mode_host=true', '-c', 'features.prefer_mxc=true',
                    '-c', 'plugins.codex-app-tools@openai-bundled.mcp_servers.codex_app.enabled=' + str(bool(snapshot)).lower()]
        return dict(snapshot=snapshot, fingerprint=fingerprint(snapshot), environment=environment, args=args)

    async def health(self, runtime):
        if not runtime['snapshot']:
            return
        required = self.settings.get('requiredServers', ['codex_app'])
        if not isinstance(required, list) or not required or any(not isinstance(s, str) or not s for s in required):
            raise RuntimeError('App health server configuration is invalid')
        result = await self.owner.rpc('mcpServerStatus/list', dict(limit=100))
        servers = result.get('data')
        if not isinstance(servers, list):
            raise RuntimeError('App tool health could not be established')
        ready = set()
        for server in servers:
            if not isinstance(server, dict) or not server.get('tools') or server.get('toolsError'):
                continue
            status = server.get('runtimeStatus')
            if isinstance(status, dict):
                status = status.get('type', status.get('state'))
            # Older native revisions return null with their live tool result.
            if status is None or status in ('ready', 'connected', 'running', 'initialized'):
                ready.add(server.get('name'))
        if not set(required).issubset(ready):
            raise RuntimeError('Required App tools are not ready')

    def accept(self, runtime):
        self.active = runtime
        self.latest_fingerprint = runtime['fingerprint']
        self.state = 'ready' if runtime['snapshot'] else 'disabled'

    def status(self):
        snapshot = self.active.get('snapshot') if self.active else None
        return dict(environmentState=self.state, environmentCheckedAt=self.last_checked,
                    appPid=snapshot.get('pid') if snapshot else None,
                    appToolsOnline=bool(snapshot and self.state == 'ready'))

    def publish(self):
        from owner import atomic_json
        selected = self.owner.selection or {}
        child = self.owner.child
        atomic_json(self.owner.root/'status.json', dict(
            ownerPid=os.getpid(), backendPid=child.pid if child else None,
            cliVersion=selected.get('version'), state=self.owner.state,
            generation=getattr(self.owner, 'generation', 0), updatedAt=time.time(),
            **self.status()))

    def read_remote(self):
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args):
                raise ValueError('Remote status redirects are unsupported')
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        with opener.open(self.remote_url, timeout=4) as response:
            raw = response.read((8 << 20) + 1)
            if len(raw) > 8 << 20:
                raise ValueError('Remote status exceeds size limit')
            return json.loads(raw)

    async def refresh(self, snapshot):
        owner = self.owner
        observed_selection = owner.selection
        observed_runtime = self.active
        if owner.busy or owner.state != 'running' or owner.upgrade_lock.locked():
            self.state = 'waiting_for_idle'
            return
        try:
            if not remote_idle(await asyncio.to_thread(self.read_remote)):
                self.state = 'waiting_for_idle'
                return
        except Exception:
            self.state = 'environment_invalid'
            return
        async with owner.upgrade_lock:
            # An upgrade may have completed while the HTTP idle probe awaited.
            # Never apply its predecessor's captured environment to the new CLI.
            if owner.selection is not observed_selection or self.active is not observed_runtime:
                self.state = 'waiting_for_idle'
                return
            previous = self.active
            try:
                runtime = await self.prepare(owner.selection, snapshot=snapshot)
            except Exception:
                self.state = 'environment_invalid'
                return
            owner.busy = owner.frozen = True
            stopped = False
            try:
                if owner.pending or not remote_idle(await asyncio.to_thread(self.read_remote)):
                    self.state = 'waiting_for_idle'
                    return
                await owner.native_idle()
                selected = await owner.verify_selection(owner.selection, runtime=runtime)
                self.state = 'refreshing'
                owner.state = 'refreshing_environment'
                stopped = True
                await owner.stop()
                await owner.launch(selected, runtime=runtime)
                self.accept(runtime)
                owner.state = 'running'
            except Exception:
                if not stopped:
                    self.state = 'waiting_for_idle'
                    return
                try:
                    await owner.stop()
                    await owner.launch(owner.selection, runtime=previous)
                    self.state = 'refresh_failed'
                    owner.state = 'running'
                except Exception:
                    self.state = 'refresh_failed'
                    owner.state = 'failed'
            finally:
                owner.busy = False
                owner.frozen = owner.state != 'running'

    async def poll(self):
        self.last_checked = time.time()
        if not self.settings:
            return
        try:
            snapshot = await self.capture()
        except Exception:
            self.state = 'discovery_failed'
            return
        if not snapshot:
            self.state = 'app_unavailable'
            return
        self.latest_fingerprint = fingerprint(snapshot)
        if self.active and self.latest_fingerprint == self.active['fingerprint']:
            self.state = 'ready'
            return
        await self.refresh(snapshot)

    async def monitor(self):
        while True:
            try:
                await self.poll()
                self.publish()
            except Exception:
                # Never allow one OS/process/discovery/publication failure to
                # permanently disable refresh. No raw environment is logged.
                self.state = 'discovery_failed'
            await asyncio.sleep(self.settings.get('pollInterval', 15) if self.settings else 15)

    async def close(self):
        if self.capture_task:
            self.capture_task.cancel()
            await asyncio.gather(self.capture_task, return_exceptions=True)
            self.capture_task = None
