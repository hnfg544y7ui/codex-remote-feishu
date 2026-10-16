"""Read-only Windows App discovery; bounded hidden probes and no secret logs."""
import ctypes
from ctypes import wintypes
import json
import ntpath
from pathlib import Path
import re
import subprocess


def powershell(command):
    try:
        result = subprocess.run(['powershell.exe', '-NoProfile', '-NonInteractive',
                                 '-Command', command], capture_output=True,
                                encoding='utf-8', errors='replace', timeout=8,
                                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        if result.returncode or len(result.stdout) > 1 << 20:
            raise RuntimeError('App discovery unavailable')
        return json.loads(result.stdout.strip() or 'null')
    except (OSError, ValueError, subprocess.SubprocessError):
        raise RuntimeError('App discovery unavailable') from None


def resolve_executable(settings):
    configured = settings.get('appExe')
    family = settings.get('sandboxPackageFamily', '')
    if family:
        if not re.fullmatch(r'[A-Za-z0-9.-]+_[A-Za-z0-9]+', family):
            raise RuntimeError('App discovery unavailable')
        name = family.rsplit('_', 1)[0]
        record = powershell("[Console]::OutputEncoding=[Text.Encoding]::UTF8; Get-AppxPackage -Name '" + name +
                            "' | Select-Object PackageFamilyName,InstallLocation | ConvertTo-Json -Compress")
        records = record if isinstance(record, list) else [record]
        matches = [r for r in records if isinstance(r, dict) and r.get('PackageFamilyName') == family]
        if len(matches) != 1 or not isinstance(matches[0].get('InstallLocation'), str):
            raise RuntimeError('App discovery unavailable')
        configured = str(Path(matches[0]['InstallLocation']) / 'app' / 'ChatGPT.exe')
    if not isinstance(configured, str) or not configured or not Path(configured).is_file():
        raise RuntimeError('App discovery unavailable')
    return configured


def read_environment(pid, expected_exe, allowed):
    if ctypes.sizeof(ctypes.c_void_p) != 8:
        raise RuntimeError('App discovery unavailable')
    kernel = ctypes.WinDLL('kernel32', use_last_error=True)
    native = ctypes.WinDLL('ntdll')
    kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel.OpenProcess.restype = wintypes.HANDLE
    kernel.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel.ReadProcessMemory.argtypes = [wintypes.HANDLE, ctypes.c_void_p, ctypes.c_void_p,
                                        ctypes.c_size_t, ctypes.POINTER(ctypes.c_size_t)]
    kernel.QueryFullProcessImageNameW.argtypes = [wintypes.HANDLE, wintypes.DWORD,
                                                wintypes.LPWSTR, ctypes.POINTER(wintypes.DWORD)]

    class Basic(ctypes.Structure):
        _fields_ = [('reserved1', ctypes.c_void_p), ('peb', ctypes.c_void_p),
                    ('reserved2', ctypes.c_void_p * 2), ('pid', ctypes.c_void_p),
                    ('reserved3', ctypes.c_void_p)]

    native.NtQueryInformationProcess.argtypes = [wintypes.HANDLE, wintypes.ULONG,
                                                ctypes.c_void_p, wintypes.ULONG,
                                                ctypes.c_void_p]

    handle = kernel.OpenProcess(0x410, False, pid)
    if not handle:
        raise RuntimeError('App discovery unavailable')

    def read(address, size):
        buffer = ctypes.create_string_buffer(size)
        received = ctypes.c_size_t()
        kernel.ReadProcessMemory(handle, address, buffer, size, ctypes.byref(received))
        if not received.value:
            raise RuntimeError('App discovery unavailable')
        return buffer.raw[:received.value]

    try:
        image = ctypes.create_unicode_buffer(32768)
        size = wintypes.DWORD(len(image))
        if not kernel.QueryFullProcessImageNameW(handle, 0, image, ctypes.byref(size)):
            raise RuntimeError('App discovery unavailable')
        if ntpath.normcase(image.value) != ntpath.normcase(expected_exe):
            raise RuntimeError('App discovery unavailable')
        info = Basic()
        if native.NtQueryInformationProcess(handle, 0, ctypes.byref(info), ctypes.sizeof(info), None):
            raise RuntimeError('App discovery unavailable')
        parameters = int.from_bytes(read(info.peb + 0x20, 8), 'little')
        address = int.from_bytes(read(parameters + 0x80, 8), 'little') if parameters else 0
        if not address:
            raise RuntimeError('App discovery unavailable')
        raw = b''
        for offset in range(0, 1 << 20, 4096):
            raw += read(address + offset, 4096)
            text = raw.decode('utf-16le', errors='ignore')
            if '\0\0' in text:
                break
        else:
            raise RuntimeError('App discovery unavailable')
        environment = {}
        for entry in text.split('\0\0', 1)[0].split('\0'):
            if '=' in entry and not entry.startswith('='):
                key, value = entry.split('=', 1)
                if key in allowed:
                    environment[key] = value
        return environment
    finally:
        kernel.CloseHandle(handle)


def discover(settings, allowed):
    executable = resolve_executable(settings)
    profile = settings.get('appUserData') or ''
    records = powershell("[Console]::OutputEncoding=[Text.Encoding]::UTF8; Get-CimInstance Win32_Process -Filter \"Name='ChatGPT.exe'\" | "
                         "Where-Object { $_.CommandLine -notmatch '\\s--type=' } | "
                         "Select-Object ProcessId,ExecutablePath | ConvertTo-Json -Compress")
    records = records if isinstance(records, list) else [records]
    matches = []
    for record in records:
        if not isinstance(record, dict) or ntpath.normcase(record.get('ExecutablePath') or '') != ntpath.normcase(executable):
            continue
        pid = record.get('ProcessId')
        if type(pid) is not int or pid <= 0:
            continue
        try:
            environment = read_environment(pid, executable, allowed)
        except (OSError, RuntimeError):
            continue
        if ntpath.normcase(environment.get('CODEX_ELECTRON_USER_DATA_PATH', '')) != ntpath.normcase(profile):
            continue
        matches.append({'pid': pid, 'environment': environment, 'executable': executable,
                        'resources': str(Path(executable).parent / 'resources')})
    return matches[0] if len(matches) == 1 else None
