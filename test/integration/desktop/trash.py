"""Run via docker exec -i CONTAINER python3 - < this-file in a fresh test desktop.
Only touches a disposable file in the test user's Desktop and trash.
"""
import ctypes
import ctypes.util
import os
import pathlib
import re
import subprocess
import time
from PIL import Image

assert os.getuid() == 1000
for process in pathlib.Path('/proc').glob('[0-9]*'):
    try:
        if (process / 'comm').read_text().strip() == 'xfce4-session':
            env = dict(s.split('=', 1) for s in (process / 'environ').read_bytes().decode().split('\0') if '=' in s)
            os.environ.update({k: v for k, v in env.items() if k in ('DISPLAY', 'DBUS_SESSION_BUS_ADDRESS', 'XAUTHORITY')})
            break
    except OSError:
        pass
assert os.environ.get('DBUS_SESSION_BUS_ADDRESS')


def call(method, *args):
    return subprocess.check_output(['gdbus', 'call', '--session', '--dest', 'org.xfce.FileManager',
        '--object-path', '/org/xfce/FileManager', '--method', 'org.xfce.Trash.' + method, *args], text=True, timeout=15).strip()


subprocess.run(['xfconf-query', '-c', 'xfce4-desktop', '-p', '/desktop-icons/file-icons/show-trash',
    '--create', '-t', 'bool', '-s', 'true'], check=True)
assert call('QueryTrash') == '(false,)', 'use a fresh disposable desktop'
file = pathlib.Path.home() / 'Desktop/codex-trash-check.txt'
file.write_text('Disposable desktop trash test\n')
call('MoveToTrash', str([str(file)]), os.environ['DISPLAY'], '')
time.sleep(1)
assert not file.exists()
assert call('QueryTrash') == '(true,)'
assert (pathlib.Path.home() / '.local/share/Trash/files' / file.name).exists()
subprocess.run(['maim', '/tmp/trash-full.png'], check=True)
print('PASS: desktop file trashed, full trash state and screenshot', flush=True)

# Exercise the real Empty Trash confirmation, using the existing XTest library.
# The test image uses the English UI; confirm the centered dialog's right button.
command = ['gdbus', 'call', '--session', '--dest', 'org.xfce.FileManager', '--object-path',
    '/org/xfce/FileManager', '--method', 'org.xfce.Trash.EmptyTrash', os.environ['DISPLAY'], '']
empty = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
time.sleep(1)
subprocess.run(['maim', '/tmp/trash-confirm.png'], check=True)
width, height = Image.open('/tmp/trash-confirm.png').size
x11 = ctypes.CDLL(ctypes.util.find_library('X11'))
xtest = ctypes.CDLL(ctypes.util.find_library('Xtst'))
x11.XOpenDisplay.argtypes = [ctypes.c_char_p]
x11.XOpenDisplay.restype = ctypes.c_void_p
x11.XFlush.argtypes = [ctypes.c_void_p]
x11.XCloseDisplay.argtypes = [ctypes.c_void_p]
xtest.XTestFakeMotionEvent.argtypes = [ctypes.c_void_p, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_ulong]
xtest.XTestFakeButtonEvent.argtypes = [ctypes.c_void_p, ctypes.c_uint, ctypes.c_int, ctypes.c_ulong]
display = x11.XOpenDisplay(os.environ['DISPLAY'].encode())
assert display
xtest.XTestFakeMotionEvent(display, -1, width // 2 + 148, height // 2 + 52, 0)
xtest.XTestFakeButtonEvent(display, 1, 1, 0)
xtest.XTestFakeButtonEvent(display, 1, 0, 0)
x11.XFlush(display)
x11.XCloseDisplay(display)
stdout, stderr = empty.communicate(timeout=15)
assert empty.returncode == 0, stderr.decode()
time.sleep(1)
assert call('QueryTrash') == '(false,)'
assert not list((pathlib.Path.home() / '.local/share/Trash/files').iterdir())
subprocess.run(['maim', '/tmp/trash-empty.png'], check=True)
print('PASS: confirmed Empty Trash, empty state and screenshot', flush=True)

subprocess.run(['gdbus', 'call', '--session', '--dest', 'org.freedesktop.FileManager1',
    '--object-path', '/org/freedesktop/FileManager1', '--method', 'org.freedesktop.FileManager1.ShowFolders',
    "['file:///home/user']", ''], check=True, timeout=15)
owner = subprocess.check_output(['gdbus', 'call', '--session', '--dest', 'org.freedesktop.DBus',
    '--object-path', '/org/freedesktop/DBus', '--method', 'org.freedesktop.DBus.GetConnectionUnixProcessID',
    'org.freedesktop.FileManager1'], text=True, timeout=15)
pid = re.search(r'uint32 (\d+)', owner).group(1)
assert pathlib.Path('/proc', pid, 'comm').read_text().strip() == 'dolphin'
subprocess.run(['exo-open', '--launch', 'FileManager', '/home/user'], check=True, timeout=15)
print('PASS: ShowFolders service belongs to Dolphin and preferred folder opener works', flush=True)
