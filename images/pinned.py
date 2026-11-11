"""Shared steps for builds that run pinned microshift-io recipes."""

import json
from pathlib import Path
import subprocess

PACKAGING_URL = "https://github.com/microshift-io/microshift.git"


def run(*args, **kwargs):
    subprocess.run(args, check=True, **kwargs)


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def replace(path, old, new):
    text = path.read_text()
    if text.count(old) != 1:
        raise RuntimeError(f"Pinned packaging no longer matches the expected recipe: {path}")
    path.write_text(text.replace(old, new))


def load_lock(path, payloads):
    lock = json.loads(path.read_text())
    for arch in payloads:
        # a tag can move; the lock has to name the payload that was tested
        if "@sha256:" not in lock["payloads"].get(arch, ""):
            raise RuntimeError(f"{path.name} does not pin the {arch} payload by digest")
    return lock


def checkout_packaging(lock, directory):
    run("git", "init", "-q", directory)
    run("git", "-C", directory, "fetch", "-q", "--depth", "1", PACKAGING_URL, lock["packaging_commit"])
    run("git", "-C", directory, "checkout", "-q", "--detach", "FETCH_HEAD")
    if output("git", "-C", directory, "rev-parse", "HEAD") != lock["packaging_commit"]:
        raise RuntimeError("Packaging commit mismatch")
    return Path(directory)
