#!/usr/bin/env python3
"""Prepare the pinned rustpush source checkout used by builds."""

from __future__ import annotations

import argparse
import filecmp
import os
import shutil
import subprocess
import sys
from pathlib import Path


UPSTREAM_REPO = "https://github.com/OpenBubbles/rustpush.git"
PIN_FILE = Path("third_party/rustpush-upstream.sha")
DEFAULT_RUSTPUSH_DIR = Path("third_party/rustpush-upstream")
FAIRPLAY_CERTS = [
    "4056631661436364584235346952193",
    "4056631661436364584235346952194",
    "4056631661436364584235346952195",
    "4056631661436364584235346952196",
    "4056631661436364584235346952197",
    "4056631661436364584235346952198",
    "4056631661436364584235346952199",
    "4056631661436364584235346952200",
    "4056631661436364584235346952201",
    "4056631661436364584235346952208",
]


def run(*args: str, env: dict[str, str] | None = None) -> None:
    subprocess.run(args, check=True, env=env)


def capture(*args: str) -> str:
    return subprocess.check_output(args, text=True, stderr=subprocess.DEVNULL).strip()


def read_pin() -> str:
    try:
        pin = PIN_FILE.read_text(encoding="utf-8").strip()
    except FileNotFoundError:
        sys.exit(f"error: {PIN_FILE} is missing")
    if not pin:
        sys.exit(f"error: {PIN_FILE} is empty")
    return pin


def git_env() -> dict[str, str]:
    env = os.environ.copy()
    env["GIT_CONFIG_COUNT"] = "1"
    env["GIT_CONFIG_KEY_0"] = "url.https://github.com/.insteadOf"
    env["GIT_CONFIG_VALUE_0"] = "git@github.com:"
    return env


def ensure_checkout(rustpush_dir: Path, pin: str) -> None:
    env = git_env()
    if not (rustpush_dir / ".git").is_dir():
        print(f"Cloning OpenBubbles/rustpush at pinned SHA {pin}...")
        rustpush_dir.parent.mkdir(parents=True, exist_ok=True)
        run("git", "clone", UPSTREAM_REPO, str(rustpush_dir), env=env)
        run("git", "-C", str(rustpush_dir), "checkout", pin, env=env)
        run("git", "-C", str(rustpush_dir), "submodule", "sync", "--recursive", env=env)
        run("git", "-C", str(rustpush_dir), "submodule", "update", "--init", "--recursive", env=env)

    current = capture("git", "-C", str(rustpush_dir), "rev-parse", "HEAD")
    if current == pin:
        return

    print(f"Checking out pinned rustpush SHA {pin} (was {current})...")
    run("git", "-C", str(rustpush_dir), "remote", "set-url", "origin", UPSTREAM_REPO, env=env)
    print(f"Discarding any local mods to {rustpush_dir} before checkout...")
    run("git", "-C", str(rustpush_dir), "reset", "--hard", "HEAD", env=env)
    run("git", "-C", str(rustpush_dir), "clean", "-fd", env=env)
    run("git", "-C", str(rustpush_dir), "fetch", "--all", "--tags", "--prune", env=env)
    run("git", "-C", str(rustpush_dir), "checkout", pin, env=env)
    run("git", "-C", str(rustpush_dir), "submodule", "sync", "--recursive", env=env)
    run("git", "-C", str(rustpush_dir), "submodule", "update", "--init", "--recursive", env=env)

    current = capture("git", "-C", str(rustpush_dir), "rev-parse", "HEAD")
    if current != pin:
        sys.exit(f"error: rustpush checkout is at {current}, expected {pin}")


def ensure_fairplay_certs(rustpush_dir: Path) -> None:
    fairplay_dir = rustpush_dir / "certs/fairplay"
    if fairplay_dir.is_dir():
        return
    print("Generating FairPlay cert stubs...")
    fairplay_dir.mkdir(parents=True, exist_ok=True)
    legacy_dir = rustpush_dir / "certs/legacy-fairplay"
    for name in FAIRPLAY_CERTS:
        shutil.copy2(legacy_dir / "fairplay.crt", fairplay_dir / f"{name}.crt")
        shutil.copy2(legacy_dir / "fairplay.pem", fairplay_dir / f"{name}.pem")


def overlay_open_absinthe(rustpush_dir: Path) -> None:
    src = Path("rustpush/open-absinthe")
    dst = rustpush_dir / "open-absinthe"
    if not (src / "Cargo.toml").is_file():
        return
    if dst.exists() and not directories_differ(src, dst):
        return
    print(f"Overlaying our open-absinthe (native NAC wiring) onto {dst}...")
    shutil.rmtree(dst, ignore_errors=True)
    shutil.copytree(src, dst)


def directories_differ(left: Path, right: Path) -> bool:
    if not right.exists():
        return True
    comparison = filecmp.dircmp(left, right)
    if comparison.left_only or comparison.right_only or comparison.funny_files:
        return True
    _, mismatches, errors = filecmp.cmpfiles(
        left,
        right,
        comparison.common_files,
        shallow=False,
    )
    if mismatches or errors:
        return True
    return any(directories_differ(left / subdir, right / subdir) for subdir in comparison.common_dirs)


def replace_line_once(path: Path, old: str, new: str, message: str) -> None:
    lines = path.read_text(encoding="utf-8").splitlines(keepends=True)
    for i, line in enumerate(lines):
        if line.rstrip("\r\n") != old:
            continue
        print(message)
        ending = "\r\n" if line.endswith("\r\n") else "\n" if line.endswith("\n") else ""
        lines[i] = f"{new}{ending}"
        path.write_text("".join(lines), encoding="utf-8")
        return


def replace_line_prefix_once(path: Path, old: str, new: str, message: str) -> None:
    lines = path.read_text(encoding="utf-8").splitlines(keepends=True)
    for i, line in enumerate(lines):
        if not line.startswith(old):
            continue
        print(message)
        lines[i] = line.replace(old, new, 1)
        path.write_text("".join(lines), encoding="utf-8")
        return


def patch_keychain(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    marker = "Ignoring exclusion of ourselves"
    needle = "            for excluded in &trust.excludeds {\n"
    if marker in text or needle not in text:
        return
    print("Patching keychain.rs to ignore self-exclusion in fast_forward_trust (Clique self-eviction fix; ports 9f29ff1)...")
    replacement = (
        "            let my_id = &state.user_identity.as_ref().unwrap().identifier;\n"
        "            for excluded in &trust.excludeds {\n"
        "                if excluded == my_id {\n"
        "                    warn!(\n"
        "                        \"Ignoring exclusion of ourselves ({}) from peer {}\",\n"
        "                        excluded,\n"
        "                        peer.0.hash.as_ref().unwrap()\n"
        "                    );\n"
        "                    continue;\n"
        "                }\n"
    )
    path.write_text(text.replace(needle, replacement, 1), encoding="utf-8")


def apply_source_patches(rustpush_dir: Path) -> None:
    replace_line_once(
        rustpush_dir / "src/lib.rs",
        "mod activation;",
        "pub mod activation;",
        "Making rustpush activation module public (needed by RelayOSConfig)...",
    )
    replace_line_once(
        rustpush_dir / "src/lib.rs",
        "mod ids;",
        "pub mod ids;",
        "Making rustpush ids module public (needed by FT RespondedElsewhere overlay)...",
    )
    client_rs = rustpush_dir / "apple-private-apis/icloud-auth/src/client.rs"
    replace_line_once(
        client_rs,
        "    token: String,",
        "    pub token: String,",
        "Making FetchedToken.token pub (needed to replay persisted PET on session restore)...",
    )
    replace_line_once(
        client_rs,
        "    expiration: SystemTime,",
        "    pub expiration: SystemTime,",
        "Making FetchedToken.expiration pub...",
    )
    replace_line_prefix_once(
        rustpush_dir / "apple-private-apis/icloud-auth/src/lib.rs",
        "pub use client::{AppleAccount, LoginState,",
        "pub use client::{AppleAccount, FetchedToken, LoginState,",
        "Re-exporting FetchedToken from icloud_auth crate root...",
    )
    patch_keychain(rustpush_dir / "src/icloud/keychain.rs")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--rustpush-dir", default=str(DEFAULT_RUSTPUSH_DIR))
    args = parser.parse_args()

    rustpush_dir = Path(args.rustpush_dir)
    if rustpush_dir != DEFAULT_RUSTPUSH_DIR:
        sys.exit(f"error: this helper only supports {DEFAULT_RUSTPUSH_DIR}")

    pin = read_pin()
    ensure_checkout(rustpush_dir, pin)
    ensure_fairplay_certs(rustpush_dir)
    overlay_open_absinthe(rustpush_dir)
    apply_source_patches(rustpush_dir)


if __name__ == "__main__":
    main()
