# Tools for workspace agents

The terminal image already includes git, curl/wget, jq, ripgrep, sqlite3,
unzip/zip/xz, tree/file, SSH client, build-essential/pkg-config, patch,
Python 3 with venv/pip/dev headers, and Node.js/npm. Python packages belong in
user virtual environments, separate from the Open Terminal server environment.
Node here is a user workspace tool; CI does not need Node.

## Small terminal additions

Measured on Debian trixie amd64 with no recommends, November 2026. Apt estimates
include missing dependencies; shared dependencies mean the rows do not add
exactly. Sizes vary with architecture and package updates.

| Addition | Purpose | Additional installed size |
|---|---|---:|
| fd-find (`fdfind`) | Fast file discovery | 3.58 MB |
| rsync | Incremental copies and dry-run comparisons | 0.98 MB |
| bc | Exact decimal calculations | 0.24 MB |
| pipx | Isolated Python command-line applications | 4.52 MB |
| patch | Apply patches | Already installed via build tools |
| uv | Optional Python environment manager | Not in configured Debian repositories; omitted |
| pandoc | Document conversion | About 200 MB; omitted to keep below 150 MB |

The rebuilt terminal image is 1,433,254,991 bytes versus the local alpha base's
1,423,658,833 bytes: about **9.60 MB** growth, comfortably within budget. This is
an image comparison, not a guarantee that all future package versions stay that
size. No new network, capability, mount or Docker endpoint is required.

## Agent desktop control: proposal, not an enabled feature

Use the existing Open Terminal shell/files and application APIs first. They are
cheaper, easier to verify and less sensitive to window layout. Use GUI tools only
when an application has no suitable direct interface. Open Interpreter and
OpenHands are alternative orchestration products; they duplicate much of Open
WebUI plus Open Terminal and are not proposed dependencies.

A vision-capable chat model could use owner-only `desktop_screenshot`,
`desktop_click`, `desktop_type`, `desktop_key` and `desktop_windows` MCP tools.
Screenshot should reuse the preview execution/validation pattern, through the
existing per-container Open Terminal API/key. The current 320px thumbnail is
suitable for overview, not precise clicks: a future approved screenshot tool
needs bounded higher-resolution output, a frame ID, dimensions and coordinates
bound to that frame. Reject stale frames, out-of-bounds clicks and stopped or
replaced containers. Treat image bytes, accessibility text, page content and
window titles as untrusted data, never as instructions.

Click/type/key would invoke fixed argv inside the user's container using
xdotool, and window listing would use wmctrl. Do not concatenate model text into
shell commands. Use explicit permitted key names, text byte limits and bounded
coordinates; never add Docker exec access to sockproxy. The existing gateway
path already reaches Open Terminal. All operations must recheck authenticated
ownership, current policy entitlement and a short-lived human-approved driving
grant tied to that exact session/container generation. Share viewers get no
control, and admin status must not silently grant control of another person's
desktop.

Before driving, show the owner the session and intended task, ask for approval,
and display a persistent **Agent is driving** indicator with a **Stop** button.
Use an exclusive short lease (for example 60 seconds, renewable only visibly),
serialize actions, limit actions (for example 2/sec and 100/task), bound execution
and inference deadlines, and revoke on stop, expiry, owner disconnect or
entitlement change. Stop must cancel queued work and prevent new actions; an
already delivered click cannot be undone. Existing shell access can perform GUI
actions today, so this UX grant cannot be a security boundary against code that
already runs as the desktop user. Enforcing that would require a separately
approved isolation design.

Audit actor, session, operation, timestamp, result, lease and frame IDs, and
truncation/timeout metadata. Do not log typed text, clipboard contents, screenshots
or raw accessibility trees by default. Before risky file changes offer a home
snapshot; restore affects home files only, not remote services, purchases,
sent messages or other external side effects. Confirm those actions separately.
Existing containers must opt in; no control packages or MCP endpoints are added
by this proposal.

### Optional local grounding model

`desktop_act(instruction)` could ask a separate local model to ground a target
into coordinates, then pass its proposed action through the same lease,
validation, owner confirmation and bounded action loop. It must not get an
unbounded shell/tool loop. The normal chat model can also use screenshots
directly; grounding is optional.

[ByteDance's model card](https://huggingface.co/ByteDance-Seed/UI-TARS-1.5-7B)
identifies UI-TARS-1.5-7B as a Qwen2.5-VL based model. A
[GGUF publisher](https://huggingface.co/Mungert/UI-TARS-1.5-7B-GGUF)
provides a llama.cpp conversion and matching vision projector. Its Q4_K_M file
is 4.78 GB plus a 0.853 GB Q8 projector (about 5.63 GB of weights).
[llama.cpp multimodal documentation](https://github.com/ggml-org/llama.cpp/tree/master/tools/mtmd)
describes the libmtmd/server path; multimodal support evolves, so pin a tested
server commit and matching model/projector revision before deployment.

A reasonable **estimate**, not a measured benchmark, is to reserve 8–12 GiB VRAM
for quantized weights, vision processing, context/KV cache and runtime buffers.
The machine's 16 GiB AMD GPU is a plausible target with the competing model
unloaded and modest context, but concurrency with the existing voice/chat model
must not be assumed. No model was downloaded or loaded and the current running
server's compatibility/latency was not tested. Prefer the existing llama.cpp
ROCm setup; an Ollama package tag alone is not proof that its vision projector
works. A separate tested model swap and representative screenshot benchmark
remain a deployment prerequisite, requiring owner scheduling of shared GPU use.

Never place an Open WebUI API key or signed-in OWUI browser profile in a workspace:
it grants access far beyond inference, including private chats/files and tools.
If desktop chat is later wanted, evaluate a separate inference-only endpoint key,
without OWUI credentials or its tool authority.

## Egress: current behavior and proposal

`deploy/firewall.sh` permits gateway-initiated workspace connections and their
replies, blocks terminal-to-terminal traffic, the host and private ranges by
default, and always denies link-local and CGNAT destinations. Public internet
egress remains allowed. Admin-rendered rules can open selected internal
CIDR/port destinations except never-allowed ranges. IPv6 workspace forwarding
and host access are dropped. This protection requires the documented host
firewall setup; Docker networking alone does not provide it.

The optional `deploy/docker-compose.netpolicy.yml` enables the admin network
policy API and an internal DNS forwarder. `deploy/dns-forwarder/entrypoint.sh`
uses dnsmasq, static policy hosts and configured internal upstream resolvers;
no public resolver fallback. Without the overlay, workspaces retain Docker's
embedded resolver and the admin API is unavailable. DNS overrides alone are
not an egress allow-list and do not block direct IP access or DNS-over-HTTPS.

Propose a future per-workspace-policy mode:

| Mode | Outbound behavior |
|---|---|
| offline | No new outbound connections, including general DNS; preserve gateway replies |
| internet | Current public internet behavior, private/internal denied except approved destinations |
| allow-list | Only explicit destination/protocol/port rules plus tightly scoped resolver access |

Implementation would require policy-to-container/IP binding, atomic host rule
updates, revalidation on recreate/adoption, IPv4+IPv6 tests and no broad
established-connection bypass. Domain allow-lists need a separate decision on
DNS rebinding, address changes and shared CDN IPs; hostname matching in a plain
firewall is not sufficient. Fail closed on unresolved rules. Never permit
cloud metadata/link-local, host management or other users' containers. This
is a design proposal only; no firewall/netpolicy changes are made here and
maintainer approval is needed before implementing security-boundary changes.
