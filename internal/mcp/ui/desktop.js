"use strict";
// desktop.html?session=<id>, on the console's origin: a desktop that is
// stopped or still starting links here from its own origin (which has no
// console API). Starting asks first and goes through the console API like the
// console's Start button, then opens the desktop again.

const $ = (id) => document.getElementById(id);
const session = new URLSearchParams(location.search).get("session") || "";

function safeURL(u) {
  try {
    const parsed = new URL(u, location.href);
    return parsed.protocol === "https:" || parsed.protocol === "http:" ? parsed.href : null;
  } catch (e) {
    return null;
  }
}

function say(text, isError) {
  $("status").textContent = text;
  $("status").className = isError ? "error" : "";
}

async function api(path, options) {
  const opts = Object.assign({ credentials: "same-origin", headers: {} }, options || {});
  if (opts.method && opts.method !== "GET") {
    opts.headers["X-Termiplus-UI"] = "1";
    opts.headers["Content-Type"] = "application/json";
  }
  const resp = await fetch("api/" + path, opts);
  let body = null;
  try { body = await resp.json(); } catch (e) { body = null; }
  if (!resp.ok) throw new Error((body && body.error) || resp.status + " " + resp.statusText);
  return body;
}

// Start (or, if running, look up) the session until the gateway reports it
// ready, then open the desktop on its own origin; KasmVNC needs a few seconds
// more, so wait briefly first. Starting is idempotent, so a request lost to a
// timeout or a network change is simply sent again.
async function startAndWait(s) {
  say("Starting the desktop …");
  const body = JSON.stringify({ workspace: s.workspace, persistent: s.persistent });
  const deadline = Date.now() + 3 * 60 * 1000;
  while (Date.now() < deadline) {
    try {
      const started = await api("sessions", { method: "POST", body: body });
      const target = started && started.stream_url && safeURL(started.stream_url);
      if (target) {
        say("Started. Opening the desktop …");
        await new Promise((r) => setTimeout(r, 4000));
        location.href = target;
        return;
      }
      say("Started, but this server gives no desktop address.", true);
      return;
    } catch (e) {
      if (!(e instanceof TypeError) && !/timeout|not become ready|502|504/i.test(e.message)) {
        say(e.message, true);
        return;
      }
    }
    await new Promise((r) => setTimeout(r, 3000));
  }
  say("The desktop did not come up. Try again, or remove it in the console and start it anew.", true);
}

async function main() {
  let s;
  try {
    s = await api("desktops/" + encodeURIComponent(session));
  } catch (e) {
    say(/no such desktop/.test(e.message)
      ? "This desktop does not exist any more. Start a new one in the console or from Open WebUI."
      : e.message, true);
    return;
  }
  if (s.running) {
    await startAndWait(s);
    return;
  }
  say("This desktop (" + s.workspace + ") is stopped" + (s.persistent ? "; your files are kept." : "."));
  const start = $("start");
  start.hidden = false;
  start.addEventListener("click", async () => {
    start.disabled = true;
    await startAndWait(s);
    start.disabled = false;
  });
}

main();
