"""Source package handoff tests with synthetic manifests and no production owner."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch, Mock

import bootstrap

CURRENT_FILES = ('host.py', 'owner.py', 'runtime_selection.py', 'runtime_environment.py',
                 'app_environment.py', 'app_windows.py', 'bootstrap.py')


class BootstrapTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'sources'
        self.root.mkdir(mode=0o700)
        self.directory, self.manifest = self.package(CURRENT_FILES)
        self.files = self.manifest['files']
        self.config = self.root.parent / 'owner.json'
        self.config.write_text(json.dumps({'remoteBinary': sys.executable}))
        self.config.chmod(0o600)

    def package(self, names):
        files = {name: hashlib.sha256(name.encode()).hexdigest() for name in names}
        checksum = hashlib.sha256()
        for name in sorted(files):
            checksum.update((name + '\0' + files[name] + '\n').encode())
        digest = checksum.hexdigest()
        directory = self.root / digest
        directory.mkdir(mode=0o700)
        for name in files:
            path = directory / name
            path.write_bytes(name.encode())
            path.chmod(0o600)
        return directory, {'schemaVersion': 1, 'directory': str(directory),
                           'entrypoint': str(directory / 'host.py'),
                           'bootstrap': str(directory / 'bootstrap.py'),
                           'sha256': digest, 'files': files}

    def test_complete_content_addressed_export_is_verified(self):
        self.assertEqual(bootstrap.verify_export(self.manifest, self.root), self.directory)

    def test_fixed_bootstrap_accepts_future_eight_module_export(self):
        directory, manifest = self.package(CURRENT_FILES + ('future_runtime_probe.py',))
        self.assertEqual(len(manifest['files']), 8)
        self.assertEqual(bootstrap.verify_export(manifest, self.root), directory)
        with patch.object(bootstrap.subprocess, 'run', return_value=Mock(
                returncode=0, stdout=json.dumps(manifest))):
            self.assertEqual(bootstrap.resolve_source({'remoteBinary': sys.executable}, self.root), directory)
        (directory / 'unlisted_future_module.py').touch()
        with self.assertRaisesRegex(RuntimeError, 'unexpected contents'):
            bootstrap.verify_export(manifest, self.root)

    def test_unsafe_module_names_and_case_collisions_are_rejected(self):
        invalid = ('../evil.py', 'nested/evil.py', 'nested\\evil.py', '/absolute.py',
                   'C:evil.py', 'evil.txt', 'evil.pyc', '.hidden.py', '123.py',
                   '模块.py', 'module-name.py', 'bad\0name.py', 'NUL.py', 'con.py',
                   'COM1.py', 'LPT9.py', 'a' * 129 + '.py')
        for name in invalid:
            with self.subTest(name=repr(name)):
                manifest = dict(self.manifest, files=dict(self.files, **{name: '0' * 64}))
                with self.assertRaisesRegex(RuntimeError, 'module name is invalid'):
                    bootstrap.verify_export(manifest, self.root)
        manifest = dict(self.manifest, files=dict(self.files, **{'HOST.py': '0' * 64}))
        with self.assertRaisesRegex(RuntimeError, 'names conflict'):
            bootstrap.verify_export(manifest, self.root)

    def test_module_count_size_and_required_module_limits(self):
        many = dict(self.files, **{'module_' + str(n) + '.py': '0' * 64 for n in range(bootstrap.MAX_FILES)})
        with self.assertRaisesRegex(RuntimeError, 'contents are incomplete'):
            bootstrap.verify_export(dict(self.manifest, files=many), self.root)
        missing = dict(self.files)
        missing.pop('owner.py')
        with self.assertRaisesRegex(RuntimeError, 'contents are incomplete'):
            bootstrap.verify_export(dict(self.manifest, files=missing), self.root)
        with (self.directory / 'owner.py').open('wb') as stream:
            stream.truncate(bootstrap.MAX_FILE_BYTES + 1)
        with self.assertRaisesRegex(RuntimeError, 'size limit'):
            bootstrap.verify_export(self.manifest, self.root)

    def test_corrupt_file_manifest_or_extra_content_is_rejected(self):
        (self.directory / 'owner.py').write_text('tampered')
        with self.assertRaisesRegex(RuntimeError, 'checksum mismatch'):
            bootstrap.verify_export(self.manifest, self.root)
        (self.directory / 'owner.py').write_text('owner.py')
        (self.directory / 'unexpected.py').touch()
        with self.assertRaisesRegex(RuntimeError, 'unexpected contents'):
            bootstrap.verify_export(self.manifest, self.root)
        (self.directory / 'unexpected.py').unlink()
        wrong = dict(self.manifest, entrypoint=str(self.directory / 'other.py'))
        with self.assertRaisesRegex(RuntimeError, 'entrypoint'):
            bootstrap.verify_export(wrong, self.root)
        wrong = dict(self.manifest, sha256='0' * 64)
        with self.assertRaisesRegex(RuntimeError, 'identity'):
            bootstrap.verify_export(wrong, self.root)

    def test_symlink_and_private_config_validation(self):
        if os.name != 'nt':
            self.config.chmod(0o644)
            with self.assertRaisesRegex(RuntimeError, 'private'):
                bootstrap.read_config(self.config)
            self.config.chmod(0o600)
        link = self.root.parent / 'linked-config'
        try:
            link.symlink_to(self.config)
        except OSError:
            return
        with self.assertRaisesRegex(RuntimeError, 'unsafe'):
            bootstrap.read_config(link)

    def test_export_subprocess_is_hidden_bounded_and_cannot_notify_parent_service(self):
        before = dict(os.environ)
        response = Mock(returncode=0, stdout=json.dumps(self.manifest))
        with patch.dict(os.environ, {'NOTIFY_SOCKET': 'synthetic-service-notify'}), \
                patch.object(bootstrap.subprocess, 'run', return_value=response) as run:
            self.assertEqual(bootstrap.resolve_source({'remoteBinary': sys.executable}, self.root), self.directory)
            self.assertEqual(os.environ['NOTIFY_SOCKET'], 'synthetic-service-notify')
        self.assertEqual(dict(os.environ), before)
        arguments = run.call_args
        self.assertEqual(arguments.args[0], [sys.executable, 'codex-runtime', 'host-export', '--directory', str(self.root)])
        self.assertNotIn('NOTIFY_SOCKET', arguments.kwargs['env'])
        self.assertEqual(arguments.kwargs['timeout'], 30)
        self.assertEqual(arguments.kwargs['stderr'], subprocess.DEVNULL)
        self.assertEqual(arguments.kwargs['creationflags'], getattr(subprocess, 'CREATE_NO_WINDOW', 0))

    def test_subprocess_errors_do_not_expose_command_or_output(self):
        failures = [subprocess.TimeoutExpired('secret-command', 30, stderr='secret-stderr'),
                    subprocess.CalledProcessError(1, 'secret-command', stderr='secret-stderr')]
        for error in failures:
            with patch.object(bootstrap.subprocess, 'run', side_effect=error):
                with self.assertRaisesRegex(RuntimeError, '^Owner package export unavailable$'):
                    bootstrap.resolve_source({'remoteBinary': sys.executable}, self.root)

    def test_host_is_loaded_in_current_process_only_after_export_verification(self):
        observations = []

        async def serve(config):
            observations.append((os.getpid(), config['remoteBinary'], os.environ.get('NOTIFY_SOCKET')))

        host = types.SimpleNamespace(serve=serve)
        previous_path = list(sys.path)
        previous_bytecode = sys.dont_write_bytecode
        try:
            with patch.dict(sys.modules), \
                    patch.dict(os.environ, {'NOTIFY_SOCKET': 'synthetic-parent-notify'}), \
                    patch.object(bootstrap, 'resolve_source', return_value=self.directory) as resolve, \
                    patch.object(bootstrap.importlib, 'import_module', return_value=host) as load:
                sys.modules.pop('host', None)
                bootstrap.run(self.config, self.root)
                resolve.assert_called_once()
                load.assert_called_once_with('host')
                self.assertEqual(sys.path[0], str(self.directory))
                self.assertTrue(sys.dont_write_bytecode)
        finally:
            sys.path[:] = previous_path
            sys.dont_write_bytecode = previous_bytecode
        self.assertEqual(observations, [(os.getpid(), sys.executable, 'synthetic-parent-notify')])


if __name__ == '__main__':
    unittest.main()
