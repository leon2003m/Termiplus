"""Firefox/Marionette console regression harness, stdlib only.
Run inside a disposable image with Firefox and this directory's UI assets at
/tmp/console-ui. HTTP and Marionette bind to loopback only; all identities fake.
"""
import datetime
import base64
import http.server
import json
import pathlib
import socket
import subprocess
import threading
import time
from urllib.parse import urlparse

ROOT = pathlib.Path('/tmp/console-ui')
NOW = datetime.datetime.now(datetime.timezone.utc).isoformat()
EXPIRY = (datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(minutes=8)).isoformat()
workspaces = [dict(name='shell', label='Terminal', persistent=True, idle_seconds=900),
    dict(name='temp', label='Temporary terminal', persistent=False, idle_seconds=900),
    dict(name='docker', label='Terminal with Docker', connection_name='Terminal with Docker', persistent=True, idle_seconds=900),
    dict(name='desktop', label='Desktop', desktop=True, persistent=True)]
sessions = [dict(session_id='terminal',workspace='temp',persistent=False,running=True,
    started_at=NOW,last_activity=NOW,idle_expires_at=EXPIRY),
    dict(session_id='desktop',workspace='desktop',persistent=True,running=True,desktop=True)]
posted = []

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        path = urlparse(self.path).path
        data = None
        if path == '/ui/api/me': data = dict(linked=True,roles=[])
        elif path == '/ui/api/workspaces': data = workspaces
        elif path == '/ui/api/sessions': data = sessions
        elif path == '/ui/api/shares': data = dict(shares=[])
        elif path.endswith('/people'): data = dict(people=[dict(username='fake-person')])
        elif path.startswith('/ui/api/'): data = dict(snapshots=[],keep=3)
        if data is not None:
            self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers()
            self.wfile.write(json.dumps(data).encode());return
        name = path.removeprefix('/ui/') or 'index.html'
        if name not in ('index.html','app.js','app.css'):
            self.send_error(404);return
        self.send_response(200)
        self.send_header('Content-Type', {'html':'text/html','css':'text/css','js':'text/javascript'}[name.split('.')[-1]])
        self.end_headers();self.wfile.write((ROOT / name).read_bytes())

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length',0))))
        posted.append((self.path,body))
        self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers()
        self.wfile.write(json.dumps(dict(url='http://localhost/fake-share',who='fake-person',expires='2099-01-01T00:00:00Z')).encode())

server = http.server.ThreadingHTTPServer(('127.0.0.1',8099),Handler)
threading.Thread(target=server.serve_forever,daemon=True).start()

for dark in (False,True):
    profile = pathlib.Path('/tmp/console-profile-' + str(dark));profile.mkdir(exist_ok=True)
    (profile/'user.js').write_text('user_pref("marionette.port",2828);\nuser_pref("marionette.host","127.0.0.1");\nuser_pref("layout.css.prefers-color-scheme.content-override",'+('0' if dark else '1')+');\n')
    browser = subprocess.Popen(['firefox-esr','--headless','--marionette','--no-remote','--profile',str(profile),'about:blank'],stdout=open('/tmp/console-firefox.log','w'),stderr=subprocess.STDOUT)
    try:
        deadline=time.monotonic()+30
        while True:
            try: sock=socket.create_connection(('127.0.0.1',2828),timeout=1);break
            except OSError:
                if time.monotonic()>deadline: raise
                time.sleep(.2)
        sock.settimeout(20)
        def receive():
            length=b''
            while not length.endswith(b':'):
                part=sock.recv(1)
                if not part: raise EOFError('Marionette closed')
                length+=part
            count=int(length[:-1]);data=b''
            while len(data)<count:
                part=sock.recv(count-len(data))
                if not part: raise EOFError('Marionette closed')
                data+=part
            return json.loads(data)
        receive();sequence=0
        def command(name,args=None):
            global sequence
            sequence+=1;packet=json.dumps([0,sequence,name,args or {}]).encode()
            sock.sendall(str(len(packet)).encode()+b':'+packet)
            response=receive()
            assert response[1]==sequence and response[2] is None,response
            return response[3]
        def js(script):
            result=command('WebDriver:ExecuteScript',dict(script=script,args=[],newSandbox=False,sandbox='default'))
            return result.get('value',result) if isinstance(result,dict) else result
        def wait(script):
            for _ in range(100):
                value=js(script)
                if value:return value
                time.sleep(.05)
            raise AssertionError(script)
        def screenshot(name):
            result=command('WebDriver:TakeScreenshot',dict(id=None,highlights=[],full=True))
            data=result.get('value') if isinstance(result,dict) else result
            pathlib.Path('/tmp/console-'+name+('-dark' if dark else '-light')+'.png').write_bytes(base64.b64decode(data))
        def key(value):
            element=command('WebDriver:FindElement',dict(using='css selector',value='dialog button'))
            element=element.get("value",element)
            uid=next(iter(element.values()))
            command('WebDriver:ElementSendKeys',dict(id=uid,text=value,value=[value]))
        command('WebDriver:NewSession',dict(capabilities={}))
        command('WebDriver:SetWindowRect',dict(width=1100,height=1000))
        command('WebDriver:Navigate',dict(url='http://127.0.0.1:8099/ui/'))
        wait('return document.querySelectorAll("#sessions > li").length === 2')
        assert js('return document.querySelectorAll(".terminal-preview").length')==1
        wait('return document.querySelector(".desktop-preview").textContent === "Preview unavailable"')
        assert js('return document.querySelector(".desktop-preview").getBoundingClientRect().height < 60')
        assert js('return Array.from(document.querySelectorAll(".session-card")).every(c => c.children[0].className === "session-heading" && c.children[2].className === "session-actions")')
        assert js('return Array.from(document.querySelectorAll(".ws-text")).every(c => c.children.length === 3)')
        assert js('return document.querySelector("#status").textContent')==''
        js('window.confirm=window.prompt=()=>{throw new Error("native popup forbidden")}; return true')
        screenshot('overview')
        js('window.scrollTo(0, 120); document.querySelector("#workspaces button").focus({preventScroll:true}); document.querySelector("#workspaces button").click(); return true')
        wait('return !!document.querySelector("dialog[open]")')
        assert js('return document.documentElement.scrollHeight <= window.innerHeight')
        assert js('return getComputedStyle(document.querySelector("dialog"),"::backdrop").position === "fixed"')
        screenshot('confirm')
        key('\ue00c')
        wait('return !document.querySelector("dialog")')
        assert js('return document.activeElement === document.querySelector("#workspaces button")')
        assert js('return !document.documentElement.classList.contains("dialog-open")')
        js('Array.from(document.querySelectorAll("#sessions button")).find(b=>b.textContent==="Share (view only)").click(); return true')
        wait('return !!document.querySelector("#share-duration")')
        assert js('return document.documentElement.scrollHeight <= window.innerHeight')
        js('const d=document.querySelector("#share-duration"); d.value="custom"; d.dispatchEvent(new Event("change")); document.querySelector("#share-custom").value="1"; d.value="60"; d.dispatchEvent(new Event("change")); return true')
        assert js('return document.querySelector("dialog form").checkValidity()')
        js('document.querySelector("#share-audience").value="person"; document.querySelector("#share-audience").dispatchEvent(new Event("change")); document.querySelector("#share-person").value="fake-person"; Array.from(document.querySelectorAll("dialog button")).find(b=>b.textContent==="Find person").click(); return true')
        wait('return !!document.querySelector(".person-results button")')
        js('document.querySelector(".person-results button").click(); return true')
        screenshot('share')
        js('document.querySelector("dialog button[type=submit]").click(); return true')
        wait('return !document.querySelector("#share-link").hidden')
        assert posted[-1][1]['for_user']=='fake-person',posted
        assert posted[-1][1]['minutes']==60
        assert js('return document.querySelector("#share-link").textContent.includes("Copy link")')
        screenshot('link')
        print('PASS Firefox '+('dark' if dark else 'light')+': dialogs, Escape/focus restoration, picker, duration, resulting copy/expiry UI',flush=True)
        command('Marionette:Quit',dict(flags=['eForceQuit']))
    finally:
        browser.terminate()
        try:browser.wait(timeout=5)
        except subprocess.TimeoutExpired:browser.kill();browser.wait()
server.shutdown()
