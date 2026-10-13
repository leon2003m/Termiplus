"""Stand-in for the gateway's /stream/ path in desktop.sh: answers every
request with what reached it (path and headers) and accepts WebSocket
upgrades. Standard library only."""

import base64
import hashlib
import http.server
import json


class Echo(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        seen = {"path": self.path, "headers": {k.lower(): v for k, v in self.headers.items()}}
        if self.headers.get("Upgrade", "").lower() == "websocket":
            key = self.headers.get("Sec-WebSocket-Key", "")
            accept = base64.b64encode(
                hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()
            ).decode()
            self.send_response(101)
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
            self.send_header("Sec-WebSocket-Accept", accept)
            self.send_header("X-Echo", json.dumps(seen))
            self.end_headers()
            return
        body = json.dumps(seen).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


http.server.ThreadingHTTPServer(("0.0.0.0", 8080), Echo).serve_forever()
