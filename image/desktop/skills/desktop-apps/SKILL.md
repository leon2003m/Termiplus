---
name: desktop-apps
description: Operate GUI apps on the XFCE desktop (LibreOffice, Nextcloud, dialogs) through accessibility first, screenshots last.
---

# Desktop apps

Order of preference, cheapest first:

1. A command instead of the app (see the documents skill; `nextcloudcmd` instead of the Nextcloud client).
2. Accessibility: `tp-ui tree` lists windows and their buttons, fields and menus by name; `tp-ui click "Save"`, `tp-ui set "File name" "report.pdf"`.
3. Marks: `tp-screenshot --marks` saves a small screenshot with numbered boxes and lists them; read the image, then `tp-act click-mark 7`.
4. Coordinates: `tp-screenshot`, read it, `tp-act click X Y` with coordinates from that image. `tp-ocr` lists words on screen with coordinates in the same scale.

Keyboard: `tp-act type "text"`, `tp-act key ctrl+s`, `tp-act key Return`. Windows: `wmctrl -l`, `wmctrl -a TITLE` to raise one.

Every `tp-act` and `tp-ui` action prints whether the screen changed. If it did NOT change, do not repeat blindly: take a new screenshot and find out why (a dialog, wrong window, element not focused).

The user may be watching or using the desktop at the same time. If windows move unexpectedly, ask before continuing.
