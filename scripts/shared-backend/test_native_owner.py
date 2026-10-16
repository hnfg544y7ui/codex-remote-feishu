"""Opt-in real CLI smoke test; isolated CODEX_HOME, no prompts/model calls."""
import asyncio
import os
from pathlib import Path
import socket
import unittest
from unittest.mock import AsyncMock, patch

from owner import Owner, digest


def free_port():
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        return listener.getsockname()[1]


@unittest.skipUnless(os.environ.get('SHARED_CODEX_TEST_BINARY') and os.environ.get('SHARED_CODEX_TEST_DIRECTORY'), 'requires explicit test CLI and isolated evidence directory')
class NativeOwnerTests(unittest.IsolatedAsyncioTestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = Path(os.environ['SHARED_CODEX_TEST_DIRECTORY']).resolve()
        cls.directory.mkdir(parents=True, exist_ok=False)

    async def test_native_health_and_failed_update_restore_without_model_calls(self):
        binary = os.environ['SHARED_CODEX_TEST_BINARY']
        root = self.directory
        env = {k: v for k, v in os.environ.items() if not k.startswith(('CODEX_', 'OPENAI_'))}
        env['CODEX_HOME'] = str(root / 'codex-home')
        (root / 'codex-home').mkdir()
        with patch.dict(os.environ, env, clear=True):
            owner = Owner(dict(directory=str(root / 'owner'), initialBinary=binary,
                               endpoint='ws://127.0.0.1:' + str(free_port()),
                               nativeEndpoint='ws://127.0.0.1:' + str(free_port())))
            try:
                await owner.boot()
                await owner.native_idle()
                original = dict(owner.selection)
                old_pid = owner.child.pid
                # Wrong candidate version must fail BEFORE launching it; the
                # already stopped old native server must then be restored.
                target = '0.0.999'
                owner.prepare = AsyncMock(return_value=dict(binary=binary, version=target, sha256=digest(binary)))
                with self.assertRaisesRegex(RuntimeError, 'version changed'):
                    await owner.upgrade(dict(version=target, expectedVersion=original['version']))
                self.assertNotEqual(owner.child.pid, old_pid)
                self.assertEqual(owner.selection, original)
                self.assertEqual(owner.state, 'running')
                await owner.native_idle()
            finally:
                await owner.stop()
                owner.log.close()
                if owner.state != 'running':
                    print((root / 'owner/backend.log').read_text(errors='replace')[-2500:])


if __name__ == '__main__':
    unittest.main()
