// Drives the panel in headless chromium through the host harness. Env:
// HARNESS_URL (e.g. http://127.0.0.1:PORT). Exits non-zero on any failure.
import { chromium } from "playwright";
import assert from "node:assert/strict";

const base = process.env.HARNESS_URL;
if (!base) { console.error("HARNESS_URL is required"); process.exit(2); }

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(what, fn, timeout = 8000) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    try { const v = await fn(); if (v) return v; } catch (e) { last = e; }
    await sleep(50);
  }
  throw new Error("timed out waiting for " + what + (last ? ": " + last.message : ""));
}

const browser = await chromium.launch();
const page = await browser.newPage();
const pageErrors = [];
page.on("pageerror", (e) => pageErrors.push(e.message));
await page.goto(base + "/host.html");
const panel = page.frameLocator("#panel");
const calls = () => page.evaluate(() => window.__mcpCalls.slice());
const statusText = () => panel.locator("#status").textContent();
const statusIsErr = async () => (await panel.locator("#status").getAttribute("class")) === "err";

// act runs one panel action; destructive buttons need a second click on the
// armed "Confirm ..." button. It returns the /mcp calls the action caused.
async function act(scope, label, destructive) {
  const before = (await calls()).length;
  await scope.getByRole("button", { name: label, exact: true }).click();
  if (destructive) await scope.getByRole("button", { name: "Confirm " + label.toLowerCase(), exact: true }).click();
  await until("status to settle after " + label, async () => {
    const t = await statusText();
    return t && t !== "Working…" && t !== "Loading…";
  });
  await sleep(400); // a stray second round trip would show up here
  assert.notEqual(await statusText(), "Working…");
  return (await calls()).slice(before);
}

const results = [];
async function test(name, fn) {
  try { await fn(); results.push([name, true]); console.log("  ok   " + name); }
  catch (e) { results.push([name, false]); console.log("  FAIL " + name + "\n       " + String(e.message).split("\n").join("\n       ")); }
}

await until("initial panel", async () => (await panel.locator("#sessions .row").count()) > 0);

await test("panel renders seeded sessions and workspaces", async () => {
  const sessions = await panel.locator("#sessions").innerText();
  assert.match(sessions, /Scratch term/);
  assert.match(sessions, /My desktop/);
  const ws = await panel.locator("#workspaces").innerText();
  assert.match(ws, /default/);
  assert.match(ws, /desk/);
  assert.match(await panel.locator("#backups").innerText(), /desk/);
  assert.equal(await statusIsErr(), false, "initial status: " + await statusText());
});

await test("Stop: one tools/call, no refresh, row gone, note shown", async () => {
  const row = panel.locator("#sessions .row", { hasText: "Scratch term" });
  const made = await act(row, "Stop", true);
  assert.deepEqual(made, [{ method: "tools/call", name: "destroy_session" }]);
  assert.equal(await panel.locator("#sessions .row", { hasText: "Scratch term" }).count(), 0);
  assert.equal(await panel.locator("#sessions .row", { hasText: "My desktop" }).count(), 1);
  assert.equal(await statusText(), "Stopped Scratch term.");
  assert.equal(await statusIsErr(), false);
});

await test("Share screen: one tools/call, share row appears, clear note", async () => {
  const row = panel.locator("#sessions .row", { hasText: "My desktop" });
  const made = await act(row, "Share screen (1 h)", true);
  assert.deepEqual(made, [{ method: "tools/call", name: "share_desktop" }]);
  const shares = panel.locator("#shares .row");
  assert.equal(await shares.count(), 1);
  assert.match(await shares.first().innerText(), /desk/);
  assert.match(await statusText(), /^Shared My desktop for 60 min\./);
  assert.equal(await statusIsErr(), false);
});

await test("Back up now: one tools/call, snapshot row appears, clear note", async () => {
  const head = panel.locator("#backups .row", { hasText: "desk" }).first();
  assert.equal(await panel.locator("#backups .row").count(), 1);
  const made = await act(head, "Back up now", false);
  assert.deepEqual(made, [{ method: "tools/call", name: "manage_snapshots" }]);
  assert.equal(await panel.locator("#backups .row").count(), 2);
  assert.match(await panel.locator("#backups").innerText(), /1 of 5/);
  assert.equal(await statusText(), "Backup created for desk.");
  assert.equal(await statusIsErr(), false);
});

await test("Revoke: one tools/call, share row gone", async () => {
  const made = await act(panel.locator("#shares .row").first(), "Revoke", true);
  assert.deepEqual(made, [{ method: "tools/call", name: "share_desktop" }]);
  assert.equal(await panel.locator("#shares .row").count(), 0);
  assert.equal(await statusText(), "Revoked the screen-share link.");
});

await test("Reset: one tools/call, session stays, note shown", async () => {
  const row = panel.locator("#sessions .row", { hasText: "My desktop" });
  const made = await act(row, "Reset", true);
  assert.deepEqual(made, [{ method: "tools/call", name: "reset_workspace" }]);
  assert.equal(await panel.locator("#sessions .row", { hasText: "My desktop" }).count(), 1);
  assert.equal(await statusText(), "Reset My desktop.");
});

await test("Refresh button still does a full open_workspaces", async () => {
  const before = (await calls()).length;
  await panel.locator("#refresh").click();
  await until("refresh to finish", async () => (await calls()).length === before + 1 && (await statusText()) === "");
  assert.equal((await calls()).at(-1).name, "open_workspaces");
});

await test("status never left on Working…", async () => {
  assert.notEqual(await statusText(), "Working…");
});

await test("no page errors", async () => assert.deepEqual(pageErrors, []));

await browser.close();
const failed = results.filter((r) => !r[1]);
console.log(`\n${results.length - failed.length}/${results.length} passed`);
process.exit(failed.length ? 1 : 0);
