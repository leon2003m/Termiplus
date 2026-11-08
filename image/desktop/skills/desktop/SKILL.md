---
name: desktop
description: Use the XFCE desktop and Open Terminal's built-in browser for web pages and graphical apps, cheapest way first.
---

# Desktop and browser

This workspace has a graphical XFCE desktop that the user can watch and take over, and Open Terminal's built-in browser. Both are started on demand by Open Terminal's desktop and browser tools; there is nothing to launch from the shell.

Order of preference, cheapest first:

1. A command instead of an app (see the documents skill: `soffice --headless`, `pandoc`, `python3`).
2. The browser tools for web pages: they work on page content and are more reliable than clicking on pixels.
3. The desktop screenshot tool, then the desktop action tool, for graphical apps (LibreOffice, Dolphin, Kate, dialogs). Take a screenshot before every action and use the pixel size it reports; never repeat an out-of-bounds coordinate.

After every action take a new screenshot or reread the page and check that it did what you meant. If nothing changed, find out why (a dialog, the wrong window, a field without focus) before you try again.

`wmctrl -l`, `xdotool` and `xclip` work on the desktop's display once it is running. The user may be using the desktop at the same time; if windows move unexpectedly, ask before continuing. For logins, captchas and payments follow the handoff skill.
