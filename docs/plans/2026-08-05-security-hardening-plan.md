# Security hardening plan

## Contract

- Scope: redact sink dry-run cookie values, create sidecar SQLite files with
  private permissions before writes, add an opt-in fail-closed allowlist
  invariant for `agent-sync`, correct the pairing threat-model comment, expose
  an `agent-sync` capability contract, and align signing-identity documentation.
- Documentation debt in scope: update the protocol version note to v2 and make
  the README test-count claim non-exact.
- Non-goals: cmux behavior, the external workspace wrapper, dependency or
  version changes, protocol behavior changes, and unrelated cleanup.
- Preserved behavior: every existing command keeps its defaults; missing
  `blocklist.yaml` continues to mean sync-all unless the new
  `--require-policy=allowlist` flag is supplied; v1 sync envelopes remain
  accepted.
- Authorized changes: `sink --dry-run` redacts cookie values unless
  `--dry-run-values` is explicitly supplied; `agent-sync` fails before launch
  and on every reload cycle when the allowlist invariant is requested but not
  active.

## Evidence and slices

Baseline on local `main` (`3a5c8ba`): `go vet ./...` passed and
`go test -race ./...` passed 816 tests across 22 packages.

1. Add redacted dry-run serialization and focused output tests.
2. Pre-create the sidecar temp database as `0600`, tighten its immediate parent
   to `0700`, and add permission tests.
3. Add the allowlist invariant and capability JSON with startup/reload and
   output-contract tests.
4. Correct the pairing and signing documentation plus the two stale version and
   count notes.
5. Run focused checks after each slice, then `gofmt`, `go vet ./...`, and
   `go test -race ./...` before the cumulative branch-close review.

The workspace Oracle council was unavailable before implementation because its
required Codex lane is self-ineligible from Codex and the installed council
manifest helper is missing. This is recorded as `INCOMPLETE`; Oracle is advisory
and does not replace the independent branch-close review gate.

## Stop and rollback

Stop if default sync behavior changes, a policy error can still launch Chrome,
cookie values appear in default dry-run output, sidecar contents are written
before private permissions exist, or full verification regresses. Each slice is
small enough to revert independently before commit.
