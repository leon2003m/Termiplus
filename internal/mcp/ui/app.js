"use strict";
// termiplus management UI. Talks to ./api/ (same origin). Every state change
// asks the user first and sends the X-Termiplus-UI header the server requires.

const $ = (id) => document.getElementById(id);

function status(text, isError) {
  const el = $("status");
  el.textContent = text || "";
  el.className = isError ? "error" : "";
}

async function api(path, options) {
  const opts = Object.assign({ credentials: "same-origin", headers: {} }, options || {});
  if (opts.method && opts.method !== "GET") {
    opts.headers["X-Termiplus-UI"] = "1";
    opts.headers["Content-Type"] = "application/json";
  }
  const resp = await fetch("api/" + path, opts);
  const text = await resp.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch (e) { body = null; }
  if (!resp.ok) {
    throw new Error((body && body.error) || resp.status + " " + resp.statusText);
  }
  return body;
}

function el(tag, attrs, children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "text") node.textContent = v;
    else node.setAttribute(k, v);
  }
  for (const c of children || []) node.append(c);
  return node;
}

function safeURL(u) {
  try {
    const parsed = new URL(u, location.href);
    return parsed.protocol === "https:" || parsed.protocol === "http:" ? parsed.href : null;
  } catch (e) {
    return null;
  }
}

async function startWorkspace(ws, button) {
  const what = ws.persistent ? "a persistent session (your home is kept)" : "a temporary session";
  if (!window.confirm("Start " + what + " of \"" + ws.name + "\"?")) return;
  button.disabled = true;
  status("Starting " + ws.name + " …");
  try {
    await api("sessions", { method: "POST", body: JSON.stringify({ workspace: ws.name, persistent: ws.persistent }) });
    status("Started " + ws.name + ".");
    await loadSessions();
  } catch (e) {
    status(e.message, true);
  } finally {
    button.disabled = false;
  }
}

async function removeSession(s, button) {
  const keep = s.persistent ? " Your home volume is kept." : "";
  if (!window.confirm("Remove session " + s.session_id + "?" + keep)) return;
  button.disabled = true;
  try {
    await api("sessions/" + encodeURIComponent(s.session_id), { method: "DELETE" });
    status("Removed " + s.session_id + ".");
    await loadSessions();
  } catch (e) {
    status(e.message, true);
    button.disabled = false;
  }
}

async function loadWorkspaces() {
  const list = await api("workspaces");
  loadBackups(list).catch((e) => status(e.message, true));
  const ul = $("workspaces");
  ul.replaceChildren();
  for (const ws of list) {
    const button = el("button", { class: "primary", type: "button", text: "Start" });
    button.addEventListener("click", () => startWorkspace(ws, button));
    const kind = [ws.desktop ? "desktop" : "terminal", ws.persistent ? "persistent" : "temporary"].join(", ");
    ul.append(el("li", {}, [
      el("span", { class: "name", text: ws.name }),
      button,
      el("span", { class: "meta", text: kind + (ws.description ? " · " + ws.description : "") }),
    ]));
  }
  $("workspaces-section").hidden = false;
}

async function loadSessions() {
  const list = await api("sessions");
  const ul = $("sessions");
  ul.replaceChildren();
  if (list.length === 0) ul.append(el("li", { class: "meta", text: "No sessions." }));
  for (const s of list) {
    const remove = el("button", { class: "danger", type: "button", text: "Remove" });
    remove.addEventListener("click", () => removeSession(s, remove));
    const items = [el("span", { class: "name", text: s.label || s.session_id }), remove];
    const url = s.stream_url && safeURL(s.stream_url);
    if (url) {
      items.splice(1, 0, el("a", { class: "button", href: url, target: "_blank", rel: "noopener noreferrer", text: "Open desktop" }));
    }
    const fwd = s.forward_url && safeURL(s.forward_url);
    if (fwd && s.running) {
      const port = el("input", { class: "port", type: "number", min: "1", max: "65535", placeholder: "port", "aria-label": "Port to open" });
      const open = el("button", { type: "button", text: "Open port" });
      open.addEventListener("click", () => {
        const n = Number(port.value);
        if (!Number.isInteger(n) || n < 1 || n > 65535) {
          status("Enter a port between 1 and 65535.", true);
          return;
        }
        window.open(fwd + n + "/", "_blank", "noopener,noreferrer");
      });
      items.splice(items.length - 1, 0, port, open);
    }
    const reset = el("button", { type: "button", text: "Reset" });
    reset.addEventListener("click", () => resetSession(s, reset));
    items.splice(items.length - 1, 0, reset);
    if (url && sharesEnabled) {
      const share = el("button", { type: "button", text: "Share (view only)" });
      share.addEventListener("click", () => shareDesktop(s, share));
      items.splice(items.length - 1, 0, share);
    }
    const meta = [s.workspace, s.persistent ? "persistent" : "temporary", s.running ? "running" : "stopped"].join(" · ");
    items.push(el("span", { class: "meta", text: meta }));
    ul.append(el("li", {}, items));
  }
  $("sessions-section").hidden = false;
}

async function resetSession(s, button) {
  const what = s.persistent
    ? "Start " + s.session_id + " over from a fresh container? Everything in /home/user is kept; programs installed elsewhere are lost."
    : "Remove the temporary sandbox " + s.session_id + "? The next use starts a fresh one.";
  if (!window.confirm(what)) return;
  button.disabled = true;
  status("Resetting " + s.session_id + " …");
  try {
    await api("sessions/" + encodeURIComponent(s.session_id) + "/reset", { method: "POST" });
    status("Reset " + s.session_id + ".");
    await loadSessions();
  } catch (e) {
    status(e.message, true);
  } finally {
    button.disabled = false;
  }
}

// --- shared desktops ------------------------------------------------------

let sharesEnabled = false;

async function shareDesktop(s, button) {
  const minutes = window.prompt("Share this desktop for how many minutes (5-1440)? Viewers can watch, not type or click.", "60");
  if (minutes === null) return;
  const n = Number(minutes);
  if (!Number.isInteger(n) || n < 5 || n > 1440) {
    status("Enter a number of minutes between 5 and 1440.", true);
    return;
  }
  const forUser = window.prompt("Only for one user? Enter their user name, or leave empty for anyone who can sign in and has the link.", "");
  if (forUser === null) return;
  button.disabled = true;
  try {
    const res = await api("shares", { method: "POST", body: JSON.stringify({ session_id: s.session_id, minutes: n, for_user: forUser.trim() }) });
    const link = safeURL(res.url);
    $("share-link").replaceChildren(
      el("span", { text: "Share link (" + res.who + ", until " + new Date(res.expires).toLocaleString() + "): " }),
      el("input", { class: "secret", type: "text", readonly: "readonly", value: link || "", "aria-label": "Share link" }),
    );
    $("share-link").hidden = false;
    status("Shared " + s.session_id + ". Copy the link below.");
    await loadShares();
  } catch (e) {
    status(e.message, true);
  } finally {
    button.disabled = false;
  }
}

async function loadShares() {
  let res;
  try {
    res = await api("shares");
  } catch (e) {
    sharesEnabled = false;
    $("shares-section").hidden = true;
    return;
  }
  sharesEnabled = true;
  const ul = $("shares");
  ul.replaceChildren();
  if (res.shares.length === 0) ul.append(el("li", { class: "meta", text: "No active share links." }));
  for (const sh of res.shares) {
    const revoke = el("button", { class: "danger", type: "button", text: "Revoke" });
    revoke.addEventListener("click", async () => {
      revoke.disabled = true;
      try {
        await api("shares/" + encodeURIComponent(sh.share_id), { method: "DELETE" });
        status("Revoked the share link.");
        $("share-link").hidden = true;
        await loadShares();
      } catch (e) {
        status(e.message, true);
        revoke.disabled = false;
      }
    });
    const who = sh.for_user ? "only " + sh.for_user : "anyone signed in with the link";
    ul.append(el("li", {}, [
      el("span", { class: "name", text: sh.session_id }),
      revoke,
      el("span", { class: "meta", text: who + " · until " + new Date(sh.expires).toLocaleString() }),
    ]));
  }
  $("shares-section").hidden = false;
}

// --- backups (snapshots and downloads of homes) ------------------------------

function sizeText(n) {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + " GiB";
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MiB";
  return Math.max(1, Math.round(n / 1024)) + " KiB";
}

async function loadBackups(workspaces) {
  const box = $("backups");
  box.replaceChildren();
  const seen = new Set();
  let any = false;
  for (const ws of workspaces.filter((w) => w.persistent)) {
    let res;
    try {
      res = await api("snapshots/" + encodeURIComponent(ws.name));
    } catch (e) {
      continue; // no home yet, or backups not configured
    }
    if (seen.has(res.home)) continue;
    seen.add(res.home);
    any = true;
    const take = el("button", { class: "primary", type: "button", text: "Take snapshot" });
    take.addEventListener("click", async () => {
      take.disabled = true;
      status("Taking a snapshot of " + ws.name + " …");
      try {
        await api("snapshots/" + encodeURIComponent(ws.name), { method: "POST" });
        status("Snapshot taken.");
        await loadBackups(workspaces);
      } catch (e) {
        status(e.message, true);
        take.disabled = false;
      }
    });
    const head = [el("span", { class: "name", text: "Home of " + ws.name }), take];
    const now = res.current_home_download_url && safeURL(res.current_home_download_url);
    if (now) head.splice(1, 0, el("a", { class: "button", href: now, text: "Download home" }));
    head.push(el("span", { class: "meta", text: "keeps the newest " + res.keep + " snapshots" }));
    const ul = el("ul", {});
    for (const sn of res.snapshots) {
      const restore = el("button", { type: "button", text: "Restore" });
      restore.addEventListener("click", async () => {
        if (!window.confirm("Restore the snapshot of " + new Date(sn.created).toLocaleString() + "? The workspace stops, the snapshot's files are written back (overwriting newer versions); files created since then stay.")) return;
        restore.disabled = true;
        status("Restoring …");
        try {
          await api("snapshots/" + encodeURIComponent(ws.name) + "/" + encodeURIComponent(sn.id) + "/restore", { method: "POST" });
          status("Restored. The workspace starts again on its next use.");
          await loadSessions();
        } catch (e) {
          status(e.message, true);
        } finally {
          restore.disabled = false;
        }
      });
      const del = el("button", { class: "danger", type: "button", text: "Delete" });
      del.addEventListener("click", async () => {
        if (!window.confirm("Delete this snapshot?")) return;
        del.disabled = true;
        try {
          await api("snapshots/" + encodeURIComponent(ws.name) + "/" + encodeURIComponent(sn.id), { method: "DELETE" });
          await loadBackups(workspaces);
        } catch (e) {
          status(e.message, true);
          del.disabled = false;
        }
      });
      const items = [el("span", { class: "name", text: new Date(sn.created).toLocaleString() }), restore, del];
      const dl = sn.download_url && safeURL(sn.download_url);
      if (dl) items.splice(1, 0, el("a", { class: "button", href: dl, text: "Download" }));
      items.push(el("span", { class: "meta", text: sizeText(sn.size) }));
      ul.append(el("li", {}, items));
    }
    if (res.snapshots.length === 0) ul.append(el("li", { class: "meta", text: "No snapshots yet." }));
    box.append(el("div", { class: "home" }, [el("div", { class: "home-head" }, head), ul]));
  }
  $("backups-section").hidden = !any;
}

async function loadAdmin() {
  const ent = await api("admin/entitlements");
  $("entitlements").value = JSON.stringify(ent.entitlements, null, 2);
  $("entitlements").readOnly = !ent.editable;
  $("save-entitlements").disabled = !ent.editable;
  try {
    $("netpolicy").value = JSON.stringify(await api("admin/netpolicy"), null, 2);
  } catch (e) {
    $("netpolicy").value = "";
    $("netpolicy").placeholder = e.message;
    $("netpolicy").readOnly = true;
    $("save-netpolicy").disabled = true;
  }
  await loadAudit();
  $("admin-section").hidden = false;
}

async function loadAudit() {
  const body = $("audit").tBodies[0];
  body.replaceChildren();
  let entries;
  try {
    entries = (await api("admin/audit")).entries;
  } catch (e) {
    body.append(el("tr", {}, [el("td", { colspan: "4", text: e.message })]));
    return;
  }
  if (!entries.length) body.append(el("tr", {}, [el("td", { colspan: "4", text: "Nothing recorded yet." })]));
  for (const e of entries) {
    const args = e.args ? " " + JSON.stringify(e.args) : "";
    const result = e.outcome + (e.error ? ": " + e.error : "");
    body.append(el("tr", {}, [
      el("td", { text: e.time.replace("T", " ").replace(/\.\d+Z$|Z$/, "") }),
      el("td", { text: e.actor }),
      el("td", { class: "mono", text: e.tool + args }),
      el("td", { class: e.outcome === "ok" ? "" : "bad", text: result }),
    ]));
  }
}

function saver(button, field, path, what) {
  $(button).addEventListener("click", async () => {
    let body;
    try {
      body = JSON.parse($(field).value);
    } catch (e) {
      status(what + ": not valid JSON (" + e.message + ")", true);
      return;
    }
    if (!window.confirm("Replace the " + what + " for everyone?")) return;
    $(button).disabled = true;
    try {
      await api(path, { method: "PUT", body: JSON.stringify(body) });
      status("Saved the " + what + ".");
      await Promise.all([loadAdmin(), loadWorkspaces().catch(() => {})]);
    } catch (e) {
      status(what + ": " + e.message, true);
    } finally {
      $(button).disabled = false;
    }
  });
}

async function main() {
  saver("save-entitlements", "entitlements", "admin/entitlements", "workspace entitlements");
  saver("save-netpolicy", "netpolicy", "admin/netpolicy", "network policy");
  try {
    const me = await api("me");
    $("roles").textContent = me.roles.length ? "groups: " + me.roles.join(", ") : "";
    if (me.roles.includes("termiplus-admin")) await loadAdmin().catch((e) => status(e.message, true));
    if (!me.linked) {
      status("Your sign-in is not linked to an Open WebUI user yet. Use the termiplus tool in Open WebUI once, then reload.", true);
      return;
    }
    await loadShares();
    await Promise.all([loadWorkspaces(), loadSessions()]);
  } catch (e) {
    status(e.message, true);
  }
}

main();
