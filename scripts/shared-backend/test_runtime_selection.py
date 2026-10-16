"""Cross-boundary selection contracts; no native/model tasks or production state."""
import asyncio
import copy
import hashlib
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock

from runtime_selection import verify, prepared_bundle


class SelectionTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.directory = self.root / 'bundle'
        self.directory.mkdir()
        self.binary = self.directory / ('codex.exe' if os.name == 'nt' else 'codex')
        self.binary.write_bytes(b'isolated-native-fixture')
        self.checksum = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        self.bundle = dict(schemaVersion=1, binary=str(self.binary), version='0.163.0',
                           bundleRoot=str(self.directory), layout='flat',
                           components={self.binary.name: self.checksum})
        self.legacy = dict(binary=str(self.binary), version='0.163.0', sha256=self.checksum)
        self.runtime = dict(environment={'CODEX_CLI_PATH': str(self.binary),
                                         'CODEX_TASK_WORKSPACE_VERIFYING_IDENTITY': 'private-memory-only'})
        self.owner = SimpleNamespace(root=self.root, config={'remoteBinary': 'remote-fixture'},
                                     command=AsyncMock(return_value=json.dumps(self.bundle)),
                                     runtime_env=SimpleNamespace(prepare=AsyncMock(return_value=self.runtime)))

    async def asyncTearDown(self):
        self.temp.cleanup()

    def assert_clean(self):
        self.assertEqual(list(self.root.glob('bundle-check-*.json')), [])
        for filename in ('selection.json', 'managed.json', 'remote-release.json'):
            self.assertFalse((self.root / filename).exists())

    def test_preparation_keeps_original_component_pins_instead_of_rehashing_changes(self):
        prefix=self.root/'releases/0.163.0'
        vendor=prefix/'node_modules/platform/vendor/triple'
        binary=vendor/'bin/codex'
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b'changed-after-preparation')
        checksums={'node_modules/platform/vendor/triple/bin/codex':'a'*64,
                   'node_modules/platform/vendor/triple/bin/codex-code-mode-host':'b'*64,
                   'node_modules/platform/vendor/triple/codex-resources/bwrap':'c'*64}
        release=dict(binary=str(binary),version='0.163.0',components=checksums)
        result=prepared_bundle(self.root,'0.163.0',release)
        expected={'bin/codex':'a'*64,'bin/codex-code-mode-host':'b'*64,'codex-resources/bwrap':'c'*64}
        self.assertEqual(result['components'],{str(Path(k)):v for k,v in expected.items()})
        self.assertEqual(result['sha256'],'a'*64)
        self.assertNotEqual(result['sha256'],hashlib.sha256(binary.read_bytes()).hexdigest())
        self.assertEqual(result['bundleRoot'],str(vendor))
        for bad in (dict(release,version='0.162.1'),dict(release,components={}),
                    dict(release,components={'../escape':'a'*64}),dict(release,binary=str(self.binary))):
            with self.subTest(release=bad), self.assertRaises((RuntimeError,ValueError)):
                prepared_bundle(self.root,'0.163.0',bad)

    async def test_legacy_migration_preserves_full_bundle_and_previous(self):
        selected = dict(self.legacy, previous=dict(version='0.162.1', binary='previous-native'))
        original = copy.deepcopy(selected)
        result = await verify(self.owner, selected)
        args, kwargs = self.owner.command.await_args
        self.assertEqual(args, ('remote-fixture', 'codex-runtime', 'bundle', '--binary',
                                str(self.binary), '--version', '0.163.0'))
        self.assertIs(kwargs['env'], self.runtime['environment'])
        self.owner.runtime_env.prepare.assert_awaited_once_with(selected)
        self.assertEqual(result['components'], self.bundle['components'])
        self.assertEqual(result['sha256'], self.checksum)
        self.assertEqual(result['previous'], selected['previous'])
        self.assertEqual(selected, original)
        self.assert_clean()

    async def test_legacy_bad_pin_rejects_without_invoking_go(self):
        with self.assertRaisesRegex(RuntimeError, 'integrity'):
            await verify(self.owner, dict(self.legacy, sha256='wrong'), runtime=self.runtime)
        self.owner.command.assert_not_awaited()
        self.owner.runtime_env.prepare.assert_not_awaited()
        self.assert_clean()

    async def test_full_manifest_is_private_complete_and_removed(self):
        selected = dict(self.bundle, sha256=self.checksum, previous=dict(self.legacy))
        original = copy.deepcopy(selected)
        seen = []

        async def command(*args, **kwargs):
            self.assertEqual(args[:4], ('remote-fixture', 'codex-runtime', 'verify-bundle', '--manifest'))
            manifest = Path(args[4])
            self.assertEqual(manifest.parent, self.root)
            self.assertEqual(json.loads(manifest.read_text()), selected)
            self.assertNotIn('private-memory-only', manifest.read_text())
            if os.name != 'nt':
                self.assertEqual(manifest.stat().st_mode & 0o077, 0)
            self.assertIs(kwargs['env'], self.runtime['environment'])
            seen.append(manifest)
            return json.dumps(self.bundle)

        self.owner.command.side_effect = command
        result = await verify(self.owner, selected, runtime=self.runtime)
        self.assertEqual(result, selected)
        self.assertEqual(selected, original)
        self.assertEqual(len(seen), 1)
        self.assertFalse(seen[0].exists())
        self.assert_clean()

    async def test_go_rejection_and_malformed_output_clean_temporary_manifest(self):
        for failure in (RuntimeError('helper checksum mismatch'), ValueError('bad package output')):
            with self.subTest(error=type(failure).__name__):
                self.owner.command.side_effect = failure
                with self.assertRaises(type(failure)):
                    await verify(self.owner, self.bundle, runtime=self.runtime)
                self.assert_clean()
        self.owner.command.side_effect = None
        self.owner.command.return_value = '{invalid-json'
        with self.assertRaises(json.JSONDecodeError):
            await verify(self.owner, self.bundle, runtime=self.runtime)
        self.assert_clean()

    async def test_cancellation_and_timeout_clean_temporary_manifest(self):
        async def waiting(*args, **kwargs):
            await asyncio.Event().wait()

        self.owner.command.side_effect = waiting
        with self.assertRaises(asyncio.TimeoutError):
            await asyncio.wait_for(verify(self.owner, self.bundle, runtime=self.runtime), .02)
        self.assert_clean()
        self.owner.command.side_effect = asyncio.CancelledError()
        with self.assertRaises(asyncio.CancelledError):
            await verify(self.owner, self.bundle, runtime=self.runtime)
        self.assert_clean()

    async def test_metadata_and_go_identity_mismatch_fail_closed(self):
        for selected in (None, [], {}, {'binary': str(self.binary)}, {'version': '0.163.0'}):
            with self.subTest(metadata=selected):
                with self.assertRaisesRegex(RuntimeError, 'incomplete'):
                    await verify(self.owner, selected, runtime=self.runtime)
        self.owner.command.assert_not_awaited()
        for changes in (dict(version='0.162.1'), dict(binary=str(self.root / 'another-native'))):
            self.owner.command.return_value = json.dumps(dict(self.bundle, **changes))
            with self.assertRaisesRegex(RuntimeError, 'identity changed'):
                await verify(self.owner, self.bundle, runtime=self.runtime)
            self.assert_clean()

    async def test_full_selection_pin_mismatch_and_invalid_previous_are_rejected(self):
        with self.assertRaisesRegex(RuntimeError, 'complete bundle'):
            await verify(self.owner, dict(self.bundle, sha256='wrong'), runtime=self.runtime)
        with self.assertRaisesRegex(RuntimeError, 'Previous runtime metadata'):
            await verify(self.owner, dict(self.bundle, previous=[]), runtime=self.runtime)
        self.assert_clean()

    async def test_manifest_serialization_error_closes_and_removes_temporary_file(self):
        selected = dict(self.bundle, unsupported={object()})
        with self.assertRaises(TypeError):
            await verify(self.owner, selected, runtime=self.runtime)
        self.owner.command.assert_not_awaited()
        self.assert_clean()


if __name__ == '__main__':
    unittest.main()
