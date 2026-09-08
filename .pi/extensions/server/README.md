# Server SSH tools (fixed target)

One compact tool `server` (actions: `server_status`, `server_exec`, `job_status`,
`job_stop`) + owner command `/server-approve`. No live SSH during tests.

## Setup (owner, main session)

1. Private config only (never in git/docs/tests):
   `~/.pi/agent/server-ssh.json` mode `0600`, e.g. `{"user":"demir","host":"<ip>"}`.
   Optional `port`, `keyPath`. Optional owner opt-out `autoApproveExec: true` (default
   `false`); unknown keys are ignored. Tool reads nothing else; agent cannot supply host/identity/options.
   Group/other-readable config is refused (`blocked`).
   WARNING: `autoApproveExec: true` lets the agent run arbitrary commands on the
   fixed target with no per-command prompt. Keep it `false` unless the owner
   explicitly accepts that.
2. Project trusts local extensions, so `pi` auto-loads `.pi/extensions/server/index.ts`.
   No new npm/system deps (node stdlib + installed Pi APIs).

## Use

- `server_status` — fixed read-only command, no approval needed.
- `server_exec {command, deadline_sec?}` — needs explicit owner approval by default:
  TUI asks `confirm`; headless/subagent returns `approval_required` until the owner
  runs `/server-approve [maxDeadlineSec] <exact command>` (one-shot, 10 min TTL,
  exact command/target/deadline bound, single use, lock-guarded, reload-safe).
  Owner opt-out: `autoApproveExec: true` in private config skips the TUI confirm
  and the headless `approval_required` gate (exact-command logging, bounded output,
  and remote deadline still apply). WARNING: with auto-approve the agent can run
  arbitrary commands with no per-command prompt.
  Empty/oversize commands and out-of-range deadlines are rejected, never clamped silently.
- `job_status {job_id}` / `job_stop {job_id}` — poll/stop; one live command per
  target (`busy` otherwise, target-scoped; unresolved reservations keep the slot
  until reconciled). `job_stop` verifies with one poll round trip: still-active
  stays `running` ("stop requested; deadline still enforces"), verified-dead
  becomes `cancelled`.
- Jobs run under transient `systemd-run --user` units (`RuntimeMaxSec` = remote
  deadline, survives local disconnect; `KillMode=control-group` asks systemd to
  kill descendants). Launch exit/receipt is checked before `running` is reported.
  Precheck requires binaries AND a reachable user bus (`show-environment`); no bus
  (no lingering/bus) => `blocked`, no weaker fallback. `--collect` may erase the
  unit after timeout, so timeout is claimed only while `Result=timeout` is still
  visible on a non-active unit; a gone unit is `unknown`, never assumed. Lost link
  => `unknown` until `job_status` reconciles; commands are never auto-rerun. No auto
  wake; poll explicitly.
- Bounds: local ssh buffers capped (~256 KB/call, over => lost-link `unknown`);
  remote per-stream disk bounded via `ulimit -f` (~1-2 MB), transfer is the 16 KB
  tail, local store/return is the 32 KB tail. `truncated` is true when the remote
  full size exceeds the transferred tail (sizes via `wc -c`), not just on local cut.
  Logs in `~/.pi/agent/server-jobs/` (0700/0600). Remote per-job dirs accumulate;
  the owner cleans old `$HOME/.local/share/pi-jobs/<id>/` dirs.
- Secrets/host masked by pattern (not perfect): tool masks target + secret patterns
  on command, outputs, and local logs; extra `maskTerms` are core-only
  (`JobManager.exec`), not a tool param.

## Logs: raw remote vs sanitized local (honest)

- Remote `$HOME/.local/share/pi-jobs/<id>/{cmd,stdout.log,stderr.log,exitcode}`
  (0600, dir 0700) holds RAW unmasked output on the trusted remote. Same-user
  boundary applies there too.
- Local `~/.pi/agent/server-jobs/<id>.*.log` (0600) holds MASKED tails only.
  Never paste remote raw logs into chat; treat them as secret-bearing.

## Subagent exposure (explicit, not automatic)

Subagents do not inherit this tool automatically. To allow one, add `server` to
that agent's `tools:` allowlist (e.g. `~/.pi/agent/agents/<name>.md`) and confirm
the run's capability ceiling permits extension tools. Do not edit global profiles
silently; headless subagents still hit `approval_required` without a one-shot.
Verify with `/server-approve` flow in the main session first.

## Trust boundary

This protects the owner from the agent, not from malicious same-user code:
any local process running as you can read `~/.pi/agent/server-ssh.json`,
`server-jobs/approvals.json`, and take the state lock. Owner mediation is
workflow-level (TUI confirm or owner `/server-approve` in the main session);
there is deliberately no agent-settable `approved` tool param. A corrupt
`jobs.json` is quarantined to `jobs.corrupt.*` and all launches block until the
owner reconciles the remote side (fail closed, no silent reset). Local
`setsid` group-kill checks are analogues only, never proof of remote cgroup kill.
