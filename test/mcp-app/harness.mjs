// Harness for the MCP App panel: serves the host page and the panel and
// proxies /mcp to an MCP server, so the browser never holds a credential and
// needs no CORS.
//
// Test mode (default, run.sh): MCP_URL + MCP_TOKEN, a static bearer token.
//
// Live mode (LIVE=1, preview-up.sh): acts as test user alice the way Open
// WebUI does. It takes a Keycloak access token with the password grant and
// calls mcp-auth with the plugin key and the Open WebUI user id. Access is
// gated by PANEL_KEY (?k=... once, then a cookie), because the page can stop
// and reset the user's sessions.
//
//   node harness.mjs                       serve
//   node harness.mjs call TOOL '{json}'    (live) call one tool, print result
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const env = process.env;
const live = env.LIVE === "1";
const panelFile = env.PANEL_FILE || path.resolve(here, "../../internal/mcp/app/workspaces.html");
const { MCP_URL, MCP_TOKEN, PORT = "0", BIND = "127.0.0.1" } = env;
if (!MCP_URL || (!live && !MCP_TOKEN)) { console.error("MCP_URL and MCP_TOKEN are required"); process.exit(2); }

let cached = { token: "", exp: 0 };
async function liveToken() {
  if (cached.token && Date.now() < cached.exp) return cached.token;
  const r = await fetch(env.TOKEN_URL, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "password", client_id: "owui", client_secret: env.OWUI_CLIENT_SECRET,
      username: env.LIVE_USER, password: env.LIVE_PASSWORD, scope: "openid",
    }),
  });
  if (!r.ok) throw new Error("token request failed: " + r.status);
  const j = await r.json();
  cached = { token: j.access_token, exp: Date.now() + Math.max(10, (j.expires_in || 60) - 20) * 1000 };
  return cached.token;
}

async function upstream(body) {
  const headers = { "content-type": "application/json" };
  if (live) {
    headers.authorization = "Bearer " + (await liveToken());
    headers["x-termiplus-owui-key"] = env.PLUGIN_KEY;
    headers["x-termiplus-owui-user"] = env.LIVE_OWUI_USER;
  } else {
    headers.authorization = "Bearer " + MCP_TOKEN;
  }
  return fetch(MCP_URL, { method: "POST", headers, body });
}

if (process.argv[2] === "call") {
  const body = JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call",
    params: { name: process.argv[3], arguments: JSON.parse(process.argv[4] || "{}") } });
  const resp = await upstream(body);
  const raw = await resp.text();
  let d;
  try { d = JSON.parse(raw); } catch { console.log("ERROR HTTP " + resp.status + " (not JSON; a slow create can outlast the proxy's 30 s)"); process.exit(1); }
  console.log(d.error ? "ERROR " + JSON.stringify(d.error) : (d.result?.content?.[0]?.text ?? ""));
  process.exit(d.error || d.result?.isError ? 1 : 0);
}

const cookieKey = "tp_panel";
function authorized(req, url, res) {
  if (!live || !env.PANEL_KEY) return true;
  const cookies = Object.fromEntries((req.headers.cookie || "").split(/;\s*/).map((c) => c.split("=")));
  if (cookies[cookieKey] === env.PANEL_KEY) return true;
  if (url.searchParams.get("k") === env.PANEL_KEY) {
    res.setHeader("set-cookie", `${cookieKey}=${env.PANEL_KEY}; Path=/; HttpOnly; Secure; SameSite=Lax`);
    return true;
  }
  return false;
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, "http://x");
  if (!authorized(req, url, res)) { res.writeHead(403, { "content-type": "text/plain" }); return res.end("forbidden: open the URL with ?k=..."); }
  if (req.method === "GET" && (url.pathname === "/" || url.pathname === "/host.html")) {
    let html = fs.readFileSync(path.join(here, "host.html"), "utf8");
    if (live) html = html.replace("<!--LIVE-->", "<script>window.__live = true;</script>");
    res.writeHead(200, { "content-type": "text/html; charset=utf-8" });
    return res.end(html);
  }
  if (req.method === "GET" && url.pathname === "/panel.html") {
    res.writeHead(200, { "content-type": "text/html; charset=utf-8" });
    return res.end(fs.readFileSync(panelFile));
  }
  if (req.method === "POST" && url.pathname === "/mcp") {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    try {
      const up = await upstream(Buffer.concat(chunks));
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
server.listen(Number(PORT), BIND, () => console.log("harness listening on " + server.address().port));
