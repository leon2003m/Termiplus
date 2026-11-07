// Harness for the MCP App panel: serves the host page and the panel, and
// proxies /mcp to the MCP server with the bearer token added here, so the
// browser never holds it and needs no CORS.
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const panelFile = path.resolve(here, "../../internal/mcp/app/workspaces.html");
const { MCP_URL, MCP_TOKEN, PORT = "0" } = process.env;
if (!MCP_URL || !MCP_TOKEN) { console.error("MCP_URL and MCP_TOKEN are required"); process.exit(2); }

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, "http://x");
  if (req.method === "GET" && (url.pathname === "/" || url.pathname === "/host.html")) {
    res.writeHead(200, { "content-type": "text/html; charset=utf-8" });
    return res.end(fs.readFileSync(path.join(here, "host.html")));
  }
  if (req.method === "GET" && url.pathname === "/panel.html") {
    res.writeHead(200, { "content-type": "text/html; charset=utf-8" });
    return res.end(fs.readFileSync(panelFile));
  }
  if (req.method === "POST" && url.pathname === "/mcp") {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    try {
      const up = await fetch(MCP_URL, {
        method: "POST",
        headers: { "content-type": "application/json", authorization: "Bearer " + MCP_TOKEN },
        body: Buffer.concat(chunks),
      });
      res.writeHead(up.status, { "content-type": "application/json" });
      return res.end(await up.text());
    } catch (e) {
      res.writeHead(502, { "content-type": "text/plain" });
      return res.end("upstream: " + e.message);
    }
  }
  res.writeHead(404);
  res.end();
});
server.listen(Number(PORT), "127.0.0.1", () => console.log("harness listening on " + server.address().port));
