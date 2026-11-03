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

// Native modal dialogs contain keyboard focus, close on Escape and restore focus.
let activeDialog = null;
function ask(title, content, accept = "Continue", read = () => true) {
  if (activeDialog) return Promise.resolve(null);
  return new Promise((resolve) => {
    const previous = document.activeElement;
    const dialog = el("dialog", { "aria-labelledby": "dialog-title" });
    activeDialog = dialog;
    const form = el("form", {});
    const error = el("p", { class: "error", role: "alert" });
    const cancel = el("button", { type: "button", text: "Cancel", autofocus: "autofocus" });
    const submit = el("button", { class: "primary", type: "submit", text: accept });
    form.append(el("h2", { id: "dialog-title", text: title }), ...content, error,
      el("div", { class: "dialog-actions" }, [cancel, submit]));
    dialog.append(form);
    let value = null;
    cancel.addEventListener("click", () => dialog.close());
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      submit.disabled = true;
      try {
        value = await read();
        dialog.close();
      } catch (e) { error.textContent = e.message; }
      finally { submit.disabled = false; }
    });
    dialog.addEventListener("close", () => {
      activeDialog = null;
      dialog.remove();
      if (previous && previous.isConnected) previous.focus();
      resolve(value);
    }, { once: true });
    document.body.append(dialog);
    dialog.showModal();
  });
}
function confirmAction(message) {
  return ask("Confirm action", [el("p", { text: message })]);
}

async function startWorkspace(ws, button) {
  const title = ws.label || ws.name;
  const what = ws.persistent ? "a persistent session (your home is kept)" : "a temporary session";
  if (!await confirmAction("Start " + what + " of \"" + title + "\"?")) return;
  button.disabled = true;
  status("Starting " + title + " …");
  try {
    await api("sessions", { method: "POST", body: JSON.stringify({ workspace: ws.name, persistent: ws.persistent }) });
    status("Started " + title + ".");
    await loadSessions();
  } catch (e) {
    status(e.message, true);
  } finally {
    button.disabled = false;
  }
}

async function removeSession(s, button) {
  const keep = s.persistent ? " Your home volume is kept." : "";
  if (!await confirmAction("Remove session " + s.session_id + "?" + keep)) return;
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
    const title = ws.label || ws.name;
    const button = el("button", { class: "primary", type: "button", text: "Start", "aria-label": "Start " + title });
    button.addEventListener("click", () => startWorkspace(ws, button));
    const purpose = ws.desktop ? "A Linux desktop for graphical apps." : ws.persistent
      ? "A Linux shell that keeps your home folder between sessions."
      : "A fresh Linux shell with nothing from your home folder. Deleted after " + (ws.idle_seconds ? Math.ceil(ws.idle_seconds / 60) + " minutes" : "the configured idle period") + " without use; useful for trying things or letting an AI run commands without touching your files.";
    const kind = [ws.desktop ? "desktop" : "terminal", ws.persistent ? "keeps your files" : "temporary"].join(", ");
    const text = [el("span", { class: "name", text: title })];
    text.push(el("span", { class: "help", text: purpose }));
    if (!ws.desktop) text.push(el("span", { class: "meta", text: "Open WebUI connection: " + (ws.connection_name || (ws.persistent ? "Terminal" : "Temporary terminal")) }));
    if (ws.description) text.push(el("span", { class: "help", text: ws.description }));
    text.push(el("span", { class: "meta", text: kind + (ws.label && ws.label !== ws.name ? " · " + ws.name : "") }));
    ul.append(el("li", { class: "workspace" }, [el("div", { class: "ws-text" }, text), button]));
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
      const help = el("details", { class: "port-help" }, [el("summary", { text: "What does Open port do?" }),
        el("p", { text: "Open a web app running inside this workspace on that port at a private browser link. Only you can open this port link. Desktop sharing shares the screen, not port access." })]);
      items.splice(items.length - 1, 0, port, open, help);
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
    const card = el("li", { "data-session": s.session_id }, items);
    if (url) card.prepend(desktopPreview(s));
    else card.prepend(terminalPreview(s));
    ul.append(card);
  }
  refreshPreviews();
  $("sessions-section").hidden = false;
}

function terminalPreview(session) {
  const box = el("div", { class: "terminal-preview" });
  box.append(el("strong", { text: session.running ? "Terminal running" : "Offline" }));
  const date = (value) => value ? new Date(value).toLocaleString() : "not recorded";
  box.append(el("span", { text: "Running since: " + (session.running ? date(session.started_at) : "—") }));
  box.append(el("span", { text: "Last activity observed by gateway: " + date(session.last_activity) }));
  if (!session.persistent && session.running) {
    const remaining = session.idle_expires_at ? Math.max(0, Math.ceil((new Date(session.idle_expires_at) - Date.now()) / 60000)) : null;
    box.append(el("span", { text: remaining === null ? (session.last_activity ? "Idle deletion paused while connected" : "Idle deletion time unavailable") : remaining > 0 ? "Idle deletion in about " + remaining + " min" : "Idle deletion pending" }));
  }
  return box;
}

// Images are fetched only for running desktops while this tab is visible.
// A failed/stopped preview loses its src so an old screen is never shown offline.
function desktopPreview(session) {
  const box = el("div", { class: "desktop-preview" });
  const label = el("span", { text: session.running ? "Loading preview…" : "Offline" });
  box.append(label);
  if (session.running) {
    const image = el("img", { alt: "Current desktop screen", hidden: "hidden", "data-preview": encodeURIComponent(session.session_id) });
    image.addEventListener("load", () => { image.hidden = false; label.hidden = true; });
    image.addEventListener("error", () => {
      image.hidden = true;
      image.removeAttribute("src");
      label.textContent = "Preview unavailable";
      label.hidden = false;
    });
    box.append(image);
  }
  return box;
}

function refreshPreviews() {
  if (document.visibilityState !== "visible") return;
  for (const image of document.querySelectorAll("img[data-preview]")) {
    image.src = "api/desktops/" + image.dataset.preview + "/preview?t=" + Math.floor(Date.now() / 30000);
  }
}
let refreshingSessions = false;
async function refreshVisibleSessions() {
  if (document.visibilityState !== "visible" || refreshingSessions || $("sessions-section").hidden) return;
  refreshingSessions = true;
  try {
    const sessions = await api("sessions");
    const current = new Map(sessions.map((s) => [s.session_id, s]));
    for (const card of $("sessions").children) {
      const session = current.get(card.dataset.session) || { running: false };
      const terminal = card.querySelector(".terminal-preview");
      if (terminal) { terminal.replaceWith(terminalPreview(session)); continue; }
      const box = card.querySelector(".desktop-preview");
      if (!box) continue;
      const running = Boolean(box.querySelector("img[data-preview]"));
      if (running !== Boolean(session.running)) box.replaceWith(desktopPreview(session));
    }
    refreshPreviews();
  } catch (e) { status(e.message, true); }
  finally { refreshingSessions = false; }
}
setInterval(refreshVisibleSessions, 30000);
document.addEventListener("visibilitychange", refreshVisibleSessions);

async function resetSession(s, button) {
  const what = s.persistent
    ? "Start " + s.session_id + " over from a fresh container? Everything in /home/user is kept; programs installed elsewhere are lost."
    : "Remove the temporary sandbox " + s.session_id + "? The next use starts a fresh one.";
  if (!await confirmAction(what)) return;
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
  const duration = el("select", { id: "share-duration" }, [
    ...[[15, "15 minutes"], [60, "1 hour"], [480, "8 hours"], [1440, "24 hours"], ["custom", "Custom"]].map(
      ([value, text]) => el("option", { value, text })),
  ]);
  duration.value = "60";
  const custom = el("input", { id: "share-custom", type: "number", min: "5", max: "1440", value: "60" });
  const customLabel = el("label", { for: "share-custom", text: "Custom duration (minutes)", hidden: "hidden" });
  customLabel.append(custom);
  custom.disabled = true;
  duration.addEventListener("change", () => { customLabel.hidden = duration.value !== "custom"; custom.disabled = customLabel.hidden; });
  const audience = el("select", { id: "share-audience" }, [
    el("option", { value: "anyone", text: "Anyone signed in with the link" }),
    el("option", { value: "person", text: "One person" }),
  ]);
  const query = el("input", { id: "share-person", type: "search", autocomplete: "off", maxlength: "128", placeholder: "Exact user name" });
  const find = el("button", { type: "button", text: "Find person" });
  const results = el("div", { class: "person-results", role: "status" });
  const picker = el("div", { hidden: "hidden" }, [el("label", { for: "share-person", text: "Find a person by user name" }), query, find, results]);
  let selected = "";
  query.addEventListener("input", () => { selected = ""; results.replaceChildren(); });
  audience.addEventListener("change", () => { picker.hidden = audience.value !== "person"; });
  find.addEventListener("click", async () => {
    selected = "";
    const searched = query.value.trim();
    find.disabled = true;
    results.textContent = "Searching…";
    try {
      const response = await api("desktops/" + encodeURIComponent(s.session_id) + "/people?q=" + encodeURIComponent(searched));
      if (query.value.trim() !== searched) return;
      results.replaceChildren();
      if (!response.people.length) results.textContent = "No matching person.";
      for (const person of response.people) {
        const choose = el("button", { type: "button", text: person.username, "aria-pressed": "false" });
        choose.addEventListener("click", () => {
          selected = person.username;
          choose.setAttribute("aria-pressed", "true");
          choose.textContent = person.username + " — selected";
        });
        results.append(choose);
      }
    } catch (e) { results.textContent = e.message; }
    finally { find.disabled = false; }
  });
  const choice = await ask("Share desktop", [
    el("p", { text: "Viewers can watch this desktop; they cannot type or click." }),
    el("label", { for: "share-duration", text: "Expires after" }), duration, customLabel,
    el("label", { for: "share-audience", text: "Who can watch" }), audience, picker,
  ], "Create link", () => {
    const minutes = Number(duration.value === "custom" ? custom.value : duration.value);
    if (!Number.isInteger(minutes) || minutes < 5 || minutes > 1440) throw new Error("Choose 5–1440 minutes.");
    if (audience.value === "person" && !selected) throw new Error("Find and select a person first.");
    return { minutes, for_user: audience.value === "person" ? selected : "" };
  });
  if (!choice) return;
  button.disabled = true;
  try {
    const res = await api("shares", { method: "POST", body: JSON.stringify({ session_id: s.session_id, minutes: choice.minutes, for_user: choice.for_user }) });
    const link = safeURL(res.url);
    $("share-link").replaceChildren(
      el("span", { text: "Share link (" + res.who + ", until " + new Date(res.expires).toLocaleString() + "): " }),
      el("input", { class: "secret", type: "text", readonly: "readonly", value: link || "", "aria-label": "Share link" }),
    );
    const copy = el("button", { type: "button", text: "Copy link" });
    copy.addEventListener("click", async () => {
      const input = $("share-link").querySelector("input");
      try { await navigator.clipboard.writeText(input.value); copy.textContent = "Copied"; }
      catch (e) { input.focus(); input.select(); status("Select and copy the link from the field."); }
    });
    $("share-link").append(copy);
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
        if (!await confirmAction("Restore the snapshot of " + new Date(sn.created).toLocaleString() + "? The workspace stops, the snapshot's files are written back (overwriting newer versions); files created since then stay.")) return;
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
        if (!await confirmAction("Delete this snapshot?")) return;
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
    if (!await confirmAction("Replace the " + what + " for everyone?")) return;
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
