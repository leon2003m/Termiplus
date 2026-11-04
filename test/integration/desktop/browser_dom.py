"""Test-only loopback WebDriver/BiDi smoke; /tmp/geckodriver must be provided.
Run only in a disposable non-root desktop container. Uses a new Firefox profile.
"""
import http.server
import json
import os
import subprocess
import threading
import time
import urllib.request

assert os.getuid() == 1000
os.environ['DISPLAY'] = ':1'


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-Type', 'text/html')
        self.end_headers()
        self.wfile.write(b'<title>Codex DOM test</title><form><label>Name <input name="name"></label><button>Save</button></form>')

    def log_message(self, *args):
        pass


server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
url = 'http://127.0.0.1:' + str(server.server_port) + '/'


def request(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request('http://127.0.0.1:4444' + path, data=data, method=method,
                                 headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=30) as response:
        result = json.load(response)['value']
    if isinstance(result, dict) and 'error' in result:
        raise AssertionError(result)
    return result


with open('/tmp/codex-geckodriver.log', 'w') as log:
    driver = subprocess.Popen(['/tmp/geckodriver', '--host', '127.0.0.1', '--port', '4444',
                               '--marionette-host', '127.0.0.1'], stdout=log, stderr=subprocess.STDOUT)
    base = None
    try:
        for _ in range(100):
            try:
                request('GET', '/status')
                break
            except OSError:
                time.sleep(.1)
        session = request('POST', '/session', {'capabilities': {'alwaysMatch': {
            'browserName': 'firefox', 'webSocketUrl': True,
            'moz:firefoxOptions': {'binary': '/usr/bin/firefox-esr',
                                   'prefs': {'accessibility.force_disabled': -1}},
        }}})
        base = '/session/' + session['sessionId']
        bidi = session['capabilities']['webSocketUrl']
        assert bidi.startswith('ws://127.0.0.1:'), bidi
        print('BiDi negotiated on loopback (messages not tested)', flush=True)
        for attempt in range(3):
            request('POST', base + '/url', {'url': url})
            element = request('POST', base + '/element', {'using': 'css selector', 'value': 'input[name=name]'})
            eid = element['element-6066-11e4-a52e-4f735466cecf']
            value = 'dom-filled-' + str(attempt)
            request('POST', base + '/element/' + eid + '/value', {'text': value})
            button = request('POST', base + '/element', {'using': 'css selector', 'value': 'button'})
            request('POST', base + '/element/' + button['element-6066-11e4-a52e-4f735466cecf'] + '/click', {})
            assert 'name=' + value in request('GET', base + '/url')
            print('PASS DOM local form', attempt + 1, flush=True)
    finally:
        if base:
            request('DELETE', base)
        driver.terminate()
        driver.wait(timeout=10)
        server.shutdown()
        server.server_close()
