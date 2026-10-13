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
    const meta = [s.workspace, s.persistent ? "persistent" : "temporary", s.running ? "running" : "stopped"].join(" · ");
    items.push(el("span", { class: "meta", text: meta }));
    ul.append(el("li", {}, items));
  }
  $("sessions-section").hidden = false;
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
  $("admin-section").hidden = false;
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
    await Promise.all([loadWorkspaces(), loadSessions()]);
  } catch (e) {
    status(e.message, true);
  }
}

main();
