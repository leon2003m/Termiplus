#!/opt/termiplus/playwright/bin/python3
"""Drive Chromium on the desktop for a model, one command per call:

  tp-browser open URL        open URL (starts the browser if needed)
  tp-browser snapshot        the page's interactive elements with refs
  tp-browser click REF       click element REF from the last snapshot
  tp-browser fill REF TEXT   replace the text of input REF
  tp-browser press KEY       press a key (Enter, Tab, Escape, ...)
  tp-browser text            the page's visible text
  tp-browser screenshot      save a screenshot, print its path
  tp-browser tabs | tab N    list tabs, switch to tab N
  tp-browser back | close

The browser runs on the desktop, so the user sees every step and can take
over (logins, captchas, payments). Each command that acts prints a new
snapshot so the model can check the result.
"""

import json
import os
import subprocess
import sys
import time
import urllib.request

from playwright.sync_api import sync_playwright

PORT = 9222
CDP = f"http://127.0.0.1:{PORT}"
CACHE = os.path.join(os.path.expanduser("~"), ".cache", "termiplus")
MARKER = os.path.join(CACHE, "browser.json")
PROFILE = os.path.join(os.path.expanduser("~"), ".config", "tp-browser")

# Tags interactive, visible elements with data-tp-ref and returns them.
SNAPSHOT_JS = r"""
() => {
  const sel = 'a[href],button,input,select,textarea,summary,[role=button],[role=link],' +
    '[role=checkbox],[role=radio],[role=tab],[role=menuitem],[role=option],[role=switch],' +
    '[role=combobox],[role=textbox],[contenteditable=""],[contenteditable=true],[onclick]';
  document.querySelectorAll('[data-tp-ref]').forEach(e => e.removeAttribute('data-tp-ref'));
  const out = [];
  let n = 0;
  for (const el of document.querySelectorAll(sel)) {
    const r = el.getBoundingClientRect();
    const st = getComputedStyle(el);
    if (r.width < 2 || r.height < 2 || st.visibility === 'hidden' || st.display === 'none') continue;
    if (r.bottom < 0 || r.top > innerHeight * 3) continue;
    if (el.disabled) continue;
    const ref = 'e' + (++n);
    el.setAttribute('data-tp-ref', ref);
    const role = el.getAttribute('role') || el.tagName.toLowerCase() +
      (el.type && el.tagName === 'INPUT' ? ':' + el.type : '');
    let name = el.getAttribute('aria-label') || el.innerText || el.value ||
      el.getAttribute('placeholder') || el.getAttribute('title') || el.getAttribute('alt') ||
      (el.labels && el.labels[0] && el.labels[0].innerText) || el.getAttribute('name') || '';
    name = name.replace(/\s+/g, ' ').trim().slice(0, 80);
    const extra = el.type === 'checkbox' || el.type === 'radio' ? (el.checked ? ' [checked]' : ' [unchecked]') : '';
    out.push(`[${ref}] ${role}: ${name}${extra}${r.top > innerHeight ? ' (below)' : ''}`);
    if (n >= 300) break;
  }
  return out;
}
"""


def die(msg, code=1):
    print(msg, file=sys.stderr)
    sys.exit(code)


def cdp_up():
    try:
        with urllib.request.urlopen(CDP + "/json/version", timeout=1) as r:
            return r.status == 200
    except OSError:
        return False


def start_browser():
    env = dict(os.environ)
    env.setdefault("DISPLAY", ":1")
    # The desktop session's bus (written by its xstartup), for accessibility.
    if "DBUS_SESSION_BUS_ADDRESS" not in env:
        try:
            with open(os.path.join(CACHE, "dbus")) as f:
                env["DBUS_SESSION_BUS_ADDRESS"] = f.read().strip()
        except OSError:
            pass
    args = [
        "/usr/bin/chromium",
        f"--remote-debugging-port={PORT}",
        "--remote-debugging-address=127.0.0.1",
        f"--user-data-dir={PROFILE}",
        "--force-renderer-accessibility",
        "--no-first-run",
        "--no-default-browser-check",
        "--password-store=basic",
    ]
    args.append("about:blank")
    os.makedirs(CACHE, exist_ok=True)
    log = open(os.path.join(CACHE, "browser.log"), "ab")
    subprocess.Popen(args, env=env, stdout=log, stderr=log, stdin=subprocess.DEVNULL,
                     start_new_session=True)
    for _ in range(60):
        if cdp_up():
            break
        time.sleep(0.25)
    else:
        die("chromium did not start; see ~/.cache/termiplus/browser.log")
    with open(MARKER, "w") as f:
        json.dump({"port": PORT, "started": int(time.time())}, f)


def load_marker():
    try:
        with open(MARKER) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def save_tab(i):
    m = load_marker()
    m["tab"] = i
    with open(MARKER, "w") as f:
        json.dump(m, f)


def pages(browser):
    return [p for c in browser.contexts for p in c.pages]


def current(browser):
    ps = pages(browser)
    if not ps:
        return browser.contexts[0].new_page()
    i = load_marker().get("tab", len(ps) - 1)
    return ps[i] if 0 <= i < len(ps) else ps[-1]


def snapshot(page):
    try:
        page.wait_for_load_state("domcontentloaded", timeout=10000)
    except Exception:  # noqa: BLE001 - still show what is there
        pass
    print(f"url: {page.url}")
    print(f"title: {page.title()}")
    for line in page.evaluate(SNAPSHOT_JS):
        print(line)


def by_ref(page, ref):
    loc = page.locator(f'[data-tp-ref="{ref}"]')
    if loc.count() != 1:
        die(f"no element {ref}; run tp-browser snapshot again")
    return loc


def settle(page):
    try:
        page.wait_for_load_state("domcontentloaded", timeout=10000)
    except Exception:  # noqa: BLE001
        pass
    time.sleep(0.5)


def main():
    argv = sys.argv[1:]
    if not argv or argv[0] in ("-h", "--help"):
        print(__doc__)
        return
    op, args = argv[0], argv[1:]
    if op == "close":
        if cdp_up():
            with sync_playwright() as pw:
                pw.chromium.connect_over_cdp(CDP).close()
        try:
            os.unlink(MARKER)
        except OSError:
            pass
        print("browser closed")
        return
    if not cdp_up():
        start_browser()
    with sync_playwright() as pw:
        browser = pw.chromium.connect_over_cdp(CDP)
        page = current(browser)
        if op == "open" and len(args) == 1:
            url = args[0] if "://" in args[0] or args[0].startswith("about:") else "https://" + args[0]
            page.goto(url, wait_until="domcontentloaded", timeout=30000)
            page.bring_to_front()
            snapshot(page)
        elif op == "snapshot" and not args:
            snapshot(page)
        elif op == "click" and len(args) == 1:
            by_ref(page, args[0]).click(timeout=10000)
            settle(page)
            snapshot(current(browser))
        elif op == "fill" and len(args) >= 2:
            by_ref(page, args[0]).fill(" ".join(args[1:]), timeout=10000)
            snapshot(page)
        elif op == "press" and len(args) == 1:
            page.keyboard.press(args[0])
            settle(page)
            snapshot(page)
        elif op == "text" and not args:
            print(page.inner_text("body")[:20000])
        elif op == "screenshot" and not args:
            os.makedirs(os.path.join(CACHE, "screens"), exist_ok=True)
            path = os.path.join(CACHE, "screens", "browser-" + time.strftime("%Y%m%d-%H%M%S") + ".png")
            page.screenshot(path=path)
            print(path)
        elif op == "back" and not args:
            page.go_back(wait_until="domcontentloaded")
            snapshot(page)
        elif op == "tabs" and not args:
            for i, p in enumerate(pages(browser)):
                print(f"{i}: {p.title()[:60]} - {p.url}")
        elif op == "tab" and len(args) == 1:
            i = int(args[0])
            ps = pages(browser)
            if not 0 <= i < len(ps):
                die("no such tab")
            save_tab(i)
            ps[i].bring_to_front()
            snapshot(ps[i])
        else:
            die(__doc__, 2)
        # Disconnect only; the browser keeps running on the desktop.


if __name__ == "__main__":
    main()
