#!/usr/bin/python3
"""Desktop helpers for models: screenshots, marks, mouse and keyboard, OCR
and accessibility. Installed once and called by its link names:

  tp-screenshot [--marks] [--width N]   save a small screenshot, print its path
  tp-act click X Y | click-mark N | type TEXT | key KEYS | scroll up|down
  tp-ocr [--json]                        words on the screen with positions
  tp-ui tree | click NAME | set NAME TEXT

Coordinates given to tp-act are those of the last screenshot; it scales them
to the screen. Every tp-act prints whether the screen changed afterwards, so
the model can verify its action. Read-only except for the input it sends.
"""

import hashlib
import json
import os
import subprocess
import sys
import tempfile
import time

from PIL import Image, ImageDraw, ImageFont

CACHE = os.path.join(os.path.expanduser("~"), ".cache", "termiplus")
SHOTS = os.path.join(CACHE, "screens")
STATE = os.path.join(CACHE, "screen.json")
DBUS_FILE = os.path.join(CACHE, "dbus")
DEFAULT_WIDTH = 1024
# AT-SPI roles a user can click or type into.
ACTIONABLE = {
    "push button", "toggle button", "check box", "radio button", "link",
    "menu item", "check menu item", "radio menu item", "menu", "page tab",
    "combo box", "entry", "password text", "text", "spin button", "list item",
    "tree item", "table cell", "slider", "icon",
}


def die(msg, code=1):
    print(msg, file=sys.stderr)
    sys.exit(code)


def setup_env():
    os.environ.setdefault("DISPLAY", ":1")
    # The model's shell is not part of the desktop session; its bus address is
    # written by the session's xstartup.
    if "DBUS_SESSION_BUS_ADDRESS" not in os.environ:
        try:
            with open(DBUS_FILE) as f:
                addr = f.read().strip()
            if addr:
                os.environ["DBUS_SESSION_BUS_ADDRESS"] = addr
        except OSError:
            pass
    os.makedirs(SHOTS, exist_ok=True)


def run(*args, check=True):
    r = subprocess.run(args, capture_output=True, text=True)
    if check and r.returncode != 0:
        die(f"{args[0]} failed: {r.stderr.strip() or r.returncode}")
    return r.stdout


def grab():
    """Full-resolution screenshot as a PIL image."""
    fd, path = tempfile.mkstemp(suffix=".png", dir=SHOTS)
    os.close(fd)
    try:
        run("scrot", "-o", "-z", path)
        img = Image.open(path)
        img.load()
        return img
    finally:
        os.unlink(path)


def digest(img):
    small = img.convert("L").resize((160, 100))
    return hashlib.sha256(small.tobytes()).hexdigest()


def load_state():
    try:
        with open(STATE) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def save_state(st):
    tmp = STATE + ".tmp"
    with open(tmp, "w") as f:
        json.dump(st, f)
    os.replace(tmp, STATE)


# --- accessibility ----------------------------------------------------------

def atspi():
    try:
        import pyatspi  # noqa: PLC0415
    except ImportError:
        die("pyatspi is not installed")
    return pyatspi


def walk(acc, depth=0, out=None, limit=600):
    if out is None:
        out = []
    if acc is None or len(out) >= limit or depth > 40:
        return out
    try:
        role = acc.getRoleName()
        states = acc.getState()
        pyatspi = atspi()
        # Application roots never carry SHOWING; their windows do.
        if role != "application" and not states.contains(pyatspi.STATE_SHOWING):
            return out
        out.append((depth, acc, role))
        for i in range(min(acc.childCount, 400)):
            walk(acc.getChildAtIndex(i), depth + 1, out, limit)
    except Exception:  # noqa: BLE001 - apps vanish mid-walk
        pass
    return out


def extents(acc):
    pyatspi = atspi()
    try:
        e = acc.queryComponent().getExtents(pyatspi.DESKTOP_COORDS)
    except Exception:  # noqa: BLE001
        return None
    if e.width <= 0 or e.height <= 0:
        return None
    return [e.x, e.y, e.width, e.height]


def active_apps():
    pyatspi = atspi()
    desk = pyatspi.Registry.getDesktop(0)
    return [desk.getChildAtIndex(i) for i in range(desk.childCount)]


def elements():
    """Visible, actionable elements of all apps: (acc, role, name, box)."""
    found = []
    for app in active_apps():
        for _, acc, role in walk(app):
            if role not in ACTIONABLE:
                continue
            box = extents(acc)
            if not box:
                continue
            name = (acc.name or acc.description or "").strip()
            found.append((acc, role, name, box))
    return found


# --- commands ---------------------------------------------------------------

def cmd_screenshot(argv):
    width = DEFAULT_WIDTH
    marks = False
    it = iter(argv)
    for a in it:
        if a == "--marks":
            marks = True
        elif a == "--width":
            width = int(next(it, DEFAULT_WIDTH))
        else:
            die(__doc__, 2)
    img = grab().convert("RGB")
    scale = min(1.0, width / img.width)
    st = {"scale": scale, "digest": digest(img), "marks": []}
    if marks:
        for i, (_, role, name, box) in enumerate(elements(), 1):
            st["marks"].append({"n": i, "role": role, "name": name[:80], "box": box})
    small = img.resize((round(img.width * scale), round(img.height * scale)))
    if st["marks"]:
        draw = ImageDraw.Draw(small)
        font = ImageFont.load_default()
        for m in st["marks"]:
            x, y, w, h = (v * scale for v in m["box"])
            draw.rectangle([x, y, x + w, y + h], outline=(255, 0, 64), width=2)
            draw.rectangle([x, y, x + 8 + 7 * len(str(m["n"])), y + 14], fill=(255, 0, 64))
            draw.text((x + 3, y + 1), str(m["n"]), fill=(255, 255, 255), font=font)
    path = os.path.join(SHOTS, time.strftime("%Y%m%d-%H%M%S") + ".png")
    small.save(path, optimize=True)
    # Keep the last 20 screenshots.
    for old in sorted(os.listdir(SHOTS))[:-20]:
        os.unlink(os.path.join(SHOTS, old))
    save_state(st)
    print(path)
    print(f"size {small.width}x{small.height}; tp-act coordinates are in this image")
    for m in st["marks"]:
        print(f"[{m['n']}] {m['role']}: {m['name']}")


def screen_xy(x, y):
    scale = load_state().get("scale") or 1.0
    return round(float(x) / scale), round(float(y) / scale)


def act_and_verify(fn):
    before = digest(grab())
    fn()
    time.sleep(0.6)
    after = digest(grab())
    print("screen changed" if after != before else "screen did NOT change")


def cmd_act(argv):
    if not argv:
        die(__doc__, 2)
    op, args = argv[0], argv[1:]
    if op == "click" and len(args) == 2:
        x, y = screen_xy(*args)
        act_and_verify(lambda: run("xdotool", "mousemove", str(x), str(y), "click", "1"))
    elif op == "dblclick" and len(args) == 2:
        x, y = screen_xy(*args)
        act_and_verify(lambda: run("xdotool", "mousemove", str(x), str(y), "click", "--repeat", "2", "1"))
    elif op == "click-mark" and len(args) == 1:
        marks = {m["n"]: m for m in load_state().get("marks", [])}
        m = marks.get(int(args[0]))
        if not m:
            die("no such mark; run tp-screenshot --marks first")
        x, y, w, h = m["box"]
        act_and_verify(lambda: run("xdotool", "mousemove", str(x + w // 2), str(y + h // 2), "click", "1"))
    elif op == "type" and args:
        act_and_verify(lambda: run("xdotool", "type", "--delay", "20", "--", " ".join(args)))
    elif op == "key" and args:
        act_and_verify(lambda: run("xdotool", "key", "--", *args))
    elif op == "scroll" and len(args) == 1 and args[0] in ("up", "down"):
        btn = "4" if args[0] == "up" else "5"
        act_and_verify(lambda: run("xdotool", "click", "--repeat", "5", btn))
    else:
        die(__doc__, 2)


def cmd_ocr(argv):
    img = grab()
    fd, path = tempfile.mkstemp(suffix=".png", dir=SHOTS)
    os.close(fd)
    try:
        img.save(path)
        tsv = run("tesseract", path, "-", "-l", os.environ.get("TP_OCR_LANG", "eng+deu"), "tsv")
    finally:
        os.unlink(path)
    scale = load_state().get("scale") or 1.0
    words = []
    for line in tsv.splitlines()[1:]:
        f = line.split("\t")
        if len(f) < 12 or not f[11].strip() or float(f[10]) < 50:
            continue
        x, y, w, h = (int(v) for v in f[6:10])
        words.append({"text": f[11], "x": round((x + w / 2) * scale), "y": round((y + h / 2) * scale)})
    if "--json" in argv:
        print(json.dumps(words))
    else:
        for w in words:
            print(f"{w['x']},{w['y']}\t{w['text']}")


def cmd_ui(argv):
    if not argv:
        die(__doc__, 2)
    if argv[0] == "tree":
        for app in active_apps():
            print(f"# {app.name}")
            for depth, acc, role in walk(app):
                name = (acc.name or "").strip()
                if name or role in ACTIONABLE:
                    print(f"{'  ' * min(depth, 12)}{role}: {name[:100]}")
        return
    if argv[0] in ("click", "set") and len(argv) >= 2:
        want = argv[1].lower()
        for acc, role, name, _ in elements():
            if name.lower() != want:
                continue
            if argv[0] == "click":
                def press(acc=acc):
                    try:
                        acc.queryAction().doAction(0)
                    except Exception as e:  # noqa: BLE001
                        die(f"cannot press {name!r}: {e}")
                act_and_verify(press)
            else:
                def settext(acc=acc):
                    try:
                        acc.queryEditableText().setTextContents(" ".join(argv[2:]))
                    except Exception as e:  # noqa: BLE001
                        die(f"cannot type into {name!r} ({role}): {e}")
                act_and_verify(settext)
            return
        die(f"no visible element named {argv[1]!r}; see tp-ui tree")
    die(__doc__, 2)


def main():
    setup_env()
    prog = os.path.basename(sys.argv[0])
    cmds = {"tp-screenshot": cmd_screenshot, "tp-act": cmd_act, "tp-ocr": cmd_ocr, "tp-ui": cmd_ui}
    if prog not in cmds:
        die(__doc__, 2)
    if len(sys.argv) > 1 and sys.argv[1] in ("-h", "--help"):
        print(__doc__)
        return
    cmds[prog](sys.argv[1:])


if __name__ == "__main__":
    main()
