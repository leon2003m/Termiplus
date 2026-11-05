# Open Terminal: API-Kompatibilität pro Mandant

> **Historische Analyse vor der Migration (Checkpoint `808cc87`).**
> Die folgenden Quellbelege und offenen Aufgaben beschreiben den damaligen
> Stand, nicht die aktuelle Implementierung. Seit 2026-11-05 verwendet der
> Arbeitsstand Open WebUI 0.13.0 und Open Terminal 0.16.0 mit nativem
> Desktop/Browser. KasmVNC, eigener Desktop-Player, Portlinks und KI-Admin-Tools
> wurden entfernt; zeitlich begrenzte Freigaben zeigen nur noch Bilder.
> Chromium ist der einzige Browser, beide Standardprofile verwenden Sysbox.
> Aktueller Vertrag und Migrationsstatus: [Native Integration](native-integration.md),
> [Werkzeugprüfung](native-tool-validation.md), [Image-Vergleich](open-terminal-image-review.md).
> Die historischen Vorschläge unten sind keine aktuelle Aufgabenliste oder
> Freigabe zur Änderung von Sicherheitsgrenzen.

Stand: 2026-11-04. Reine lokale Quellcodeanalyse; keine Laufzeitprüfung,
Netzaufrufe oder Änderungen an Images, Proxy, Firewall und Deployment.

## Positionierung und Zielvertrag

Produktvorgabe von Leon: **termiplus ist die freie Multi-Tenant-Alternative zum
kostenpflichtigen OWUI-Produkt „Terminals“.** Das ist die vorgegebene
Positionierung, keine hier durchgeführte Preis-/Produktrecherche. Open Terminal
liefert die Arbeitsplatz-API; termiplus stellt jedem OWUI-Nutzer eine isolierte
Instanz bereit. Desktop-, Browser-, Git- und Live-Updates-Tabs sollen über das
Gateway denselben API-Vertrag wie mit einem normalen Open Terminal vorfinden.
Die technische Ausgangsarchitektur ist bereits genau diese: eine administrativ
konfigurierte OWUI-Verbindung, Routing über `X-User-Id` auf nutzereigene Container
(TP:internal/gateway/gateway.go:1–4,318–362).

„Open Terminal ist Single-User“ beschreibt hier das gewünschte Betriebsmodell
**einer OT-Instanz pro isoliertem Nutzer-Arbeitsplatz**, nicht die Behauptung,
dass die Referenz keinerlei Multi-User-Code enthält. Sie enthält optionalen
`MULTI_USER`-Betrieb mit OS-User-/Home-Auflösung; ohne diesen liefert
`get_filesystem` ein gewöhnliches `UserFS`. Das ersetzt nicht die von termiplus
bereitgestellte Container-, Runtime- und Netztrennung
(OT:open_terminal/dependencies.py:7–11,34–46;
TP:internal/gateway/lifecycle.go:513–534).

Ziel ist daher die Kompatibilität des gesamten nutzerbezogenen Datenpfads,
nicht nur das Einbetten eines Desktops. MCP bleibt die Steuerebene für
Provisionierung, Entitlements und Verwaltung; OT bleibt die Arbeits-API
(TP:map/CLAUDE.md:25–29). Die unten beschriebenen eigenen Console-Funktionen
sind optionaler Verwaltungsnutzen, keine Voraussetzung dafür, dass native
OWUI-Tabs auf nutzereigene OT-Instanzen zugreifen können.

## Quellen und Aussagegrenze

`OT` bezeichnet den unveränderten lokalen Checkout
`/home/admllmu/src/open-terminal-ref`, Commit `1f2460a`.
`TP` bezeichnet diesen Checkout, Commit `808cc87`, Branch
`feat/native-workspace-integration`. Die vorhandene lokale Änderung an
`deploy/policies.json` wurde nicht als beschlossene Konfiguration ausgewertet.
Alle Angaben `OT:path:Zeile` bzw. `TP:path:Zeile` beziehen sich auf diese Stände.
Während der Analyse wurden parallel weitere Implementierungsänderungen im
Arbeitsbaum sichtbar (u.a. Desktop-Dateien entfernt). Diese wurden weder
bearbeitet noch als fertige Migration bewertet. TP-Belege sind deshalb gegen
`git show 808cc87:<path>` zu lesen, auch wenn Dateien inzwischen geändert sind.

Der OWUI-0.13-Frontend-/Relay-Code gehört nicht zu den untersuchten Quellen.
Die folgenden Tabellen belegen den **von Open Terminal angebotenen Vertrag**;
sie beweisen nicht, welche Variante OWUI 0.13 tatsächlich auswählt oder welche
Header sein Relay ergänzt. Insbesondere Browser-Relay, Prefix-Rewriting und
Capability-Discovery müssen später gegen genau diesen OWUI-Stand geprüft werden.
Unser Image installiert standardmäßig Open Terminal **0.14.0**; die Gleichheit
mit dem lokalen Referenzcheckout ist damit noch nicht bewiesen
(TP:image/Dockerfile:9–35). Versionsnummern beider Produkte nicht gleichsetzen.

## 1. Endpoints und Protokolle

### Discovery und Desktop

HTTP-Bearer-Prüfung bei gesetztem OT-API-Key: OT:open_terminal/dependencies.py:14–31.
Unser Gateway setzt pro Container einen Key (TP:image/Dockerfile:74–75).

| Endpoint | Vertrag und Quellbeleg |
|---|---|
| `GET /api/config` | `features.terminal/notebooks/system/git/events/browser/desktop`; Desktop nur bei aktiviertem Feature und erfüllter `available()`-Prüfung. OT:open_terminal/main.py:497–512. |
| `GET /openapi.json` | Werkzeugoperationen sind über FastAPI-Router verfügbar; Desktop-Router wird bei fehlenden Desktop-Abhängigkeiten aus dem Schema ausgeblendet. OT:open_terminal/main.py:217–222,2495–2502; Desktop-Operationen siehe unten. |
| `GET /desktop` | Bearer-API-Key; startet Desktop bei Bedarf; JSON `{width,height,transport,platform,audio}`. OT:open_terminal/desktop.py:354–400. |
| `GET /desktop/screenshot` | Bearer; PNG desselben OT-Desktops, startet ihn ebenfalls bei Bedarf. OT:open_terminal/desktop.py:403–416. |
| `POST /desktop/action` | Bearer; `{actions:[...]}`, 1–50 Schritte, Click/Double-click/Move/Drag/Scroll/Type/Key; Koordinaten werden gegen Desktopgröße geprüft; abschließend PNG. Fehler melden bereits ausgeführte Aktionen und Fehlerindex, kein Rollback. OT:open_terminal/desktop.py:419–465,525–570. |
| `WS /desktop/stream` | Nach Upgrade innerhalb 10 s JSON `{"type":"auth","token":"…"}`, sonst Close 4001. Danach transportabhängige Nutzdaten. OT:open_terminal/desktop.py:573–598. |
| `WS /desktop/audio` | Gleicher Auth-Handshake; ohne Audio Close 4004. Binärer WebM-/Opus-Strom, 48 kHz Stereo, 64 kbit/s. Audio optional; benötigt pulseaudio, ffmpeg, pactl. OT:open_terminal/desktop.py:232–234,275–306,573–598. |

Linux liefert `transport="vnc"`: binäre WS-Payloads transportieren rohe
VNC-Bytes zum lokalen Unix-Socket, kein Kasm-Webclient und keine JSON-Mausbefehle
(OT:open_terminal/desktop.py:57–59,313–338).
macOS/Windows wählen `NativeDesktop`, `transport="frames"`; der Client sendet
JSON-Eingaben und `frameAck`, der Server binäre Bildänderungen. Das ist kein
alternativer Linux-Modus des aktuellen Codes
(OT:open_terminal/desktop.py:369–374; desktop_native.py:106–113,256–280).
Das Bildformat ist ein 12-Byte-Header, sechs unsigned 16-Bit-Werte in Network
Byte Order: Gesamtbreite/-höhe, Patch-x/-y/-breite/-höhe, danach JPEG.
Unveränderte Bilder entfallen (OT:open_terminal/frames.py:13–35).

### Terminal, Dateien, Git und Live-Updates

Diese API-Flächen gehören ebenfalls zum Kompatibilitätsziel. Die Tabelle ist
zusammen mit Desktop/Browser zu lesen; eine reine OpenAPI-Werkzeugübernahme
reicht nicht, da mehrere UI-Endpunkte ausdrücklich `include_in_schema=False`
tragen (OT:open_terminal/main.py:741–778,2122,2299–2345).

| API-Fläche | Erwarteter OT-Vertrag | Gateway-Bewertung der TP-Baseline |
|---|---|---|
| Terminal-Tabs | POST/GET `/api/terminals`, GET/DELETE `/api/terminals/{session_id}`, GET `/api/terminals/user/output`, POST `/api/terminals/user/input`. WS auf `/api/terminals/{session_id}`: Auth-JSON, danach binäre PTY-Ein-/Ausgabe und JSON `resize`, `activity`, `chat`. OT:open_terminal/main.py:2122,2276–2390. | HTTP und WS sind generisch routbar; die Sperre `/api/v1/` betrifft **nicht** `/api/terminals`. Container- und User-/Chat-Auswahl müssen vor dem Upstream-Zugriff identisch sein. TP:internal/gateway/gateway.go:311–362; ws.go:64–114. |
| Shell-/Dateiwerkzeuge | `/execute` HTTP und WS, WS `/execute/{process_id}/status`; HTTP-Dateioperationen unter `/files/…`, z.B. `/files/list`. Prozess-WS senden Listen bzw. Ausgabestatus nach Authentisierung. OT:open_terminal/main.py:675–697,1634–1750; Dateien weitere Routen:849–1633. | HTTP-Bodies/Query und WS-Nutzdaten durchreichen, Container-Key nur am Upstream einsetzen; keine zweite MCP-Datei-/Exec-Implementierung nötig. TP:internal/gateway/gateway.go:393–415; ws.go:91–114. |
| Git-Tab lesend | GET `/git/status`, `/git/diff`, `/git/log`, `/git/show`, `/git/branches`, `/git/worktrees`, `/git/pr/{view}`; Bearer, UserFS und Chat-CWD. OT:open_terminal/main.py:741–782. | Keine eigene Git-Sperre: vorhandener HTTP-Proxy reicht diese Pfade weiter. Kein Git-Server im Gateway erforderlich. TP:internal/gateway/gateway.go:303–362,393–415. |
| Git-Aktionen | POST `/git/pr/{action}`; POST/PATCH/DELETE `/git/{action}` mit begrenzter Aktionsliste (u.a. stage/commit/checkout/fetch/pull/push). OT:open_terminal/main.py:797–840. | Methoden, JSON und Fehler unverändert transportieren; Repository und Credentials bleiben im nutzereigenen OT. Remote-fetch/push unterliegen dessen Egress. Kein Read-only-Versprechen für die gesamte Git-API. TP:internal/gateway/gateway.go:393–415; lifecycle.go:170–175. |
| Live-Updates | WS `/events`: Auth-JSON, danach `subscribe` mit optionalem `path` und `ports`; Antworten `fs:ready`, `fs:change`, `ports:change`. UserFS und Chat-CWD wählen den Kontext. OT:open_terminal/main.py:700–738; utils/events.py:30–91. | Auth-Übersetzung und nachfolgendes Frame-Relay passen bereits; keine Umwandlung in Polling oder SSE erforderlich. Echte Zustellung, Reconnect und Watcher unter Sysbox/gVisor noch zu testen. TP:internal/gateway/ws.go:64–124. |
| App-/Port-Vorschau | GET `/ports`; HTTP `/proxy/{port}/{path}` für GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS. OT prüft sichtbaren Port und verbindet zu **localhost im OT-Container**. Für diese Route belegt der gelesene Code keinen eigenen WS-Handler. OT:open_terminal/main.py:1958–2023. | HTTP generisch routbar. Native App-Vorschau kann bestehende eigene Portlink-UI teilweise ersetzen; WS-/Hot-Reload-Parität nicht aus HTTP-Unterstützung ableiten. Ein Gateway-Host-Proxy wäre kein gleichwertiger Ersatz. TP:internal/gateway/gateway.go:393–415. |

### Browser

Alle folgenden Pfade haben Prefix `/browser`
(OT:open_terminal/browser/router.py:42).

| Endpoint | Vertrag und Quellbeleg |
|---|---|
| `POST /sessions` | Bearer; URL; erstellt Sitzung und liefert `id,token,url,title,mode,chrome_available`. OT:open_terminal/browser/router.py:141–156,173–197. |
| `GET`, `PATCH`, `DELETE /sessions/{sid}` | Bearer und Owner-/Chat-Abgleich. PATCH unterstützt URL, Titel, Modus `proxy`/`chrome`, Aktivstatus und Chatbindung. OT:open_terminal/browser/router.py:117–130,158–163,200–250. |
| `GET /view/{sid}/{token}/runtime.js` | Capability in URL statt Bearer; Laufzeitskript für umgeschriebene Webseiten. OT:open_terminal/browser/router.py:133–138,291–298. |
| `/view/{sid}/{token}/frame/{scheme}/{host}/{path}` | GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS; Capability; HTTP(S)-Proxy mit relativen Browser-Pfaden und CSP-Sandbox. OT:open_terminal/browser/router.py:273–311. |
| `WS /view/{sid}/{token}/ws` | Capability; erste JSON-Nachricht `{url:"ws(s)://…",protocols:[…]}`, Antwort `{type:"open",protocol:…}`, dann Text-/Binär-Relay. **Kein API-Key-Auth-Frame.** Upstream-Nachrichtengrenze 16 MiB. OT:open_terminal/browser/router.py:399–479. |
| `WS /view/{sid}/{token}/stream` | Capability; Chrome erforderlich; JSON `state` und initial JSON `frame` mit Base64-Bild. `{"type":"frames"}` aktiviert binäre JPEG-Patches, `frameAck` gibt nächsten Patch frei. Clientbefehle u.a. navigate/back/forward/reload/visibility/viewport/mouse/key. OT:open_terminal/browser/router.py:482–499; chrome.py:601–710,724–782. |
| `WS /view/{sid}/{token}/vnc` | Capability; Chrome mit eigenem Display erforderlich, sonst Close 4004; binäre VNC-Weiterleitung. OT:open_terminal/browser/router.py:482–499; chrome.py:578–599. |
| `GET /tabs`, `/snapshot`, `/screenshot`; `POST /action` | Bearer; Tab-/Chat-Kontext, Accessibility-Snapshot mit Elementreferenzen, PNG, Aktionsbatch mit Referenzen oder Pixelkoordinaten. OT:open_terminal/browser/router.py:558–620,851–868. |

Chrome-Modus sucht Chromium/Chrome/Edge oder ein konfiguriertes Executable,
steuert über `--remote-debugging-pipe` und nutzt ein Profil pro Owner im Home.
Mit Xtigervnc/xauth/xrandr/xdotool startet er ein eigenes Display; andernfalls
headless. Firefox/geckodriver implementieren diesen CDP-Vertrag nicht
(OT:open_terminal/browser/chrome.py:27–56,231–285).
`OPEN_TERMINAL_ENABLE_BROWSER` und `…ENABLE_DESKTOP` sind standardmäßig aktiv;
`…BROWSER_NO_SANDBOX` standardmäßig aus (OT:open_terminal/env.py:142–154).

## 2. Was unser Gateway durchreicht oder blockiert

| Pfad/Verhalten | Heutiger Zustand |
|---|---|
| Routing | `/p/<policy>/…` entfernt Policy-Prefix, ohne Prefix gilt `default`. Ziel ist OT-Port, standardmäßig 8000, nicht Kasm 6901. TP:internal/gateway/gateway.go:393–429; cmd/gateway/main.go:64–66. |
| Desktop-HTTP, Browser-Sessions und Werkzeuge | Keine eigene Desktop-/Browser-Sperre: nach Gateway-Bearer und User-/Chat-Prüfung generischer Reverse-Proxy, Container-Key ersetzt Authorization; Cookie wird entfernt. Funktioniert auf Routingebene, sofern Ziel-OT diese Endpoints bereitstellt. TP:internal/gateway/gateway.go:303–362,393–415. |
| Desktop-WS/Audio | Handshake kompatibel: Gateway prüft ersten Auth-Frame und ersetzt Token durch Container-Key; danach unveränderte Frames. Binary VNC/JPEG/Audio benötigt keine Gateway-Transkodierung. Kein Nachweis eines funktionierenden Desktop-Images dadurch. TP:internal/gateway/ws.go:23–27,64–114. |
| Browser-Capability-HTTP | Gateway fordert weiterhin Bearer und `X-User-Id`; Capability allein genügt hier nicht. Fehlende Header: 401/400. Ob OWUI sie ergänzt, ist offen. TP:internal/gateway/gateway.go:303–328; OT:open_terminal/browser/router.py:133–138,291–309. |
| Browser-Capability-WS | **Protokollkonflikt:** Gateway verlangt Auth-Frame und sendet einen solchen auch upstream. `/ws` erwartet stattdessen URL/Protokolle, `/vnc` rohe Binärdaten, `/stream` Browserkommandos. Ein Browser mit nativem OT-Vertrag wird so nicht unverändert unterstützt. TP:internal/gateway/ws.go:64–104; OT:open_terminal/browser/router.py:410–426; chrome.py:589,663–670. |
| Discovery ohne User | `/openapi.json`, `/api/config`, `/system` benutzen Spec-User; Spec-Policy wird nur pro Runtime geteilt. Unterschiedliche Images/Featureflags derselben Runtime können deshalb falsche Desktop-/Browser-Capabilities liefern. Schlussfolgerung aus TP:internal/gateway/gateway.go:227–253,318–344 und OT:open_terminal/main.py:503–512. |
| Pfadgrenzen | Encoded slash, nichtkanonische Pfade und `/api/v1/…` werden abgewiesen. Browser-URLs müssen diese Grenzen im Relay erhalten; keine pauschale Lockerung für beliebige Zielpfade. TP:internal/gateway/gateway.go:267–279,311–315. |
| Kasm-Stream | Separater Control-Pfad `/control/v1/stream/<container>/…`, nur laufender Desktop des Owners, Kasm-Basic-Auth auf Port 6901; für Shares separater Viewer. Kein Adapter für `/desktop/stream`. TP:internal/gateway/stream.go:14–37,41–77,95–105. |

WS-Upstream übernimmt nur User-/Session-/Terminal-Context-Header und handelt
keine WS-Extensions/Subprotokolle aus. Browser-Zielsubprotokolle liegen allerdings
im JSON-Relayvertrag, nicht zwingend im äußeren WS-Handshake. Nicht vorschnell
Subprotokoll-Passthrough ergänzen (TP:internal/gateway/ws.go:57–59,129–148;
OT:open_terminal/browser/router.py:426–441).

### Mandantentrennung entlang des Datenpfads

1. **Identität kommt vom vertrauenswürdigen Relay.** Gateway-Bearer bzw.
   WS-Token authentisieren die OWUI-Verbindung. `X-User-Id` wählt den Mandanten;
   der Gateway prüft das Format, aber bindet diesen Wert nicht an einen
   individuellen Nutzer-Token. Daher darf ein Endnutzer nicht mit dem gemeinsamen
   Gateway-Key beliebige User-Header setzen. Das OWUI-Relay muss die Identität
   serverseitig bestimmen und vom Client gelieferte Identitätsheader ersetzen.
   Dies ist eine Integrationsanforderung, kein hier geprüfter OWUI-Befund
   (TP:internal/gateway/gateway.go:303–338,380–390; ws.go:72–84).
2. **Ein Routingkontext für alle Tabs.** OWUI-Nutzer + Policy bestimmen den
   persistenten Arbeitsplatz; bei ephemeral kommt der Chat hinzu. HTTP, PTY,
   Events, Desktop und Browser müssen dieselbe Auswahl behalten. Ein „fehlender
   Header“-Fallback darf keine gemeinsame Benutzerinstanz erzeugen; der heutige
   Spec-User ist nur für die ausdrücklich zugelassenen Discovery-GETs vorgesehen
   (TP:internal/gateway/gateway.go:318–353;
   lifecycle.go:220–222; map/CLAUDE.md:25,35–38).
3. **Drei getrennte Schlüsselrollen.** Der OWUI-Verbindungskey endet am Gateway;
   der abgeleitete Container-API-Key endet am nutzereigenen OT. Das heutige
   Stream-Passwort ist zusätzlich ein separat abgeleitetes Kasm-Credential,
   einschließlich eigener Viewer-Domäne. Es ist weder der OT-Key noch ein
   Mandanten-Identifier. Native OT-Streams nutzen API-Key-Handshake bzw.
   Browser-Capability und benötigen nicht automatisch dieses Kasm-Passwort.
   Beim Kasm-Rückbau muss dessen Schutzwirkung (Owner-/Viewer-Rechte) ersetzt
   werden, nicht das Passwort blind in den neuen WS-Vertrag wandern
   (TP:internal/gateway/lifecycle.go:146–167,524–534;
   gateway.go:402; stream.go:28–37,49–69;
   OT:open_terminal/desktop.py:573–586;
   browser/router.py:133–138,482–499).
4. **Capability ist keine OWUI-Identität.** OT-Management bindet Browser-Sitzungen
   an Owner und Chat, die View-Routen prüfen jedoch nur Sitzung und URL-Token.
   Das Gateway muss vor diesen Routen weiterhin die richtige Nutzerinstanz
   wählen. URL-Token allein darf weder Container noch Policy eines anderen
   Mandanten auswählbar machen. Cookie- und URL-Rewriting im OWUI-Relay müssen
   diese Bindung erhalten; Gateway-Key und Container-Key gehören nicht in
   eingebettete Webseiten oder deren URLs
   (OT:open_terminal/browser/router.py:121–150,273–309;
   TP:internal/gateway/gateway.go:318–362,402–404).
5. **Netz und Laufzeit bleiben die äußere Grenze.** HTTP-Browserproxy,
   Chromium, Git und vom Modell gestartete Prozesse müssen innerhalb derselben
   Sysbox-/gVisor-Sandbox und deren Egress-Policy arbeiten. Ein erlaubter
   Gateway→OT-Stream erlaubt nicht OT→andere Mandanten/LAN. Offline-Policy
   wählt ein eigenes Netz; Resolver ist dort nicht gesetzt. Browser-URL-Prüfung
   ersetzt keine Firewall und auch keine DNS-Sperre
   (TP:internal/gateway/lifecycle.go:170–175,513–530;
   deploy/firewall.sh:9–33;
   OT:open_terminal/browser/rewrite.py:30–48).

**Kompatibilitäts-Gate:** Spätere Tests müssen für zwei Nutzer mit gleichem
Policy-/Chatnamen und möglichst kollidierenden lokalen Tab-/Prozess-IDs zeigen,
dass HTTP, PTY, Events, Git-Diffs, Browser-Frames und Desktop/Audio ausschließlich
zur jeweils richtigen Instanz gelangen. Negative Tests: manipuliertes
`X-User-Id`, fremder Browser-Token, fehlender Chat, abgelaufener Stream,
Spec-User-Missbrauch und Viewer-Eingabe. Das sind geforderte Tests, keine in
dieser Analyse behaupteten Ergebnisse; Ableitung aus den oben belegten
Routing-, Schlüssel- und Capability-Grenzen.

## 3. Passt unser KasmVNC-Image?

**Nicht direkt zum belegten OT-Linux-Desktopvertrag.** Unser Startscript erzeugt
KasmVNC-HTTP/WS auf `0.0.0.0:6901`, Display `:1`, mit getrennten User-/Viewerrechten
(TP:image/desktop/start-desktop.sh:34–66). OT erzeugt dagegen ein neues Display
`:40` aufwärts und startet ausdrücklich **Xtigervnc** mit Unix-VNC-Socket,
`-rfbport -1`, `-nolisten tcp`, Xauthority und `SecurityTypes None`. Die fehlende
VNC-Passwortprüfung ist dort an den lokalen Socket und den authentifizierten
OT-Relay gebunden, kein Vorbild für einen offenen TCP-Port
(OT:open_terminal/desktop.py:162–205).

OT verlangt Xtigervnc, startxfce4, dbus-run-session, xauth, xdotool, scrot, xclip
(OT:open_terminal/desktop.py:38–54). Unser Dockerfile deklariert stattdessen Kasm,
XFCE, maim/Pillow und Firefox, nicht vollständig diese Abhängigkeiten und keinen
Chromium-Browser (TP:image/desktop/Dockerfile:24–35). Transitive Pakete wurden
nicht in einem gebauten Image inventarisiert; daher kein behaupteter
Laufzeitbefund über tatsächlich fehlende Binaries. `maim` ersetzt im OT-Code
weder `scrot` noch Xtigervnc. Bloßes Nachinstallieren würde OT einen **zweiten**
Desktop starten lassen; Kasm-Vorschau auf `DISPLAY=:1` und OT-Bild könnten dann
verschiedene Sitzungen zeigen (OT:open_terminal/desktop.py:168,376;
TP:internal/gateway/preview.go:21). Eine Port-Umschreibung löst das nicht.

Empfehlung: einen OT-nativen Desktop pro Sandbox als Ziel untersuchen, statt
Kasm-Webclient und OT-Desktop dauerhaft parallel nachzubauen. Kasm erst ablösen,
wenn insbesondere View-only-Sharing ersetzt oder bewusst aufgegeben wurde:
heute erzwingt Kasm separate Schreib-/Leserechte; OT-Linux reicht eingehende
VNC-Bytes unverändert durch (TP:image/desktop/start-desktop.sh:54–60;
OT:open_terminal/desktop.py:319–326).

## 4. Einordnung unserer Features

Die folgenden Aussagen sind Architekturvorschläge, keine Löschfreigabe; bewertet
wurde die Implementierung im aktuellen Checkout, nicht jeder historische Branch.

| Bereich | Durch OT potenziell ersetzbar | Bleibt bei termiplus |
|---|---|---|
| `feat/desktop-preview` | Eigene Vollbild-/Desktop-Interaktion; Screenshot-Erzeugung könnte künftig OT übernehmen. | Übersichtskacheln, Owner-Prüfung, Offline-Verhalten, 30-s-Cache, Größen-/Formatvalidierung: OT-Screenshot startet Desktop, heutige Vorschau verlangt laufenden Owner-Desktop. TP:internal/gateway/preview.go:28–66,69–112; OT:open_terminal/desktop.py:403–416. |
| `console-ux` | Separater interaktiver Desktop-/Browser-Client, soweit OWUI diese Ansichten tatsächlich abdeckt. | Workspace-/Session-Auswahl, Start/Stop, Portlinks, Share-Verwaltung und deren Dialoge. Diese Funktionen existieren in TP:internal/mcp/ui/app.js:95–139,155–184,272–283; OT-Browser-Sitzung ist eine Tab-Sitzung, kein Sandbox-Lifecycle: OT:open_terminal/browser/router.py:173–197,246–250. |
| `workspace-tools` | Vorgeschlagene eigene Screenshot/Click/Type/Key-Werkzeugschicht; generische Browser-Accessibility-/Bildsteuerung ist bereits OT-API. TP:docs/workspace-tools.md:30–55; OT:open_terminal/desktop.py:525–561; browser/router.py:588–620,851–868. | CLI-/App-Werkzeuge, Firefox-spezifische Tests bei Bedarf und Regeln zu Owner, Entitlements, Zustimmung/Stop und nicht vertrauenswürdigen Bild-/Seitendaten. Native APIs ersetzen diese Regeln nicht. TP:docs/workspace-tools.md:32–70,243; internal/mcp/entitlements_test.go:61–96. |
| Plattform | Kein eigener Nachbau von Tab-Rendering oder Bildtransport erforderlich, wenn das native Relay integriert ist. | Runtime-Isolation, Mandantenzuordnung, Container-/Home-Lifecycle, Egress/DNS/Firewall, Entitlements, MCP-Steuerebene und widerrufbares View-only-Sharing. TP:map/CLAUDE.md:25–29; internal/gateway/stream.go:14–37; internal/mcp/shares.go:138–157,181–204. |

## 5. Reihenfolge und Security impact

Priorität ist die vollständige native OWUI-API pro Nutzer. Console-Politur und
zusätzliche MCP-Arbeitswerkzeuge folgen erst nach dieser Kompatibilität.

1. **Versionen und tatsächlichen OWUI-Relayvertrag festhalten.** Später lokal
   OWUI-0.13-Quellen ergänzen; HTTP-Header, First-Frames, Prefixe, Tabs und
   Capability-Discovery als Tests dokumentieren; Terminal, Dateien, Git, Events,
   App-Vorschau und beide Streaming-Familien abdecken. Identitätsheader müssen
   aus dem authentisierten OWUI-Relay stammen. Referenz gegen installierten
   OT-0.14.0-Code abgleichen. **Security impact:** zunächst nur Tests/Inventar;
   kein Auth-Bypass. Begründung: Versions- und Relay-Lücken oben.
2. **Discovery pro tatsächlichem Image/Featureprofil korrigieren.** Spec-Sharing
   nicht mehr nur nach Runtime; Tests für Terminal/Desktop mit gleicher Runtime
   und unterschiedlichen Capabilities. **Security impact:** keine Desktop-
   Session durch Discovery starten, Spec-User nicht in Benutzerkontext überführen.
   Ausgangspunkt TP:internal/gateway/gateway.go:227–253,318–344.
3. **OT-natives Image isoliert erproben.** Vollständige Desktop-Abhängigkeiten,
   optional Chromium, ein Display statt parallelem Kasm-/OT-Desktop; Audio erst
   bei Bedarf. Tests unter Sysbox und gVisor, ohne zusätzliche Hostports oder
   Docker-Exec. **Security impact:** Paket-/Browser-Angriffsfläche wächst;
   Chromium-Sandbox eingeschaltet lassen. Keine zusätzlichen Capabilities,
   Mounts, Runtime-Ausnahmen oder `--no-sandbox` als stillen Workaround.
   Grundlage OT:open_terminal/browser/chrome.py:248–277;
   TP:image/desktop/Dockerfile:24–35.
4. **Browser-Relay gezielt adaptieren.** Zuerst OWUI-Headerfluss nachweisen;
   dann eng begrenzten Adapter für Capability-HTTP/WS entwerfen, der Owner,
   Policy und Chat bindet und upstream den richtigen ersten Frame liefert.
   Keine globale Abschaltung der WS-Auth. **Security impact:** Änderung der
   Auth-/Routinggrenze benötigt negative Cross-User-/Cross-Chat-, Token-Replay-,
   Ablauf-, Pfad- und Handshake-Tests sowie explizite Freigabe bei Lockerung.
   URL-Tokens nicht loggen. Grundlage OT:open_terminal/browser/router.py:121–138;
   TP:internal/gateway/ws.go:64–104.
5. **Egress und Freigaben als Gates prüfen.** Browser-Proxy und Chrome müssen
   aus dem jeweiligen Sandbox-Netz heraus arbeiten; Public/LAN/Host/Metadata,
   Redirects, WebSockets und Offline-DNS testen. OT validiert HTTP(S)-URL und
   Credentials, aber `target_url` prüft keine privaten Zielnetze; deshalb
   keinesfalls Firewall durch Browser-Proxy ersetzen. Bestehende Entitlements
   auf neu zugängliche Pfade prüfen. **Security impact:** keine pauschalen
   Browser-Netzausnahmen; Firewall-/Sockproxy-/Härtungslockerung nur mit
   Notwendigkeitstest und Maintainer-Freigabe. Quellen:
   OT:open_terminal/browser/rewrite.py:30–48; chrome.py:261–317;
   TP:deploy/firewall.sh:9–33; internal/mcp/entitlements_test.go:61–96.
6. **Migration und Rückbau erst nach Parität.** Gleiches Desktopbild in Tab,
   Screenshot und optionaler Kachel belegen; Vorschau weiter begrenzen und
   validieren. Share-Viewer dürfen auch über neue Action-/VNC-Pfade keine Eingabe
   erhalten; Widerruf und Stop testen. Danach eigene Interaktionswerkzeuge und
   ggf. Kasm ablösen, Console und MCP-Verwaltung behalten.
   **Security impact:** OT-Capability ist kein Ersatz für unseren zeitlich
   begrenzten, widerrufbaren View-only-Share. Kein direkter Browserzugang zum
   Container-Key oder zum ungeschützten VNC-Socket. Quellen:
   TP:internal/gateway/preview.go:28–112; stream.go:28–37;
   internal/mcp/shares.go:198–204; OT:open_terminal/desktop.py:319–326.

Diese Schritte sind ein Arbeitsvorschlag. In dieser Analyse wurde kein Gate als
bestanden ausgegeben und keine Security-Grenze verändert.
