#!/usr/bin/env python3
"""Prepare the bind-mounted data directory, then drop runtime privileges."""

from __future__ import annotations

import os
import pwd
import stat
import sys
from pathlib import Path

DATA_DIR = Path("/app/data")
APP_USER = "appuser"


def _secure_data_tree(path: Path, uid: int, gid: int) -> None:
    path.mkdir(parents=True, exist_ok=True)
    for root, directories, files in os.walk(path, followlinks=False):
        _secure_path(Path(root), uid, gid, 0o700)
        for name in directories:
            candidate = Path(root, name)
            if not candidate.is_symlink():
                _secure_path(candidate, uid, gid, 0o700)
        for name in files:
            candidate = Path(root, name)
            if not candidate.is_symlink():
                _secure_path(candidate, uid, gid, 0o600)


def _secure_path(path: Path, uid: int, gid: int, mode: int) -> None:
    metadata = path.stat(follow_symlinks=False)
    if stat.S_ISLNK(metadata.st_mode):
        return
    if metadata.st_uid != uid or metadata.st_gid != gid:
        os.chown(path, uid, gid, follow_symlinks=False)
    if stat.S_IMODE(metadata.st_mode) != mode:
        os.chmod(path, mode, follow_symlinks=False)


def main() -> None:
    if len(sys.argv) < 2:
        raise SystemExit("no command specified")

    os.umask(0o077)
    if os.geteuid() == 0:
        account = pwd.getpwnam(APP_USER)
        _secure_data_tree(DATA_DIR, account.pw_uid, account.pw_gid)
        os.environ["HOME"] = account.pw_dir
        os.setgroups([])
        os.setgid(account.pw_gid)
        os.setuid(account.pw_uid)

    os.execvp(sys.argv[1], sys.argv[1:])


if __name__ == "__main__":
    main()
