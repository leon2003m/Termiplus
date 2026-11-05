# Native frontend acceptance — 2026-11-05

Operator-directed acceptance used the deployed Open WebUI 0.13.0 frontend,
Open Terminal 0.16.0 and the final reviewed desktop image. A temporary,
sandboxed headless Chromium session drove the real UI through Playwright;
screenshots of the image/PDF viewers, desktop and browser were visually
inspected. This complements the [callable suite](../native-tool-validation.md)
and the [independent HTTP/PTY proof](../../test/native-independent/INDEPENDENT_PROOF.md).
It is not another independent source review.

The test used an existing normal test account and a new saved chat on
`termiplus-scratch`. All fixtures belonged to that chat's isolated temporary
workspace. No active personal desktop was restarted.

| Case | Observed result |
| --- | --- |
| Files navigation and text | The real Files tab opened a fixture directory and text file; its marker and German Unicode text were visible. |
| Image viewer | A 640×360 PNG decoded at its expected dimensions and rendered its colored background and marker. |
| PDF viewer | The native PDF tab rendered page 1 and its marker; the text layer was present. |
| Shared Desktop | The native live canvas rendered the wallpaper and bottom application dock. No logout/switch-user control or error dialog appeared in the inspected desktop. |
| Native Browser, Chrome mode | Navigation to a fixture HTTP server inside the workspace changed the tab title; the live browser canvas rendered the HTML heading and body. The native accessibility snapshot independently contained the same marker. |
| Model → inline image | Through the real chat UI, `termiplus-test` called `display_file` for the existing PNG with `inline=true`; the image rendered in the assistant response. |
| Model → PDF tab | After closing the existing PDF tab, a second chat request made the model call `display_file` with `page=1, inline=false`. A fresh PDF tab appeared and rendered its marker. |
| PDF download | The viewer's Download action delivered `preview.pdf`; downloaded bytes began with `%PDF-`. |

Two completed model responses produced three `display_file` calls in total:
PNG inline, PDF page 1, and an explicit non-inline PDF page 1. Assertions and
screenshots waited for rendered content, not just an allocated canvas. No
frontend JavaScript page errors were observed during these cases.

The test chat, its scratch container and fixture server were removed afterward.
Temporary credentials were deleted and the test account's sessions revoked.
Screenshots and downloads were kept outside the repository; no private URLs,
tokens or real-user data are part of this report.

## Repeat the acceptance

1. Use a new saved chat with the disposable scratch connection and a normal
   test account. Create text, PNG, PDF and HTML fixtures with distinct markers.
2. Open Files from the native tab menu. Navigate into the fixture directory and
   open the text, image and PDF; check rendered content, not just tab creation.
3. Open Desktop and inspect the wallpaper/dock after the live canvas paints.
4. Open Browser, select Chrome mode and navigate to the HTML fixture served
   within the workspace. Compare rendered content with `browser_snapshot`.
5. Ask the configured local model to display the existing PNG inline. Close
   the PDF tab, ask it to display page 1 non-inline, and verify that a new PDF
   tab renders. Download that PDF through the viewer.
6. Remove only the test chat/workspace and end the temporary test session.

These checks establish the observed frontend handoff for the pinned deployed
versions. They do not establish arbitrary model task completion or every
viewer format, browser setting and future upstream version.
