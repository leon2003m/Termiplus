"""Test-only AT-SPI probe for a disposable, non-root desktop.

Run with the desktop user's DISPLAY/session bus environment. Example:
  python3 a11y_probe.py --app dolphin
  python3 a11y_probe.py --app dolphin --role button --name user --click
A successful AT-SPI reply is not proof of an application change; verify outcome.
"""
import argparse
import json
import os
import pathlib
import time

import pyatspi


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--app', required=True)
    parser.add_argument('--role')
    parser.add_argument('--name')
    action = parser.add_mutually_exclusive_group()
    action.add_argument('--click', action='store_true')
    action.add_argument('--set-text')
    args = parser.parse_args()
    if os.getuid() != 1000:
        parser.error('use the disposable desktop user (UID 1000)')
    if (args.click or args.set_text is not None) and (not args.role or args.name is None):
        parser.error('actions require an exact role and name')
    if args.set_text is not None and len(args.set_text.encode()) > 4096:
        parser.error('test text is limited to 4096 bytes')
    # Bridge the disposable user's own XFCE session environment only.
    for process in pathlib.Path('/proc').glob('[0-9]*'):
        try:
            if process.stat().st_uid != os.getuid():
                continue
            if (process / 'comm').read_text().strip() != 'xfce4-session':
                continue
            env = dict(item.split('=', 1) for item in
                       (process / 'environ').read_bytes().decode().split('\0') if '=' in item)
            os.environ.update({key: value for key, value in env.items()
                               if key in ('DISPLAY', 'DBUS_SESSION_BUS_ADDRESS', 'XAUTHORITY')})
            break
        except OSError:
            continue
    start = time.monotonic()
    records, matches = [], []
    visited = 0
    incomplete = False

    def walk(node, depth=0):
        nonlocal visited, incomplete
        if visited >= 2000 or depth > 30 or time.monotonic() - start > 3:
            incomplete = True
            return
        visited += 1
        try:
            role, name = node.getRoleName(), node.name
            showing = node.getState().contains(pyatspi.STATE_SHOWING)
            if showing:
                try:
                    bounds = tuple(node.queryComponent().getExtents(pyatspi.DESKTOP_COORDS))
                except NotImplementedError:
                    bounds = None
                records.append(dict(role=role, name=name[:120], bounds=bounds))
                if role == args.role and name == args.name:
                    matches.append(node)
            for child in node:
                walk(child, depth + 1)
        except Exception:
            # Inspection can return partial data, but actions must fail closed.
            incomplete = True
            return

    try:
        for app in pyatspi.Registry.getDesktop(0):
            if app is not None and app.name == args.app:
                walk(app)
    except Exception:
        incomplete = True
    result = dict(nodes=records, visited=visited, incomplete=incomplete,
                  seconds=round(time.monotonic() - start, 3))
    if args.click or args.set_text is not None:
        if incomplete:
            parser.error('action refused: application tree traversal was incomplete')
        if len(matches) != 1:
            parser.error('action needs exactly one visible match; found ' + str(len(matches)))
        target = matches[0]
        if args.click:
            interface = target.queryAction()
            if interface.nActions == 0:
                parser.error('element has no action')
            result['accepted'] = bool(interface.doAction(0))
        else:
            result['accepted'] = bool(target.queryEditableText().setTextContents(args.set_text))
    print(json.dumps(result, ensure_ascii=True))


if __name__ == '__main__':
    main()
