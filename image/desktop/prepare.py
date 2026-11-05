"""Release only abandoned, native-managed Chromium singleton links on startup.

Persistent homes outlive the container's PID namespace. A stale Chromium PID
can therefore refer to an unrelated process after recreation. The native lease
is authoritative: never touch a profile with a held lease or without its marker.
"""
import fcntl
import os
from pathlib import Path
import sys


def clean_profiles(root):
    if not root.is_dir():
        return
    for profile in root.iterdir():
        if profile.is_symlink() or not profile.is_dir():
            continue
        try:
            lease = os.open(profile / ".open-terminal.lock", os.O_RDWR | os.O_NOFOLLOW)
            try:
                fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
                if os.read(lease, 8) != b"managed\n":
                    continue
                for name in ("SingletonLock", "SingletonSocket", "SingletonCookie"):
                    link = profile / name
                    if link.is_symlink():
                        link.unlink()
            finally:
                os.close(lease)
        except OSError:
            # A live lease, an unmanaged profile, or a concurrently removed home
            # must not keep the server from starting or trigger forced cleanup.
            continue


if __name__ == "__main__":
    clean_profiles(Path.home() / ".local/state/open-terminal/browser")
    os.execv(sys.argv[1], sys.argv[1:])
