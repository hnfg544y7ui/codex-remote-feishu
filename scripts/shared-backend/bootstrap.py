"""Stable startup entry: resolve the installed Remote's complete owner package."""
import argparse
import asyncio
import hashlib
import importlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

REQUIRED_FILES = frozenset(('host.py', 'owner.py', 'bootstrap.py'))
MAX_FILES = 64
MAX_FILE_BYTES = 1 << 20


def checked_path(path, directory=False):
    path = Path(os.path.abspath(path))
    for item in (path, *path.parents):
        if item.is_symlink():
            raise RuntimeError('Owner startup path is unsafe')
    info = path.stat()
    if (directory and not stat.S_ISDIR(info.st_mode)) or (not directory and not stat.S_ISREG(info.st_mode)):
        raise RuntimeError('Owner startup file type is unsafe')
    if os.name != 'nt' and (info.st_mode & 0o077 or info.st_uid != os.getuid()):
        raise RuntimeError('Owner startup files must be private to this user')
    return path


def read_config(path):
    path = checked_path(path)
    if path.stat().st_size > 1 << 20:
        raise RuntimeError('Owner configuration exceeds size limit')
    config = json.loads(path.read_text(encoding='utf-8'))
    if not isinstance(config, dict):
        raise RuntimeError('Owner configuration is invalid')
    return config


def verify_export(manifest, root):
    if (not isinstance(manifest, dict) or type(manifest.get('schemaVersion')) is not int or
            manifest['schemaVersion'] != 1):
        raise RuntimeError('Owner package manifest is invalid')
    files = manifest.get('files')
    if not isinstance(files, dict) or not REQUIRED_FILES.issubset(files) or len(files) > MAX_FILES:
        raise RuntimeError('Owner package contents are incomplete')
    if any(not isinstance(name, str) or not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]{0,127}\.py', name)
           for name in files):
        raise RuntimeError('Owner package module name is invalid')
    if any(re.fullmatch(r'(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])', name[:-3].upper()) for name in files):
        raise RuntimeError('Owner package module name is invalid')
    if len({name.lower() for name in files}) != len(files):
        raise RuntimeError('Owner package module names conflict')
    checksum = hashlib.sha256()
    for name in sorted(files):
        value = files[name]
        if not isinstance(value, str) or len(value) != 64 or any(c not in '0123456789abcdef' for c in value):
            raise RuntimeError('Owner package checksum is invalid')
        checksum.update((name + '\0' + value + '\n').encode())
    digest = checksum.hexdigest()
    if manifest.get('sha256') != digest:
        raise RuntimeError('Owner package identity is invalid')
    root = checked_path(root, True)
    directory = checked_path(manifest['directory'], True)
    if directory != root / digest:
        raise RuntimeError('Owner package directory is invalid')
    if manifest.get('entrypoint') != str(directory / 'host.py') or manifest.get('bootstrap') != str(directory / 'bootstrap.py'):
        raise RuntimeError('Owner package entrypoint is invalid')
    if {p.name for p in directory.iterdir()} != set(files):
        raise RuntimeError('Owner package has unexpected contents')
    for name in files:
        path = checked_path(directory / name)
        if path.stat().st_size > MAX_FILE_BYTES:
            raise RuntimeError('Owner package component exceeds size limit')
        with path.open('rb') as stream:
            content = stream.read(MAX_FILE_BYTES + 1)
        if len(content) > MAX_FILE_BYTES or hashlib.sha256(content).hexdigest() != files[name]:
            raise RuntimeError('Owner package checksum mismatch')
    return directory


def resolve_source(config, root):
    binary = config.get('remoteBinary')
    if not isinstance(binary, str) or not Path(binary).is_absolute() or not Path(binary).is_file():
        raise RuntimeError('Configured Remote executable is unavailable')
    environment = dict(os.environ)
    environment.pop('NOTIFY_SOCKET', None)
    try:
        result = subprocess.run([binary, 'codex-runtime', 'host-export', '--directory', str(root)],
                                env=environment, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                timeout=30, encoding='utf-8', errors='strict',
                                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        if result.returncode or len(result.stdout) > 1 << 20:
            raise RuntimeError('Owner package export failed')
        return verify_export(json.loads(result.stdout), root)
    except (OSError, subprocess.SubprocessError, UnicodeError, ValueError, KeyError, TypeError):
        raise RuntimeError('Owner package export unavailable') from None


def run(config_path, source_root):
    config = read_config(config_path)
    directory = resolve_source(config, source_root)
    # Importing the verified immutable package must not add __pycache__ files.
    # The previous package is not loaded before resolving the current binary.
    sys.dont_write_bytecode = True
    sys.path.insert(0, str(directory))
    if 'host' in sys.modules:
        raise RuntimeError('Owner implementation was already loaded')
    host = importlib.import_module('host')
    asyncio.run(host.serve(config))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', required=True)
    parser.add_argument('--source-root', required=True)
    args = parser.parse_args()
    try:
        run(args.config, args.source_root)
    except Exception:
        # Native subprocess output and captured runtime credentials stay private.
        print('Owner startup failed; verify private configuration and exported package', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
