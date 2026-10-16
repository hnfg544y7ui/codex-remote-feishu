import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

import owner as owner_module
from owner import Owner


class IntegrityTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.root=Path(self.temp.name)
        self.owner=Owner(dict(directory=str(self.root),endpoint='ws://127.0.0.1:17550'))
        self.old=dict(binary=str(self.root/'old/codex'),version='0.162.1',sha256='old',
                      schemaVersion=1,components={'codex':'old','codex-code-mode-host':'old-helper'})
        self.new=dict(binary=str(self.root/'new/codex'),version='0.163.0',sha256='new',
                      schemaVersion=1,components={'codex':'new','codex-code-mode-host':'new-helper'})
        self.owner.selection=dict(self.old)
        self.owner.state='running'
        self.previous=dict(environment={'CODEX_CLI_PATH':self.old['binary']},snapshot=None,args=[],fingerprint=None)
        self.owner.runtime_env.active=self.previous
        self.owner.runtime_env.prepare=AsyncMock(return_value=dict(self.previous))
        self.owner.prepare=AsyncMock(return_value=dict(self.new))
        self.owner.verify_selection=AsyncMock(side_effect=lambda selected,**kw:dict(selected))
        self.owner.native_idle=AsyncMock()
        self.owner.stop=AsyncMock()
        self.owner.launch=AsyncMock()

    async def asyncTearDown(self):
        await self.owner.runtime_env.close()
        self.owner.log.close()
        self.temp.cleanup()

    def request(self,**extra):
        return dict(version='0.163.0',expectedVersion='0.162.1',**extra)

    async def test_bad_current_bundle_rejects_before_download_or_stop(self):
        self.owner.verify_selection.side_effect=RuntimeError('old helper checksum')
        with self.assertRaises(RuntimeError):await self.owner.upgrade(self.request())
        self.owner.prepare.assert_not_awaited()
        self.owner.stop.assert_not_awaited()
        self.assertFalse(self.owner.frozen)

    async def test_boot_uses_one_snapshot_for_validation_and_launch(self):
        await self.owner.boot()
        self.owner.runtime_env.prepare.assert_awaited_once()
        context=self.owner.runtime_env.prepare.return_value
        self.assertIs(self.owner.verify_selection.await_args.kwargs['runtime'],context)
        self.assertIs(self.owner.launch.await_args.kwargs['runtime'],context)

    async def test_bad_candidate_helper_rejects_before_stop(self):
        def verify(selected,**kwargs):
            if selected['version']=='0.163.0':raise RuntimeError('candidate helper missing')
            return dict(selected)
        self.owner.verify_selection.side_effect=verify
        with self.assertRaises(RuntimeError):await self.owner.upgrade(self.request())
        self.owner.stop.assert_not_awaited()
        self.assertEqual(self.owner.selection,self.old)

    async def test_bad_rollback_helper_keeps_current_native_running(self):
        self.owner.selection['previous']=dict(self.new)
        self.owner.verify_selection.side_effect=lambda selected,**kw: (_ for _ in ()).throw(RuntimeError('bad rollback')) if selected['version']=='0.163.0' else dict(selected)
        with self.assertRaises(RuntimeError):await self.owner.upgrade(self.request(rollback=True))
        self.owner.prepare.assert_not_awaited()
        self.owner.stop.assert_not_awaited()

    async def test_complete_components_survive_selection_commit(self):
        await self.owner.upgrade(self.request())
        saved=json.loads(self.owner.selection_file.read_text())
        self.assertEqual(saved['components'],self.new['components'])
        self.assertEqual(saved['previous']['components'],self.old['components'])

    async def test_selection_write_failure_restores_previous_bundle_and_environment(self):
        actual=owner_module.atomic_json
        failed=False
        def write(path,value):
            nonlocal failed
            if Path(path)==self.owner.selection_file and not failed:
                failed=True
                raise OSError('injected persistence failure')
            return actual(path,value)
        with patch('owner.atomic_json',side_effect=write):
            with self.assertRaises(OSError):await self.owner.upgrade(self.request())
        self.assertEqual(self.owner.launch.await_count,2)
        self.assertIs(self.owner.launch.await_args.kwargs['runtime'],self.previous)
        self.assertEqual(self.owner.selection,self.old)
        self.assertEqual(json.loads(self.owner.selection_file.read_text()),self.old)
        self.assertEqual(self.owner.state,'running')
        self.assertFalse(self.owner.frozen)

    async def test_double_selection_write_failure_keeps_admission_closed(self):
        actual=owner_module.atomic_json
        def write(path,value):
            if Path(path)==self.owner.selection_file:raise OSError('injected persistence failure')
            return actual(path,value)
        with patch('owner.atomic_json',side_effect=write):
            with self.assertRaisesRegex(RuntimeError,'rollback failed'):await self.owner.upgrade(self.request())
        self.assertEqual(self.owner.state,'failed')
        self.assertTrue(self.owner.frozen)

    async def test_stale_cli_environment_cannot_launch_new_binary(self):
        self.owner.launch=Owner.launch.__get__(self.owner)
        with patch('owner.asyncio.create_subprocess_exec',new_callable=AsyncMock) as spawn:
            with self.assertRaisesRegex(RuntimeError,'environment does not match'):
                await self.owner.launch(self.new,runtime=self.previous)
            spawn.assert_not_awaited()

    async def test_failure_stderr_cannot_persist_plugin_credentials(self):
        sentinel='fixture-private-identity-do-not-log'
        with self.assertRaises(RuntimeError):
            await self.owner.command(sys.executable,'-c',"import sys;sys.stderr.write('"+sentinel+"');sys.exit(3)")
        self.owner.log.flush()
        self.assertNotIn(sentinel,(self.root/'backend.log').read_text())

    async def test_legacy_partial_selection_never_becomes_ready_on_boot(self):
        self.owner.verify_selection.side_effect=RuntimeError('incomplete bundle')
        with self.assertRaises(RuntimeError):await self.owner.boot()
        self.owner.launch.assert_not_awaited()
        self.assertFalse(self.owner.selection_file.exists())


if __name__=='__main__':unittest.main()
