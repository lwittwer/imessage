#!/usr/bin/env bash
# Exercise the actual prepared dependency's guard with synthetic APNs tokens.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

python3 - "$ROOT/third_party/rustpush-upstream/src/passwords.rs" "$TMP/guard.rs" <<'PY'
import pathlib
import re
import sys

source = pathlib.Path(sys.argv[1]).read_text()
watch = source.split("async fn prepare_watch(", 1)[1]
match = re.search(r"if (state\.my_token_registered .*?) \{ return Ok\(\(\)\) \}", watch)
if match is None:
    raise SystemExit("prepare_watch guard changed; update the regression fixture")
guard = match.group(1).replace("state.my_token_registered", "registered").replace("connection.get_token().await", "token")
fixture = '''
fn skip_registration(registered: Option<[u8; 32]>, token: [u8; 32]) -> bool {
    GUARD
}
#[test]
fn fresh_state_registers() {
    assert!(!skip_registration(None, [1; 32]));
}
#[test]
fn changed_token_registers() {
    assert!(!skip_registration(Some([2; 32]), [1; 32]));
}
#[test]
fn matching_token_skips_registration() {
    assert!(skip_registration(Some([1; 32]), [1; 32]));
}
'''
pathlib.Path(sys.argv[2]).write_text(fixture.replace("GUARD", guard))
PY
rustc --test "$TMP/guard.rs" -o "$TMP/guard-tests"
"$TMP/guard-tests"
