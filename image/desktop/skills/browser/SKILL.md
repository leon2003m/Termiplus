---
name: browser
description: Use websites with tp-browser - Chromium on the desktop driven by refs, which the user can watch and take over.
---

# Browser

Use `tp-browser`, not screenshots, for websites. Use `curl` instead when you only need to download a file or call an API.

1. `tp-browser open example.org` opens the page and prints its elements as `[e12] button: Search`.
2. Act by ref: `tp-browser click e12`, `tp-browser fill e7 "text"`, `tp-browser press Enter`. Each prints the new page; check that it shows what you expected.
3. Refs change after every page change: always use refs from the latest output, and run `tp-browser snapshot` again if unsure.
4. `tp-browser text` reads the page; elements marked `(below)` are off screen but clickable.
5. `tp-browser screenshot` only when the layout matters (charts, images); it prints a file path you can read.
6. `tp-browser tabs` / `tab N` when a link opened a new tab.

For cookie banners, decline or choose "necessary only".
For logins, captchas and payments follow the handoff skill: stop and let the user do it on the desktop.
Downloads land in ~/Downloads; move results for the user to ~/outputs.
