"""Runtime discovery and composition regression tests; synthetic credentials only."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch, Mock

import app_environment as app
import app_windows


class EnvironmentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.exe = self.root / 'new-app' / 'ChatGPT.exe'
        self.resources = self.exe.parent / 'resources'
        self.resources.mkdir(parents=True)
        self.exe.touch()
        self.env = {'CODEX_APP_TOOLS_PIPE_PATH': 'synthetic-pipe',
                    'CODEX_TASK_WORKSPACE_VERIFYING_IDENTITY': 'synthetic-identity',
                    'CODEX_ELECTRON_USER_DATA_PATH': str(self.root / 'profile')}
        self.snapshot = {'pid': 42, 'environment': self.env,
                         'executable': str(self.exe), 'resources': str(self.resources)}
        self.config = {'codexHome': str(self.root / 'home'), 'appEnvironment': {'mode': 'linux'}}

    def node(self):
        node = self.resources / 'cua_node' / 'bin' / ('node.exe' if os.name == 'nt' else 'node')
        node.parent.mkdir(parents=True)
        node.touch()
        node.chmod(0o700)
        return node

    def test_selected_cli_and_current_resources_replace_old_values(self):
        node = self.node()
        old = str(self.root / 'old' / 'codex')
        binary = str(self.root / 'new' / 'codex')
        base = {'CODEX_CLI_PATH': old, 'CODEX_HOME': 'wrong',
                'CODEX_MCP_NODE_PATH': str(self.root / 'old-node' / 'node'),
                'CODEX_ELECTRON_RESOURCES_PATH': str(self.root / 'old-resources'),
                'PATH': os.pathsep.join([str(Path(old).parent), 'system'])}
        before = dict(os.environ)
        result = app.compose(base, self.config, self.snapshot, binary)
        self.assertEqual(result['CODEX_CLI_PATH'], binary)
        self.assertEqual(result['CODEX_HOME'], self.config['codexHome'])
        self.assertEqual(result['CODEX_ELECTRON_RESOURCES_PATH'], str(self.resources))
        self.assertEqual(result['CODEX_MCP_NODE_PATH'], str(node))
        self.assertEqual(result['PATH'], os.pathsep.join([str(Path(binary).parent), 'system']))
        self.assertEqual(dict(os.environ), before)
        self.assertEqual(base['CODEX_CLI_PATH'], old)

    def test_no_app_preserves_cli_and_proxy_without_stale_app_credentials(self):
        base = dict(self.env, HTTPS_PROXY='http://proxy', PATH='system', NOTIFY_SOCKET='notify',
                    CODEX_APP_SERVER_WS_URL='ws://self', CODEX_APP_SERVER_FORCE_CLI='1')
        result = app.compose(base, self.config, None, '/selected/codex')
        self.assertEqual(result['HTTPS_PROXY'], 'http://proxy')
        self.assertNotIn('CODEX_TASK_WORKSPACE_VERIFYING_IDENTITY', result)
        self.assertTrue(all(k not in result for k in app.SELF_CONNECTION_KEYS))

    def test_untrusted_fields_and_missing_node_are_not_inherited(self):
        self.env.update(CODEX_MCP_NODE_PATH='missing', CODEX_CLI_PATH='wrong',
                        NOTIFY_SOCKET='unsafe', OTHER_SECRET='private', PATH='old')
        result = app.compose({'PATH': 'system'}, self.config, self.snapshot, '/selected/codex')
        self.assertNotIn('OTHER_SECRET', result)
        self.assertNotIn('CODEX_MCP_NODE_PATH', result)
        self.assertNotIn('NOTIFY_SOCKET', result)
        self.assertEqual(result['CODEX_CLI_PATH'], '/selected/codex')

    def test_current_app_external_node_is_accepted_but_old_base_node_is_ignored(self):
        node = self.root / 'current-app-cache-node'
        node.touch()
        node.chmod(0o700)
        self.env['CODEX_MCP_NODE_PATH'] = str(node)
        result = app.compose({'CODEX_MCP_NODE_PATH': 'old-base-node'}, self.config, self.snapshot, '/selected/codex')
        self.assertEqual(result['CODEX_MCP_NODE_PATH'], str(node))
        self.env.pop('CODEX_MCP_NODE_PATH')
        self.assertNotIn('CODEX_MCP_NODE_PATH', app.compose(
            {'CODEX_MCP_NODE_PATH': str(node)}, self.config, self.snapshot, '/selected/codex'))

    def test_selected_bundle_vendor_path_replaces_previous_vendor_path(self):
        old = self.root / 'old-vendor' / 'bin' / 'codex'
        binary = self.root / 'new-vendor' / 'bin' / 'codex'
        vendor = binary.parent.parent / 'path'
        vendor.mkdir(parents=True)
        base = {'CODEX_CLI_PATH': str(old), 'PATH': os.pathsep.join([
            str(old.parent), str(old.parent.parent / 'path'), 'system'])}
        result = app.compose(base, self.config, None, str(binary))
        self.assertEqual(result['PATH'], os.pathsep.join([str(binary.parent), str(vendor), 'system']))

    def test_fingerprint_tracks_pid_paths_and_credentials_without_exposing_them(self):
        original = app.fingerprint(self.snapshot)
        self.assertEqual(len(original), 64)
        self.env['OTHER_SECRET'] = 'ignored'
        self.assertEqual(original, app.fingerprint(self.snapshot))
        self.env['CODEX_TASK_WORKSPACE_VERIFYING_IDENTITY'] = 'updated-synthetic'
        self.assertNotEqual(original, app.fingerprint(self.snapshot))
        self.assertIsNone(app.fingerprint(None))

    def test_windows_follows_explicit_family_and_validates_profile(self):
        settings = {'mode': 'windows', 'appExe': 'removed-old-version.exe',
                    'sandboxPackageFamily': 'OpenAI.Codex_selected',
                    'appUserData': self.env['CODEX_ELECTRON_USER_DATA_PATH']}
        package = {'PackageFamilyName': 'OpenAI.Codex_selected',
                   'InstallLocation': str(self.exe.parent.parent)}
        # Package layout is <install>/app/ChatGPT.exe.
        exe = self.root / 'package' / 'app' / 'ChatGPT.exe'
        (exe.parent / 'resources').mkdir(parents=True)
        exe.touch()
        package['InstallLocation'] = str(exe.parent.parent)
        process = {'ProcessId': 42, 'ExecutablePath': str(exe)}
        with patch.object(app_windows, 'powershell', side_effect=[package, process]), \
                patch.object(app_windows, 'read_environment', return_value=self.env):
            result = app.discover(settings)
        self.assertEqual(result['executable'], str(exe))
        self.assertEqual(result['resources'], str(exe.parent / 'resources'))
        wrong = dict(self.env, CODEX_ELECTRON_USER_DATA_PATH='another-profile')
        with patch.object(app_windows, 'powershell', side_effect=[package, process]), \
                patch.object(app_windows, 'read_environment', return_value=wrong):
            self.assertIsNone(app.discover(settings))

    def test_windows_family_only_resolves_current_package_without_configured_executable(self):
        exe = self.root / 'package' / 'app' / 'ChatGPT.exe'
        (exe.parent / 'resources').mkdir(parents=True)
        exe.touch()
        settings = {'mode': 'windows', 'sandboxPackageFamily': 'OpenAI.Codex_selected',
                    'appUserData': self.env['CODEX_ELECTRON_USER_DATA_PATH']}
        package = {'PackageFamilyName': settings['sandboxPackageFamily'],
                   'InstallLocation': str(exe.parent.parent)}
        process = {'ProcessId': 42, 'ExecutablePath': str(exe)}
        with patch.object(app_windows, 'powershell', side_effect=[package, process]), \
                patch.object(app_windows, 'read_environment', return_value=self.env):
            result = app.discover(settings)
        self.assertEqual(result['executable'], str(exe))
        self.assertEqual(result['pid'], 42)
        self.assertIsNone(app.discover({'mode': 'windows'}))

    def test_windows_default_empty_profile_is_matched_without_configured_profile(self):
        settings = {'mode': 'windows', 'appExe': str(self.exe)}
        process = {'ProcessId': 42, 'ExecutablePath': str(self.exe)}
        environment = dict(self.env)
        environment.pop('CODEX_ELECTRON_USER_DATA_PATH')
        with patch.object(app_windows, 'powershell', return_value=process), \
                patch.object(app_windows, 'read_environment', return_value=environment):
            result = app.discover(settings)
        self.assertEqual(result['pid'], 42)
        with patch.object(app_windows, 'powershell', return_value=process), \
                patch.object(app_windows, 'read_environment', return_value=self.env):
            self.assertIsNone(app.discover(settings))

    def test_windows_multiple_processes_with_same_profile_are_ambiguous(self):
        settings = {'mode': 'windows', 'appExe': str(self.exe),
                    'appUserData': self.env['CODEX_ELECTRON_USER_DATA_PATH']}
        records = [{'ProcessId': pid, 'ExecutablePath': str(self.exe)} for pid in (42, 43)]
        with patch.object(app_windows, 'powershell', return_value=records), \
                patch.object(app_windows, 'read_environment', return_value=self.env):
            self.assertIsNone(app.discover(settings))
        wrong_profile = dict(self.env, CODEX_ELECTRON_USER_DATA_PATH='other-profile')
        with patch.object(app_windows, 'powershell', return_value=records), \
                patch.object(app_windows, 'read_environment', side_effect=[wrong_profile, self.env]):
            self.assertEqual(app.discover(settings)['pid'], 43)
        settings.pop('appUserData')
        default = dict(self.env)
        default.pop('CODEX_ELECTRON_USER_DATA_PATH')
        with patch.object(app_windows, 'powershell', return_value=records), \
                patch.object(app_windows, 'read_environment', return_value=default):
            self.assertIsNone(app.discover(settings))

    def test_windows_unavailable_missing_files_and_timeout_are_safe(self):
        settings = {'mode': 'windows', 'appExe': str(self.exe),
                    'appUserData': self.env['CODEX_ELECTRON_USER_DATA_PATH']}
        with patch.object(app_windows, 'powershell', return_value=None):
            self.assertIsNone(app.discover(settings))
        settings['appExe'] = 'missing'
        self.assertIsNone(app.discover(settings))
        with patch.object(app_windows.subprocess, 'run', side_effect=subprocess.TimeoutExpired('secret-command', 8)):
            with self.assertRaisesRegex(RuntimeError, '^App discovery unavailable$') as caught:
                app_windows.powershell('secret-command')
            self.assertIsNone(caught.exception.__cause__)

    def test_windows_probes_are_bounded_and_hidden(self):
        with patch.object(app_windows.subprocess, 'run', return_value=Mock(returncode=0, stdout='null')) as run:
            app_windows.powershell('probe')
        self.assertEqual(run.call_args.kwargs['timeout'], 8)
        self.assertEqual(run.call_args.kwargs['creationflags'], getattr(subprocess, 'CREATE_NO_WINDOW', 0))

    @unittest.skipUnless(os.name == 'nt', 'Windows read-only process reader')
    def test_windows_reader_checks_actual_process_and_limits_environment(self):
        environment = dict(self.env, OTHER_SECRET='never-return-this',
                           SystemRoot=os.environ.get('SystemRoot', 'C:\\Windows'))
        child = subprocess.Popen([sys.executable, '-c', 'import sys; sys.stdin.buffer.read(1)'],
                                 env=environment, stdin=subprocess.PIPE,
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                 creationflags=subprocess.CREATE_NO_WINDOW)
        try:
            captured = app_windows.read_environment(child.pid, sys.executable, app.APP_KEYS)
            self.assertEqual(captured, self.env)
            with self.assertRaisesRegex(RuntimeError, '^App discovery unavailable$'):
                app_windows.read_environment(child.pid, str(self.exe), app.APP_KEYS)
        finally:
            child.communicate(b'x', timeout=5)

    @unittest.skipIf(os.name == 'nt', 'Linux ownership and procfs integration')
    def test_linux_private_capture_rejects_permissions_stale_record_and_pid_reuse(self):
        runtime = self.root / 'runtime.json'
        runtime.write_text(json.dumps({'appPid': os.getpid(), 'environment': self.env}))
        runtime.chmod(0o600)
        self.assertIsNotNone(app.linux_record(runtime))
        runtime.chmod(0o644)
        self.assertIsNone(app.linux_record(runtime))
        runtime.chmod(0o600)
        with patch.object(app, 'linux_process_started', return_value=time.time() + 60):
            self.assertIsNone(app.linux_record(runtime))
        link = self.root / 'link.json'
        link.symlink_to(runtime)
        self.assertIsNone(app.linux_record(link))
        with patch.object(app.os, 'getuid', return_value=os.getuid() + 1):
            self.assertIsNone(app.linux_record(runtime))

    @unittest.skipIf(os.name == 'nt', 'Linux procfs integration')
    def test_linux_live_environment_is_whitelisted_and_identity_checked(self):
        executable = str(Path('/proc/self/exe').resolve())
        live = app.linux_environment(os.getpid(), executable)
        self.assertTrue(set(live).issubset(app.APP_KEYS))
        self.assertIsNone(app.linux_environment(os.getpid(), str(self.exe)))
        self.assertLess(app.linux_process_started(os.getpid()), time.time() + 1)

    def test_linux_capture_requires_live_matching_process(self):
        settings = {'mode': 'linux', 'appExe': str(self.exe), 'runtimeFile': 'private-runtime'}
        record = {'appPid': 42, '_verifiedStart': 10,
                  'environment': dict(self.env, OTHER_SECRET='ignored')}
        with patch.object(app, 'linux_record', return_value=record), \
                patch.object(app, 'linux_environment', return_value=None):
            self.assertIsNone(app.discover(settings))
        with patch.object(app, 'linux_record', return_value=record), \
                patch.object(app, 'linux_environment', return_value={'HTTPS_PROXY': 'http://current-proxy'}), \
                patch.object(app, 'linux_process_started', return_value=10):
            result = app.discover(settings)
        self.assertEqual(result['environment']['HTTPS_PROXY'], 'http://current-proxy')
        self.assertNotIn('OTHER_SECRET', result['environment'])
        with patch.object(app, 'linux_record', return_value=record), \
                patch.object(app, 'linux_environment', return_value=self.env), \
                patch.object(app, 'linux_process_started', return_value=11):
            self.assertIsNone(app.discover(settings))


if __name__ == '__main__':
    unittest.main()
