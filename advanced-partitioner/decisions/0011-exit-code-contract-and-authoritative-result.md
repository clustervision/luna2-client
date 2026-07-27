# 0011 — Exit-code contract end-to-end; the pipeline result is authoritative

Date: 2026-07-10 · Status: accepted

## Context
The boot wrapper (`95luna/luna-installer.sh`) decides retry-vs-park on the node-installer's
exit code, and provisioning marks success on exit 0. The FTA (Cluster B) found the chain broken
in both directions: sub-tool exit codes were flattened to strings, the TUI hardcoded exit 1, the
data-fence halt exited 1 (so a permanent refusal retried every 30 s forever), and the terminal
outcome rode a lossy drop-oldest event bus — a dropped event, an operator quit, or SIGINT could
report exit 0 on a failed or half-finished install.

## Decision
- **Codes:** sub-tools keep `0 = success, 1 = retryable failure, 2 = safety refusal`
  (`cli.ExitSafetyRefusal`: data fence, persistent protection, device overlap, unreadable
  storage-result, identity mismatch). `lpart-node-installer` maps sub-tool 2/3 → **exit 3**
  (`exitFatalConfig`); the wrapper parks on **2 and 3** and retries everything else.
- **Typed propagation:** `pipeline.stepError` carries the child's numeric code;
  `pipeline.FailureClass` rides `EvInstallerFailed.Data`; renderers map fatal → 3.
- **Authoritative result:** the pipeline records `Result{Done,Failed,Class}` on itself BEFORE
  closing the event channel; `lpart-node-installer` derives its exit from `Pipeline.Result()`,
  never from the rendered stream. Stream-ended-without-terminal-event and ctx-cancel are
  failures under the node-installer's strict renderer semantics (the standalone `lpart-tui`
  viewer stays lenient).

## Consequences
A permanent failure parks the node for an operator instead of re-running the destructive
pipeline indefinitely (which multiplied every data-loss risk); an interrupted install can never
be recorded as provisioned. Pinned by `pipeline/result_test.go`, `installerui/strict_test.go`,
and the storage-prepare exit-2 e2e tests.
