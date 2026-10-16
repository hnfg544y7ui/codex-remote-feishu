"""Preserve complete bundle evidence across the Go/Python maintenance boundary."""
import asyncio
import json
from pathlib import Path
import tempfile


def prepared_bundle(root, version, release):
    """Retain the preparer's pinned hashes; never rebaseline changed helpers."""
    if release.get('version') != version or not isinstance(release.get('components'), dict):
        raise RuntimeError('Prepared runtime evidence is incomplete')
    prefix = Path(root).resolve() / 'releases' / version
    binary = Path(release['binary']).resolve(strict=True)
    binary.relative_to(prefix)
    bundle_root = binary.parent.parent
    components = {}
    for relative, checksum in release['components'].items():
        path = Path(relative)
        if path.is_absolute() or '..' in path.parts or not path.parts:
            raise RuntimeError('Prepared component path is invalid')
        key = str((prefix/path).relative_to(bundle_root))
        components[key] = checksum
    main = str(binary.relative_to(bundle_root))
    if main not in components:
        raise RuntimeError('Prepared CLI checksum is missing')
    return dict(schemaVersion=1, binary=str(binary), version=version,
                bundleRoot=str(bundle_root), layout='npm-vendor',
                components=components, sha256=components[main])


async def verify(owner, selected, runtime=None):
    from owner import digest
    if not isinstance(selected, dict) or not selected.get('binary') or not selected.get('version'):
        raise RuntimeError('Selected runtime metadata is incomplete')
    if runtime is None:
        runtime = await owner.runtime_env.prepare(selected)
    environment = runtime['environment']
    if 'schemaVersion' not in selected:
        # A legacy single-executable record is migrated only after its pinned
        # executable and every platform helper are independently verified.
        if selected.get('sha256') != await asyncio.to_thread(digest, selected['binary']):
            raise RuntimeError('Selected CLI integrity check failed')
        data = await owner.command(owner.config['remoteBinary'], 'codex-runtime', 'bundle',
                                   '--binary', selected['binary'], '--version', selected['version'], env=environment)
    else:
        descriptor, name = tempfile.mkstemp(prefix='bundle-check-', suffix='.json', dir=owner.root)
        import os
        try:
            with os.fdopen(descriptor, 'w', encoding='utf-8') as stream:
                json.dump(selected, stream)
            data = await owner.command(owner.config['remoteBinary'], 'codex-runtime', 'verify-bundle',
                                       '--manifest', name, env=environment)
        finally:
            Path(name).unlink(missing_ok=True)
    bundle = json.loads(data)
    if bundle.get('version') != selected['version'] or Path(bundle.get('binary', '')).resolve() != Path(selected['binary']).resolve():
        raise RuntimeError('Verified runtime identity changed')
    key = str(Path(bundle['binary']).relative_to(bundle['bundleRoot']))
    checksum = bundle['components'][key]
    if selected.get('sha256') and selected['sha256'] != checksum:
        raise RuntimeError('Selected CLI differs from its complete bundle')
    bundle['sha256'] = checksum
    if 'previous' in selected:
        if not isinstance(selected['previous'], dict):
            raise RuntimeError('Previous runtime metadata is invalid')
        bundle['previous'] = selected['previous']
    return bundle
