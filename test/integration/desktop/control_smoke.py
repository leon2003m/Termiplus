"""Non-root GUI smoke test; use ONLY a fresh disposable desktop/tools container.

Exercises Dolphin navigation, a loopback Firefox form, Kate typing/paste/save,
window metadata, screenshots and optional OCR. No user browser profiles touched.
"""
import http.server
import os
import pathlib
import subprocess
import tempfile
import threading
import time
from urllib.parse import parse_qs, urlparse

assert os.getuid() == 1000
for attempt in range(100):
    for process in pathlib.Path('/proc').glob('[0-9]*'):
        try:
            if process.stat().st_uid != os.getuid() or (process / 'comm').read_text().strip() != 'xfce4-session':
                continue
            env = dict(item.split('=', 1) for item in (process / 'environ').read_bytes().decode().split('\0') if '=' in item)
            os.environ.update({key: value for key, value in env.items() if key in ('DISPLAY', 'DBUS_SESSION_BUS_ADDRESS', 'XAUTHORITY')})
            break
        except OSError:
            continue
    if os.environ.get('DBUS_SESSION_BUS_ADDRESS'):
        break
    time.sleep(.2)
assert os.environ.get('DISPLAY') and os.environ.get('DBUS_SESSION_BUS_ADDRESS')


def run(*args, **kwargs):
    return subprocess.check_output(args, timeout=15, text=True, stderr=subprocess.DEVNULL, **kwargs).strip()


def launch(*args):
    return subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def window(name):
    for _ in range(100):
        try:
            return run('xdotool', 'search', '--onlyvisible', '--class', name).splitlines()[-1]
        except subprocess.CalledProcessError:
            time.sleep(.1)
    raise AssertionError('no visible ' + name)


def focus(win):
    run('wmctrl', '-ia', hex(int(win)))
    time.sleep(.3)


def key(*keys):
    run('xdotool', 'key', '--clearmodifiers', *keys)
    time.sleep(.3)


def type_text(text):
    run('xdotool', 'type', '--clearmodifiers', '--delay', '1', text)


folder = pathlib.Path.home() / 'Documents'
folder.mkdir(exist_ok=True)
output = folder / ('codex-gui-smoke-' + str(os.getpid()) + '.txt')
assert not output.exists()
launch('dolphin', '--new-window', str(pathlib.Path.home()))
dolphin = window('dolphin')
focus(dolphin)
key('ctrl+l')
type_text(str(folder))
key('Return')
time.sleep(.5)
assert 'Documents' in run('xdotool', 'getwindowname', dolphin)
print('PASS Dolphin Documents', flush=True)

launch('kate', '--startanon', '-n', str(output))
kate = window('kate')
time.sleep(.5)
focus(kate)
type_text('keyboard test')
key('ctrl+a')
text = 'Clipboard test with Unicode αβ\n'
subprocess.run(['xclip', '-selection', 'clipboard'], input=text, text=True, check=True, timeout=5)
assert run('xclip', '-selection', 'clipboard', '-o') == text.strip()
key('ctrl+v')
key('ctrl+s')
for _ in range(30):
    if output.exists() and output.read_text() == text:
        break
    time.sleep(.1)
assert output.read_text() == text
print('PASS Kate keyboard/clipboard/save', flush=True)

received = []


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        received.append(self.path)
        self.send_response(200)
        self.send_header('Content-Type', 'text/html; charset=utf-8')
        self.end_headers()
        self.wfile.write(b'<title>Codex local form</title><form><label>Name <input name="name" autofocus></label><button>Save</button></form>')

    def log_message(self, *args):
        pass


server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
url = 'http://127.0.0.1:' + str(server.server_port) + '/'
profile = pathlib.Path(tempfile.mkdtemp(prefix='codex-gui-firefox-'))
(profile / 'user.js').write_text('user_pref("accessibility.force_disabled", -1);\n')
firefox_process = launch('firefox-esr', '--no-remote', '--profile', str(profile), url)
try:
    firefox = window('firefox')
    time.sleep(2)
    focus(firefox)
    key('ctrl+l')
    type_text(url)
    key('Return')
    time.sleep(1)
    type_text('pixel-filled')
    key('Return')
    for _ in range(30):
        if any(parse_qs(urlparse(path).query).get('name') == ['pixel-filled'] for path in received):
            break
        time.sleep(.1)
    assert any(parse_qs(urlparse(path).query).get('name') == ['pixel-filled'] for path in received)
    print('PASS Firefox local form', flush=True)
    for name, win in [('dolphin', dolphin), ('kate', kate), ('firefox', firefox)]:
        focus(win)
        screenshot = '/tmp/codex-control-' + name + '.png'
        run('maim', screenshot)
        print(name, run('xprop', '-id', win, 'WM_CLASS'), pathlib.Path(screenshot).stat().st_size, 'PNG bytes', flush=True)
        start = time.monotonic()
        recognized = run('tesseract', screenshot, 'stdout', '-l', 'eng+deu')
        print('OCR', name, round(time.monotonic() - start, 3), 'seconds', repr(recognized[:300]), flush=True)
finally:
    firefox_process.terminate()
    server.shutdown()
    server.server_close()
