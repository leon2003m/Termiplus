# Disposable desktop experiments

Nothing here is installed by the shipped Dockerfiles. Use a desktop image built
from the feature branch, tagged `termiplus-desktop:codex-theme`. Docker builds
need package download access; runtime tests have no internet/LAN or published
ports. Do not run these scripts against a user's desktop: they create documents,
launch apps, replace clipboard contents and manipulate window focus.

```sh
docker build -t termiplus-desktop:codex-tools-clean \
  -f test/integration/desktop/Dockerfile.tools test/integration/desktop
docker network create --internal codex-tools-net
docker run -d --name codex-tools-test --runtime=sysbox-runc \
  --network codex-tools-net --hostname localhost \
  -e OPEN_TERMINAL_API_KEY=fake-test-key \
  -e OPEN_TERMINAL_DESKTOP_PASSWORD=0123456789fake-test-password0123456789 \
  termiplus-desktop:codex-tools-clean
# Waits for XFCE; uses only fake local fixtures.
docker exec -i codex-tools-test python3 - < test/integration/desktop/control_smoke.py
# Emits only a bounded test-app tree. Action matching is exact and unambiguous.
docker exec -i codex-tools-test timeout 10 python3 - --app dolphin \
  < test/integration/desktop/a11y_probe.py
```

For the optional browser DOM test, download Mozilla's pinned upstream driver
outside the internal runtime network, verify the recorded test archive digest,
extract it under /tmp and stream just the binary into the disposable container's
runtime /tmp:

```sh
mkdir -p /tmp/codex-geckodriver
curl -fL -o /tmp/codex-geckodriver/driver.tar.gz \
  https://github.com/mozilla/geckodriver/releases/download/v0.37.1/geckodriver-v0.37.1-linux64.tar.gz
printf '%s\n' 'e815130ea95983e162ae91843b48d3a3ce991735635fce83a647afde21e09f7e  /tmp/codex-geckodriver/driver.tar.gz' | sha256sum -c -
tar -xzf /tmp/codex-geckodriver/driver.tar.gz -C /tmp/codex-geckodriver
docker exec -i codex-tools-test sh -c 'cat > /tmp/geckodriver && chmod 700 /tmp/geckodriver' \
  < /tmp/codex-geckodriver/geckodriver
docker exec -i codex-tools-test python3 - < test/integration/desktop/browser_dom.py
```

The digest records the downloaded test artifact; it is not an independent
upstream signature verification. WebDriver and BiDi bind only to loopback. This
test exercises WebDriver commands and negotiates BiDi; it does not test BiDi
messages. No browser system-access option or permissive origin list is enabled.
AT-SPI `accepted=true` is **not** proof the app changed: verify saved file/form
outcomes, which the exploratory semantic tests did not reliably achieve.
Inspection reports `incomplete=true` on traversal limits or errors; actions are
refused for every incomplete traversal, even if one match was found.
Run the probe regression tests without desktop dependencies using
`python3 -m unittest discover -s test/integration/desktop -p test_a11y_probe.py`.

Read screenshots from runtime /tmp using `docker exec ... cat`, because Sysbox's
mount layout can make `docker cp` read a different /tmp. Test outputs include only
fake fixture data. Always clean up, including after failures:

```sh
docker rm -f codex-tools-test
docker network rm codex-tools-net
```

`trash.py` is the separate desktop-trash regression test. It requires a fresh
empty trash and an actual desktop, and confirms the real Empty Trash dialog.
The measured results/limitations and the unimplemented control proposal are in
[workspace-tools.md](../../../docs/workspace-tools.md).
