// Drives the panel of the running preview (preview-up.sh) in headless chromium
// against the REAL backend, through the Caddy edge. Env: PANEL_URL (with ?k=),
// optional SHOT (screenshot path). Restart/Share/Back-up act for real.
import { chromium } from "playwright";
import assert from "node:assert/strict";

const panelUrl = process.env.PANEL_URL;
if (!panelUrl) { console.error("PANEL_URL is required"); process.exit(2); }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(what, fn, timeout = 60000) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    try { const v = await fn(); if (v) return v; } catch (e) { last = e; }
    await sleep(100);
  }
  throw new Error("timed out waiting for " + what + (last ? ": " + last.message : ""));
}

const browser = await chromium.launch({ args: ["--host-resolver-rules=MAP *.test 127.0.0.1"] });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 900, height: 900 } });
const page = await ctx.newPage();
await page.goto(panelUrl);
const panel = page.frameLocator("#panel");
const calls = () => page.evaluate(() => window.__mcpCalls.slice());
const status = () => panel.locator("#status").textContent();
const isErr = async () => (await panel.locator("#status").getAttribute("class")) === "err";

async function act(scope, label, destructive) {
  const before = (await calls()).length;
  await scope.getByRole("button", { name: label, exact: true }).click();
  if (destructive) await scope.getByRole("button", { name: "Confirm " + label.toLowerCase(), exact: true }).click();
  await until("status after " + label, async () => { const t = await status(); return t && t !== "Working…" && t !== "Loading…"; });
  await sleep(500);
  return (await calls()).slice(before);
}

const results = [];
async function test(name, fn) {
  try { await fn(); results.push(true); console.log("  ok   " + name); }
  catch (e) { results.push(false); console.log("  FAIL " + name + "\n       " + String(e.message).split("\n").join("\n       ")); }
}

await until("panel data", async () => (await panel.locator("#sessions .row").count()) > 0);
if (process.env.SHOT) await page.screenshot({ path: process.env.SHOT });

let shareUrl = "";
await test("panel shows the real sessions, workspaces and homes", async () => {
  assert.match(await panel.locator("#sessions").innerText(), /Persistent desktop/);
  assert.match(await panel.locator("#workspaces").innerText(), /Temporary terminal/);
  assert.match(await panel.locator("#backups").innerText(), /Persistent terminal|Persistent desktop/);
  assert.equal(await isErr(), false, await status());
});
await test("Back up now creates a snapshot in one call", async () => {
  const head = panel.locator("#backups .row").first();
  if ((await head.getByRole("button", { name: "Back up now", exact: true }).count()) === 0) {
    // The home is at its snapshot limit (earlier runs fill it; the server never
    // prunes over MCP). The panel must say so instead of offering the button.
    assert.match(await head.innerText(), /full: free space in the console/);
    console.log("       (skipped: home at snapshot limit)");
    return;
  }
  const rows = await panel.locator("#backups .row").count();
  const made = await act(head, "Back up now", false);
  assert.deepEqual(made.map((c) => c.name), ["manage_snapshots"]);
  assert.equal(await isErr(), false, await status());
  assert.ok((await panel.locator("#backups .row").count()) > rows, "snapshot row missing: " + await status());
  assert.match(await status(), /Backup created/);
});
await test("Share screen returns a stream.test link", async () => {
  const row = panel.locator("#sessions .row", { hasText: "Persistent desktop" }).first();
  const made = await act(row, "Share screen (1 h)", true);
  assert.deepEqual(made.map((c) => c.name), ["share_desktop"]);
  assert.equal(await isErr(), false, await status());
  assert.equal(await panel.locator("#shares .row").count(), 1);
  assert.match(await status(), /^Shared /);
  // the URL is in the tool result, not on screen: read it back through the host
  shareUrl = await page.evaluate(async () => {
    const r = await fetch("/mcp", { method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify({ jsonrpc: "2.0", id: 9, method: "tools/call", params: { name: "share_desktop", arguments: { action: "list" } } }) });
    return (await r.json()).result.content[0].text;
  });
  assert.match(shareUrl, /share_id/);
});
await test("a signed-in viewer gets screen pixels from the share", async () => {
  // create a fresh share to learn its URL (the list never returns tokens)
  const url = await page.evaluate(async () => {
    const r = await fetch("/mcp", { method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify({ jsonrpc: "2.0", id: 10, method: "tools/call", params: { name: "share_desktop", arguments: { action: "create", session_id: (JSON.parse((await (await fetch("/mcp", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ jsonrpc: "2.0", id: 11, method: "tools/call", params: { name: "list_sessions", arguments: {} } }) })).json()).result.content[0].text).find((s) => s.desktop).session_id), minutes: 10, _confirmed: true } } }) });
    return JSON.parse((await r.json()).result.content[0].text).url;
  });
  assert.match(url, /^https:\/\/stream\.test:8443\/share\/[0-9a-f]{64}\/$/);
  const viewer = await ctx.newPage();
  await viewer.goto(url);
  await viewer.waitForSelector("#username", { timeout: 30000 });
  await viewer.fill("#username", "bob");
  await viewer.fill("#password", "bob-pw-a2e");
  await viewer.click("#kc-login");
  await viewer.waitForURL(/stream\.test/, { timeout: 30000 });
  const resp = await viewer.goto(url + "screen.png");
  assert.equal(resp.status(), 200);
  assert.match(resp.headers()["content-type"] || "", /image\/png/);
  const body = await resp.body();
  assert.ok(body.length > 2000, "tiny screenshot: " + body.length);
  console.log("       share screen.png: " + body.length + " bytes");
});
await test("Revoke removes the share links", async () => {
  while ((await panel.locator("#shares .row").count()) > 0) {
    await panel.locator("#shares .row").first().getByRole("button", { name: "Revoke", exact: true }).click();
    await panel.locator("#shares .row").first().getByRole("button", { name: "Confirm revoke", exact: true }).click();
    await sleep(1500);
  }
  assert.equal(await isErr(), false, await status());
});
await test("Restart recreates the desktop container, session stays", async () => {
  const row = panel.locator("#sessions .row", { hasText: "Persistent desktop" }).first();
  const made = await act(row, "Restart", true);
  assert.deepEqual(made.map((c) => c.name), ["reset_workspace"]);
  assert.equal(await isErr(), false, await status());
  assert.match(await status(), /^Restarted /);
  assert.equal(await row.getByRole("button", { name: "Stop", exact: true }).count(), 0, "Stop must be gone");
});
await test("status never left on Working…", async () => assert.notEqual(await status(), "Working…"));

if (process.env.SHOT_AFTER) await page.screenshot({ path: process.env.SHOT_AFTER });
await browser.close();
const failed = results.filter((r) => !r).length;
console.log(`\n${results.length - failed}/${results.length} passed`);
process.exit(failed ? 1 : 0);
