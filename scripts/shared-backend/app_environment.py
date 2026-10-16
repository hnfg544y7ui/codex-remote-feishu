"""Current App runtime discovery. Credentials never leave process memory."""
import hashlib
import json
import os
from pathlib import Path
import stat

import app_windows

PROXY_KEYS = ('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'NO_PROXY',
              'http_proxy', 'https_proxy', 'all_proxy', 'no_proxy')
APP_KEYS = frozenset(PROXY_KEYS + (
    'CODEX_APP_TOOLS_PIPE_PATH', 'CODEX_TASK_WORKSPACE_VERIFYING_IDENTITY',
    'BROWSER_USE_ACCESS_VERIFYING_IDENTITY', 'CODEX_ELECTRON_USER_DATA_PATH',
    'CODEX_WINDOWS_SANDBOX_PACKAGE_FAMILY', 'CODEX_WINDOWS_REGISTERED_CORE',
    'CODEX_MCP_NODE_PATH', 'CODEX_BROWSER_USE_NODE_PATH', 'CODEX_TECTONIC_PATH',
    'CODEX_SHELL',
))
SELF_CONNECTION_KEYS = ('CODEX_APP_SERVER_WS_URL', 'CODEX_APP_SERVER_FORCE_CLI',
                        'NOTIFY_SOCKET')
RESOURCE_KEYS = ('CODEX_ELECTRON_RESOURCES_PATH', 'CODEX_MCP_NODE_PATH',
                 'CODEX_BROWSER_USE_NODE_PATH', 'CODEX_TECTONIC_PATH')


def filtered(environment):
    if not isinstance(environment, dict):
        return {}
    return {k: v for k, v in environment.items()
            if k in APP_KEYS and isinstance(v, str) and '\0' not in v}


def linux_environment(pid, executable):
    """Read live credentials, checking identity on both sides of the read."""
    process = Path('/proc') / str(pid)
    expected = Path(executable).resolve(strict=True)
    if (process / 'exe').resolve(strict=True) != expected:
        return None
    with (process / 'environ').open('rb') as stream:
        raw = stream.read((1 << 20) + 1)
    if len(raw) > 1 << 20 or (process / 'exe').resolve(strict=True) != expected:
        return None
    pairs = [value.split('=', 1) for value in raw.decode('utf-8', 'surrogateescape').split('\0')
             if '=' in value and not value.startswith('=')]
    return filtered(dict(pairs))


def linux_process_started(pid):
    fields = (Path('/proc') / str(pid) / 'stat').read_text().rsplit(')', 1)[1].split()
    ticks = int(fields[19])
    boot = next(int(line.split()[1]) for line in Path('/proc/stat').read_text().splitlines()
                if line.startswith('btime '))
    return boot + ticks / os.sysconf('SC_CLK_TCK')


def linux_record(path):
    """Read a private same-user regular file without following symlinks."""
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        return None
    flags = os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0)
    fd = os.open(path, flags)
    with os.fdopen(fd, 'r', encoding='utf-8') as stream:
        opened = os.fstat(stream.fileno())
        if ((opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino) or
                opened.st_uid != os.getuid() or opened.st_mode & 0o077):
            return None
        content = stream.read((1 << 20) + 1)
        if len(content) > 1 << 20:
            return None
        record = json.loads(content)
    if not isinstance(record, dict):
        return None
    pid = record.get('appPid')
    if type(pid) is not int or pid <= 0:
        return None
    started = linux_process_started(pid)
    if info.st_mtime < started:
        return None
    record['_verifiedStart'] = started
    return record


def discover(settings):
    """Return a validated live App snapshot, or None while unavailable.

    Linux captured environments require private-file ownership, live process
    identity and post-start capture time. Failures never include process output.
    """
    if not settings:
        return None
    try:
        mode = settings.get('mode', 'windows' if os.name == 'nt' else 'linux')
        if mode == 'windows':
            snapshot = app_windows.discover(settings, APP_KEYS)
        elif mode == 'linux':
            executable = str(Path(settings['appExe']).resolve(strict=True))
            record = linux_record(Path(settings['runtimeFile']))
            if not record:
                return None
            pid = record.get('appPid')
            if type(pid) is not int or pid <= 0:
                return None
            live = linux_environment(pid, executable)
            if live is None or linux_process_started(pid) != record['_verifiedStart']:
                return None
            environment = filtered(record.get('environment', {}))
            environment.update(live)
            snapshot = {'pid': pid, 'environment': environment,
                        'executable': executable,
                        'resources': str(Path(executable).parent / 'resources')}
        else:
            return None
        if not snapshot:
            return None
        snapshot['environment'] = filtered(snapshot['environment'])
        if not all(snapshot['environment'].get(k) for k in (
                'CODEX_APP_TOOLS_PIPE_PATH', 'CODEX_TASK_WORKSPACE_VERIFYING_IDENTITY')):
            return None
        if not Path(snapshot['resources']).is_dir():
            return None
        return snapshot
    except (OSError, ValueError, KeyError, TypeError, RuntimeError, IndexError, StopIteration):
        return None


def _current_file(value, resources=None, executable=False):
    if not value:
        return None
    try:
        if not Path(value).is_absolute():
            return None
        candidate = Path(value).resolve(strict=True)
        if resources is not None:
            candidate.relative_to(resources.resolve(strict=True))
        if candidate.is_file() and (not executable or os.name == 'nt' or os.access(candidate, os.X_OK)):
            return str(candidate)
    except (OSError, ValueError):
        pass
    return None


def compose(base, owner_config, snapshot, binary):
    """Compose one child environment without mutating any global environment."""
    environment = dict(base)
    for key in APP_KEYS - frozenset(PROXY_KEYS):
        environment.pop(key, None)
    for key in RESOURCE_KEYS + SELF_CONNECTION_KEYS + ('CODEX_INTERNAL_ORIGINATOR_OVERRIDE',):
        environment.pop(key, None)
    environment['CODEX_CLI_PATH'] = str(binary)
    environment['CODEX_HOME'] = str(owner_config.get('codexHome') or base.get('CODEX_HOME') or Path.home() / '.codex')
    # The owner always supplies its original base, never a previous composed
    # child. Strip inherited CLI paths too, so old release bins cannot win.
    separator = ';' if owner_config.get('appEnvironment', {}).get('mode') == 'windows' or os.name == 'nt' else ':'
    previous_cli = str(base.get('CODEX_CLI_PATH', ''))
    obsolete = {str(Path(binary).parent), str(Path(previous_cli).parent) if previous_cli else ''}
    if previous_cli:
        obsolete.add(str(Path(previous_cli).parent.parent / 'path'))
    obsolete.update(str(Path(base[k]).parent) for k in RESOURCE_KEYS if base.get(k))
    if base.get('CODEX_ELECTRON_RESOURCES_PATH'):
        obsolete.add(base['CODEX_ELECTRON_RESOURCES_PATH'])
    paths = [p for p in environment.get('PATH', '').split(separator) if p and p not in obsolete]
    selected_paths = [str(Path(binary).parent)]
    vendor_path = Path(binary).parent.parent / 'path'
    if vendor_path.is_dir():
        selected_paths.append(str(vendor_path))
    environment['PATH'] = separator.join(list(dict.fromkeys(selected_paths + paths)))
    if not snapshot:
        return environment
    app = filtered(snapshot['environment'])
    environment.update(app)
    resources = Path(snapshot['resources'])
    environment['CODEX_ELECTRON_RESOURCES_PATH'] = str(resources)
    environment['CODEX_INTERNAL_ORIGINATOR_OVERRIDE'] = 'Codex Desktop'
    node_name = 'node.exe' if separator == ';' else 'node'
    node = None
    # Current App versions may manage Node in a per-user runtime cache outside
    # resources. Trust only paths freshly read from the verified App process.
    for candidate in (app.get('CODEX_MCP_NODE_PATH'), app.get('CODEX_BROWSER_USE_NODE_PATH')):
        node = _current_file(candidate, executable=True)
        if node:
            break
    for candidate in (str(resources / 'cua_node' / 'bin' / node_name),
                      str(resources / 'cua_node' / node_name)) if not node else ():
        node = _current_file(candidate, resources, executable=True)
        if node:
            break
    for key in ('CODEX_MCP_NODE_PATH', 'CODEX_BROWSER_USE_NODE_PATH'):
        environment.pop(key, None)
        if node:
            environment[key] = node
    tectonic = _current_file(str(resources / 'tectonic' / ('tectonic.exe' if separator == ';' else 'tectonic')), resources, True)
    environment.pop('CODEX_TECTONIC_PATH', None)
    if tectonic:
        environment['CODEX_TECTONIC_PATH'] = tectonic
    return environment


def fingerprint(snapshot):
    """Opaque identity; callers must not persist the underlying environment."""
    if not snapshot:
        return None
    value = {k: snapshot[k] for k in ('pid', 'executable', 'resources')}
    value['environment'] = filtered(snapshot['environment'])
    return hashlib.sha256(json.dumps(value, sort_keys=True).encode()).hexdigest()
