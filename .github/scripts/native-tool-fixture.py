"""Local deterministic DOM/event oracle for native tool integration tests."""

import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

HTML = """<!doctype html><meta charset="utf-8"><title>Native fixture PAGE</title>
<style>body{margin:0;background:#e8eff8;font:18px sans-serif}h1{margin:10px}
#probe{position:absolute;left:100px;top:100px;width:180px;height:70px}
#message{position:absolute;left:100px;top:220px;width:350px;height:40px}
#drag{position:absolute;left:100px;top:320px;width:150px;height:90px;background:#3b9}
#scroll{position:absolute;left:600px;top:100px;width:300px;height:300px;overflow:scroll;background:#ddd}
#content{width:1500px;height:1500px;background:linear-gradient(45deg,#aaf,#ffa)}</style>
<h1>Native fixture PAGE</h1><button id="probe">Probe</button>
<input id="message" aria-label="Message"><button id="drag">Drag target</button>
<div id="scroll" aria-label="Scroll area"><div id="content"></div></div>
<script>
const box=document.querySelector('#scroll');box.scrollLeft=400;box.scrollTop=400;
function report(e){fetch('/event',{method:'POST',body:JSON.stringify(e)});}
for(const type of ['mousedown','mouseup','mousemove','dblclick','click','auxclick','keydown','input']){
 document.addEventListener(type,e=>report({type,target:e.target.id,button:e.button,buttons:e.buttons,
  x:e.clientX,y:e.clientY,key:e.key,value:e.target.value}),true);
}
document.addEventListener('contextmenu',e=>e.preventDefault());
document.addEventListener('auxclick',e=>e.preventDefault());
box.addEventListener('scroll',()=>report({type:'scroll',x:box.scrollLeft,y:box.scrollTop}));
report({type:'ready',page:'PAGE'});
requestAnimationFrame(()=>requestAnimationFrame(()=>report({type:'painted',page:'PAGE'})));
</script>"""


class Handler(BaseHTTPRequestHandler):
    events = []

    def log_message(self, *args):
        pass

    def reply(self, data, content_type="application/json"):
        raw = data.encode()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path == "/state":
            self.reply(json.dumps(self.events))
        else:
            page = "B" if self.path.startswith("/b") else "A"
            self.reply(HTML.replace("PAGE", page), "text/html; charset=utf-8")

    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.path == "/reset":
            self.events.clear()
        elif self.path == "/event":
            self.events.append(json.loads(raw))
        self.reply("{}")


HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
