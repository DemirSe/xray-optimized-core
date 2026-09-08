// ponytail: one tool (action enum) instead of four tools; one core module, no framework.
// Pure logic for the fixed-target Pi SSH tools. No network, no Pi imports:
// the transport (runRemote) is injected so tests use a fake. Node stdlib only.
import { createHash, randomBytes } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, renameSync, rmdirSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// Fixed reviewed read-only status command. Never agent-supplied.
export const STATUS_COMMAND = "uname -srm; uptime; systemctl is-system-running";

// Hardened SSH options. No agent-supplied host/identity/options ever.
export const SSH_OPTIONS = [
	"-o", "BatchMode=yes",
	"-o", "StrictHostKeyChecking=yes",
	"-o", "PasswordAuthentication=no",
	"-o", "ConnectTimeout=10",
	"-o", "ServerAliveInterval=15",
	"-o", "ServerAliveCountMax=3",
];

export const MAX_COMMAND_BYTES = 16 * 1024;
export const MAX_OUTPUT_BYTES = 32 * 1024; // stored and returned per stream
export const REMOTE_TAIL_BYTES = 16 * 1024;
export const MAX_TRANSPORT_BYTES = 256 * 1024; // local ssh buffer cap per call (stdout+stderr)
export const DEFAULT_DEADLINE_SEC = 600;
export const MAX_DEADLINE_SEC = 3600;
export const MIN_DEADLINE_SEC = 5;
export const APPROVAL_TTL_MS = 10 * 60 * 1000;
// ponytail: ulimit -f is 512B blocks on Linux bash (~1MiB at 2048; ~2MiB if 1K blocks).
// Bounds remote per-stream disk; poll still transfers only the 16KB tail.
export const REMOTE_ULIMIT_BLOCKS = 2048;

export const JOB_ID_RE = /^[A-Za-z0-9_-]{16}$/;
export const DEST_PART_RE = /^[A-Za-z0-9._-]+$/; // user/host charset guard (ssh arg injection)

export class ConnectionError extends Error {}
export class BlockedError extends Error {}

export function genJobId() {
	return randomBytes(12).toString("base64url");
}

export function assertJobId(id) {
	if (typeof id !== "string" || !JOB_ID_RE.test(id)) throw new BlockedError(`invalid job id`);
	return id;
}

export function unitFor(id) {
	return `pi-job-${assertJobId(id)}.service`;
}

// ponytail: single quotes with '\'' escape; script goes over stdin so no remote re-quoting.
export function shQuote(s) {
	return `'${String(s).replace(/'/g, `'\\''`)}'`;
}

// Mask host material + common secret shapes BEFORE any persistence/return.
// Honest ceiling: patterns, not perfect generic secret detection.
export function maskSecrets(text, extraTerms = []) {
	let out = String(text ?? "");
	for (const t of extraTerms) {
		if (typeof t === "string" && t) out = out.split(t).join("[redacted-host]");
	}
	out = out.replace(/-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----/g, "[redacted-private-key]");
	out = out.replace(/\bAKIA[0-9A-Z]{16}\b/g, "[redacted-aws-key]");
	out = out.replace(/\b(ghp|gho|ghu|github_pat)_[A-Za-z0-9_]{8,}\b/g, "[redacted-token]");
	out = out.replace(/\b(sk-ant|sk)-[A-Za-z0-9-]{8,}\b/g, "[redacted-api-key]");
	out = out.replace(/\bxox[baprs]-[A-Za-z0-9-]{8,}\b/g, "[redacted-slack-token]");
	out = out.replace(/(password|passwd|secret|token)\s*[:=]\s*\S+/gi, "$1=[redacted]");
	return out;
}

// Keep the tail (logs: the end matters). Returns { text, truncated }.
export function boundOutput(s) {
	const str = String(s ?? "");
	if (Buffer.byteLength(str) <= MAX_OUTPUT_BYTES) return { text: str, truncated: false };
	const buf = Buffer.from(str);
	const tail = buf.subarray(buf.length - MAX_OUTPUT_BYTES);
	// Avoid splitting a UTF-8 sequence: drop up to 3 leading continuation bytes.
	let start = 0;
	while (start < 3 && start < tail.length && (tail[start] & 0xc0) === 0x80) start++;
	return { text: tail.subarray(start).toString("utf8"), truncated: true };
}

// Transport budget check shared by the real ssh runner and tests.
// Throws ConnectionError when the next chunk would exceed the cap.
export function checkTransportBudget(totalBytes, addBytes) {
	if (totalBytes + addBytes > MAX_TRANSPORT_BYTES) {
		throw new ConnectionError("transport over limit; link treated as lost until reconciled");
	}
}

// Config-file mode guard: private config must not be group/other-readable.
export function assertConfigPerms(mode) {
	if ((mode & 0o077) !== 0) throw new BlockedError("private SSH config must be mode 0600");
}

export function approvalHash(target, command) {
	return createHash("sha256").update(`${target}\0${command}`, "utf8").digest("hex");
}

export function nonce() {
	return randomBytes(6).toString("hex");
}

// Remote per-job layout under $HOME/.local/share/pi-jobs/<id>/ (transient, per-job only).
// NOTE: raw remote files hold UNMASKED output (trusted remote, same-user boundary);
// local state holds MASKED tails only. See README trust section.
export function jobDirFor(id) {
	return `$HOME/.local/share/pi-jobs/${assertJobId(id)}`;
}

// Minimal cross-process mutex: atomic mkdir + stale-lock expiry. Lock is held only
// for short read-modify-write sections, never across remote calls.
// Same-user boundary: any local process as you can take/remove this lock; it stops
// accidents/races, not malicious same-user code.
export function acquireLockSync(stateDir, timeoutMs = 5000) {
	ensurePrivateDir(stateDir);
	const p = join(stateDir, ".lock");
	const start = Date.now();
	for (;;) {
		try {
			mkdirSync(p, { mode: 0o700 });
			try { chmodSync(p, 0o700); } catch {}
			return;
		} catch (e) {
			if (e?.code !== "EEXIST") throw e;
			try {
				const st = statSync(p);
				if (Date.now() - st.mtimeMs > 10000) { try { rmdirSync(p); } catch {} continue; }
			} catch {}
			if (Date.now() - start > timeoutMs) throw new BlockedError("state locked; retry");
			try { Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 20); } catch {}
		}
	}
}

export function releaseLockSync(stateDir) {
	try { rmdirSync(join(stateDir, ".lock")); } catch {}
}

export function withLockSync(stateDir, fn) {
	acquireLockSync(stateDir);
	try { return fn(); } finally { releaseLockSync(stateDir); }
}

export function buildPrecheckScript() {
	// Binaries alone prove nothing about the user bus/lingering: require both markers.
	return [
		"command -v systemd-run >/dev/null 2>&1 && command -v systemctl >/dev/null 2>&1 && echo PIJOB-OK || echo PIJOB-NO-SYSTEMD",
		"systemctl --user show-environment >/dev/null 2>&1 && echo PIJOB-BUS-OK || echo PIJOB-NO-BUS",
	].join("\n");
}

// Launch under a transient systemd unit: RuntimeMaxSec is the remote deadline
// (survives local disconnect). KillMode=control-group asks systemd to kill
// descendants; verified only by stop/poll round trips, not by local analogues.
// D uses double-quoted $HOME so it expands in the launch shell; inner uses the
// same literal $HOME so it expands identically in the unit shell (same user).
// ulimit -f bounds remote log disk before poll tails bound transfer.
export function buildLaunchScript({ id, command, deadlineSec }) {
	assertJobId(id);
	const unit = unitFor(id);
	const sub = `.local/share/pi-jobs/${id}`;
	const inner = [
		`ulimit -f ${REMOTE_ULIMIT_BLOCKS} 2>/dev/null || true`,
		`bash "$HOME"/${sub}/cmd >"$HOME"/${sub}/stdout.log 2>"$HOME"/${sub}/stderr.log; code=$?`,
		`printf '%s' "$code" >"$HOME"/${sub}/exitcode`,
		`chmod 600 "$HOME"/${sub}/stdout.log "$HOME"/${sub}/stderr.log "$HOME"/${sub}/exitcode 2>/dev/null || true`,
	].join("; ");
	return [
		"set -e -u",
		': "${HOME:?}"',
		"umask 077",
		`D="$HOME"/${sub}`,
		`U=${shQuote(unit)}`,
		'mkdir -p "$D"',
		'chmod 700 "$D"',
		`printf '%s' ${shQuote(command)} > "$D/cmd"`,
		'chmod 600 "$D/cmd"',
		': > "$D/stdout.log"; chmod 600 "$D/stdout.log"',
		': > "$D/stderr.log"; chmod 600 "$D/stderr.log"',
		`rm -f "$D/exitcode"`,
		`systemd-run --user --unit="$U" --collect -p RuntimeMaxSec=${Math.floor(deadlineSec)} -p KillMode=control-group -- bash -c ${shQuote(inner)}`,
	].join("\n");
}

// One poll round trip. Random token per call so job output cannot spoof section markers.
// OUTSIZE/ERRSIZE carry full remote sizes so the 16KB tail yields a true truncated flag.
export function buildPollScript({ id, token }) {
	assertJobId(id);
	const unit = unitFor(id);
	const sub = `.local/share/pi-jobs/${id}`;
	return [
		"set -u",
		': "${HOME:?}"',
		`U=${shQuote(unit)}`,
		`D="$HOME"/${sub}`,
		`T=${shQuote(token)}`,
		`printf '%s\\n' "$T-SHOW"`,
		'systemctl --user show "$U" 2>/dev/null || printf "NO-UNIT\\n"',
		`printf '%s\\n' "$T-EXIT"`,
		'cat "$D/exitcode" 2>/dev/null || printf "NONE\\n"',
		`printf '\\n%s\\n' "$T-OUT"`,
		`tail -c ${REMOTE_TAIL_BYTES} "$D/stdout.log" 2>/dev/null || true`,
		`printf '\\n%s\\n' "$T-OUTSIZE"`,
		'wc -c < "$D/stdout.log" 2>/dev/null || printf "0\\n"',
		`printf '\\n%s\\n' "$T-ERR"`,
		`tail -c ${REMOTE_TAIL_BYTES} "$D/stderr.log" 2>/dev/null || true`,
		`printf '\\n%s\\n' "$T-ERRSIZE"`,
		'wc -c < "$D/stderr.log" 2>/dev/null || printf "0\\n"',
		`printf '\\n%s\\n' "$T-END"`,
	].join("\n");
}

export function buildStopScript({ id }) {
	assertJobId(id);
	const unit = unitFor(id);
	return [
		"set -u",
		`U=${shQuote(unit)}`,
		'systemctl --user stop "$U" 2>/dev/null || true',
		'systemctl --user kill -s KILL "$U" 2>/dev/null || true',
		'echo PIJOB-STOPPED',
	].join("\n");
}

export function section(text, token, name) {
	const start = text.indexOf(`${token}-${name}`);
	if (start < 0) return null;
	const keys = ["SHOW", "EXIT", "OUT", "OUTSIZE", "ERR", "ERRSIZE", "END"];
	const rest = keys.filter((k) => k !== name)
		.map((k) => text.indexOf(`${token}-${k}`, start + 1))
		.filter((i) => i >= 0);
	const end = rest.length ? Math.min(...rest) : text.length;
	return text.slice(start + `${token}-${name}`.length, end);
}

export function parseShow(showText) {
	const out = {};
	if (!showText) return out;
	for (const line of showText.split("\n")) {
		const m = line.match(/^(ActiveState|SubState|Result|ExecMainStatus|LoadState)=(\S.*)?$/);
		if (m) out[m[1]] = (m[2] ?? "").trim();
	}
	return out;
}

function parseSize(s) {
	const n = Number(String(s ?? "").trim().split(/\s+/)[0]);
	return Number.isFinite(n) && n >= 0 ? Math.floor(n) : 0;
}

// Real wrapper parsing shared by manager and tests (not a toy reimplementation).
export function parsePollOutput(stdout, token) {
	const show = section(stdout, token, "SHOW");
	const exitRaw = section(stdout, token, "EXIT");
	const outRaw = section(stdout, token, "OUT");
	const outSizeRaw = section(stdout, token, "OUTSIZE");
	const errRaw = section(stdout, token, "ERR");
	const errSizeRaw = section(stdout, token, "ERRSIZE");
	if (show === null || exitRaw === null || outRaw === null || errRaw === null
		|| outSizeRaw === null || errSizeRaw === null) {
		throw new BlockedError("unparseable poll output");
	}
	const props = parseShow(show);
	const exitText = exitRaw.trim();
	const exitCode = /^\d+$/.test(exitText) ? Number(exitText) : null;
	const stdoutTail = outRaw.replace(/^\n/, "");
	const stderrTail = errRaw.replace(/^\n/, "");
	const outSize = Math.max(parseSize(outSizeRaw), Buffer.byteLength(stdoutTail));
	const errSize = Math.max(parseSize(errSizeRaw), Buffer.byteLength(stderrTail));
	const truncated = outSize > Buffer.byteLength(stdoutTail) || errSize > Buffer.byteLength(stderrTail);
	return { props, exitCode, stdoutTail, stderrTail, outSize, errSize, truncated };
}

export function classifyPoll({ props, exitCode }, locallyStopped) {
	if (locallyStopped) return "cancelled";
	if (exitCode !== null) return exitCode === 0 ? "completed" : "failed";
	// --collect may erase the unit after timeout: only claim timed_out while the
	// manager still reports Result=timeout on a non-active unit. Gone unit => unknown.
	if (props.Result === "timeout" && props.ActiveState !== "active") return "timed_out";
	if (props.ActiveState === "active") return "running";
	if (props.ActiveState === "failed") return "failed";
	if (props.Result === "success" && props.ActiveState === "inactive") return "failed"; // exited, code file lost
	return "unknown"; // NO-UNIT / LoadState=not-found / anything else: fail closed, never assume
}

function ensurePrivateDir(dir) {
	mkdirSync(dir, { recursive: true, mode: 0o700 });
	try { chmodSync(dir, 0o700); } catch {}
}

function writePrivateFile(path, data) {
	writeFileSync(path, data, { mode: 0o600 });
	try { chmodSync(path, 0o600); } catch {}
}

function assertApprovalArgs({ command, target, maxDeadlineSec }) {
	if (typeof command !== "string" || !command.trim()) throw new BlockedError("empty command");
	if (Buffer.byteLength(command) > MAX_COMMAND_BYTES) throw new BlockedError("command too large");
	maxDeadlineSec = Math.floor(Number(maxDeadlineSec));
	if (!Number.isFinite(maxDeadlineSec) || maxDeadlineSec < MIN_DEADLINE_SEC || maxDeadlineSec > MAX_DEADLINE_SEC) {
		throw new BlockedError(`maxDeadline ${MIN_DEADLINE_SEC}..${MAX_DEADLINE_SEC}s`);
	}
	if (typeof target !== "string" || !target) throw new BlockedError("empty target");
	return maxDeadlineSec;
}

export class ApprovalStore {
	constructor(stateDir) {
		this.dir = stateDir;
		this.file = join(stateDir, "approvals.json");
		ensurePrivateDir(stateDir);
	}
	load() {
		if (!existsSync(this.file)) return [];
		try {
			const v = JSON.parse(readFileSync(this.file, "utf8"));
			return Array.isArray(v) ? v : [];
		} catch {
			return []; // corrupt approvals fail closed: none usable
		}
	}
	save(list) {
		writePrivateFile(this.file, JSON.stringify(list, null, 2));
	}
	// Owner-issued exact-command/target/deadline-bound one-shot approval.
	// Human-mediated (TUI confirm or owner /server-approve in the main session);
	// the file itself is same-user writable, so this is a workflow guard, not an
	// OS boundary. There is deliberately no agent-settable `approved` tool param.
	approve({ command, target, maxDeadlineSec, ttlMs = APPROVAL_TTL_MS, now = Date.now() }) {
		maxDeadlineSec = assertApprovalArgs({ command, target, maxDeadlineSec });
		return withLockSync(this.dir, () => {
			const list = this.load();
			list.push({
				hash: approvalHash(target, command),
				target, maxDeadlineSec, used: false,
				expiresAt: now + ttlMs,
			});
			this.save(list);
		});
	}
	// Returns true once; concurrent double-consume fails closed via fs lock.
	consume({ command, target, deadlineSec, now = Date.now() }) {
		return withLockSync(this.dir, () => {
			const list = this.load();
			const want = approvalHash(target, command);
			const hit = list.find((a) => !a.used && a.hash === want && a.target === target
				&& a.expiresAt > now && Number(deadlineSec) <= Number(a.maxDeadlineSec));
			if (!hit) return false;
			hit.used = true;
			this.save(list);
			return true;
		});
	}
}

export class JobManager {
	// runRemote(script, { timeoutMs }) -> { stdout, stderr, exitCode }; throws ConnectionError on lost link.
	constructor({ stateDir, runRemote, target, now = Date.now }) {
		this.stateDir = stateDir;
		this.runRemote = runRemote;
		this.target = target;
		this.now = now;
		this.jobsCorrupt = false;
		ensurePrivateDir(stateDir);
		this.jobs = this.loadJobs();
	}
	jobsFile() { return join(this.stateDir, "jobs.json"); }
	loadJobs() {
		if (!existsSync(this.jobsFile())) return {};
		try {
			const v = JSON.parse(readFileSync(this.jobsFile(), "utf8"));
			if (!v || typeof v !== "object" || Array.isArray(v)) throw new Error("bad registry");
			return v;
		} catch {
			// Fail closed: quarantine, keep reservation (block new launches), never reset to empty.
			try { renameSync(this.jobsFile(), join(this.stateDir, `jobs.corrupt.${this.now()}.json`)); } catch {}
			this.jobsCorrupt = true;
			return {};
		}
	}
	readDiskOrThrow() {
		if (this.jobsCorrupt) throw new BlockedError("job registry corrupt; owner must reconcile remote jobs before new launches");
		if (!existsSync(this.jobsFile())) return {};
		try {
			const v = JSON.parse(readFileSync(this.jobsFile(), "utf8"));
			if (!v || typeof v !== "object" || Array.isArray(v)) throw new Error("bad registry");
			return v;
		} catch {
			try { renameSync(this.jobsFile(), join(this.stateDir, `jobs.corrupt.${this.now()}.json`)); } catch {}
			this.jobsCorrupt = true;
			throw new BlockedError("job registry corrupt; owner must reconcile remote jobs before new launches");
		}
	}
	assertUsable() {
		if (this.jobsCorrupt) throw new BlockedError("job registry corrupt; owner must reconcile remote jobs before new launches");
	}
	saveJobs() {
		writePrivateFile(this.jobsFile(), JSON.stringify(this.jobs, null, 2));
	}
	logPath(id, stream) {
		return join(this.stateDir, `${id}.${stream}.log`);
	}
	runningJob() {
		// Target-scoped: another target's leftover never blocks this one. Unknown
		// reservations count: only reconciliation clears them.
		return Object.values(this.jobs).find((j) => (j.status === "running" || j.status === "unknown") && j.target === this.target) ?? null;
	}
	get(id) {
		const j = this.jobs[assertJobId(id)];
		if (!j) throw new BlockedError(`unknown job id`);
		if (j.target !== this.target) throw new BlockedError(`cross-target job rejected`);
		return j;
	}
	maskTermsFor(job) {
		return [this.target, ...((job && Array.isArray(job.maskTerms) ? job.maskTerms : []).filter((t) => typeof t === "string" && t))];
	}
	async runStatus() {
		const started = this.now();
		try {
			const r = await this.runRemote(STATUS_COMMAND, { timeoutMs: 25000 });
			const duration = this.now() - started;
			const stdout = boundOutput(maskSecrets(r.stdout, [this.target]));
			const stderr = boundOutput(maskSecrets(r.stderr, [this.target]));
			return {
				status: r.exitCode === 0 ? "ok" : "failed",
				exit_code: r.exitCode, duration_ms: duration,
				stdout: stdout.text, stderr: stderr.text,
				truncated: stdout.truncated || stderr.truncated,
				remote_known: true,
			};
		} catch (e) {
			if (e instanceof ConnectionError) {
				return { status: "unknown", exit_code: null, duration_ms: this.now() - started, stdout: "", stderr: "", truncated: false, remote_known: false, note: "link lost; unknown until reconciled" };
			}
			throw e;
		}
	}
	async exec({ command, deadlineSec = DEFAULT_DEADLINE_SEC, maskTerms = [] }) {
		if (typeof command !== "string" || !command.trim()) throw new BlockedError("empty command");
		if (Buffer.byteLength(command) > MAX_COMMAND_BYTES) throw new BlockedError("command too large");
		deadlineSec = Math.floor(Number(deadlineSec));
		if (!Number.isFinite(deadlineSec) || deadlineSec < MIN_DEADLINE_SEC || deadlineSec > MAX_DEADLINE_SEC) {
			throw new BlockedError(`deadline ${MIN_DEADLINE_SEC}..${MAX_DEADLINE_SEC}s`);
		}
		this.assertUsable();
		const cleanTerms = (Array.isArray(maskTerms) ? maskTerms : []).filter((t) => typeof t === "string" && t).slice(0, 16);
		// Locked reserve: reload under lock so concurrent managers see each other.
		let id;
		withLockSync(this.stateDir, () => {
			this.jobs = this.readDiskOrThrow();
			const busy = this.runningJob();
			if (busy) throw new BlockedError(`target busy (${busy.id}); status/stop still work`);
			do { id = genJobId(); } while (this.jobs[id]);
			const now = this.now();
			this.jobs[id] = {
				id, target: this.target, status: "running",
				exit_code: null, createdAt: now, startedAt: now, endedAt: null,
				deadlineSec, locallyStopped: false, maskTerms: cleanTerms,
				commandMasked: maskSecrets(command, [this.target, ...cleanTerms]),
			};
			this.saveJobs();
		});
		const job = this.jobs[id];
		// Precheck native remote prerequisites + user bus: no weaker fallback, blocked if absent.
		let pre;
		try {
			pre = await this.runRemote(buildPrecheckScript(), { timeoutMs: 25000 });
		} catch (e) {
			if (e instanceof ConnectionError) return this.report(id, "unknown", "link lost during launch; unknown until reconciled");
			throw e;
		}
		if (!pre.stdout.includes("PIJOB-OK") || !pre.stdout.includes("PIJOB-BUS-OK")) {
			withLockSync(this.stateDir, () => {
				this.jobs = this.readDiskOrThrow();
				if (this.jobs[id]) { this.jobs[id].status = "blocked"; this.jobs[id].endedAt = this.now(); this.saveJobs(); }
			});
			return this.report(id, "blocked", "remote lacks systemd-run/systemctl or user bus (lingering/bus); no weaker fallback");
		}
		// Checked launch: inspect exit/receipt before publishing running.
		let launch;
		try {
			launch = await this.runRemote(buildLaunchScript({ id, command, deadlineSec }), { timeoutMs: 25000 });
		} catch (e) {
			if (e instanceof ConnectionError) return this.report(id, "unknown", "link lost during launch; unknown until reconciled");
			throw e;
		}
		const unit = unitFor(id);
		const receipt = `${launch.stdout ?? ""}\n${launch.stderr ?? ""}`;
		if (launch.exitCode !== 0 || !receipt.includes(unit)) {
			const why = maskSecrets(String(launch.stderr ?? launch.stdout ?? "").slice(-500), [this.target]);
			withLockSync(this.stateDir, () => {
				this.jobs = this.readDiskOrThrow();
				if (this.jobs[id]) { this.jobs[id].status = "blocked"; this.jobs[id].endedAt = this.now(); this.saveJobs(); }
			});
			return this.report(id, "blocked", `launch receipt missing (exit ${launch.exitCode}): ${why}`);
		}
		// Re-merge under lock so a concurrent manager's jobs are not overwritten.
		withLockSync(this.stateDir, () => {
			const disk = this.readDiskOrThrow();
			if (!disk[id]) disk[id] = job;
			this.jobs = disk;
			this.saveJobs();
		});
		return this.report(id, "running", `launched under transient unit ${unit}, remote deadline ${deadlineSec}s`);
	}
	async poll(id) {
		this.assertUsable();
		withLockSync(this.stateDir, () => { this.jobs = this.readDiskOrThrow(); });
		const job = this.get(id);
		if (job.status !== "running") return this.report(id, job.status);
		const token = `PIJOB-${nonce()}`;
		let r;
		try {
			r = await this.runRemote(buildPollScript({ id, token }), { timeoutMs: 25000 });
		} catch (e) {
			if (e instanceof ConnectionError) return this.report(id, "unknown", "link lost; unknown until reconciled");
			throw e;
		}
		const parsed = parsePollOutput(r.stdout, token);
		const status = classifyPoll(parsed, job.locallyStopped);
		this.storeTails(job, parsed);
		if (status === "running" || status === "unknown") {
			if (status === "unknown") return this.report(id, "unknown", "remote state unclear; not rerun", parsed);
			return this.report(id, "running", undefined, parsed);
		}
		withLockSync(this.stateDir, () => {
			this.jobs = this.readDiskOrThrow();
			const cur = this.jobs[id];
			if (!cur || cur.status !== "running") return;
			cur.status = status;
			cur.exit_code = parsed.exitCode;
			cur.endedAt = this.now();
			cur.truncated = !!(cur.truncated || parsed.truncated);
			this.saveJobs();
		});
		return this.report(id, status, undefined, parsed);
	}
	async stop(id) {
		this.assertUsable();
		withLockSync(this.stateDir, () => { this.jobs = this.readDiskOrThrow(); });
		const job = this.get(id); // validates format, ownership, target
		if (job.status !== "running") return this.report(id, job.status);
		try {
			await this.runRemote(buildStopScript({ id }), { timeoutMs: 25000 });
		} catch (e) {
			if (e instanceof ConnectionError) return this.report(id, "unknown", "link lost during stop; unknown until reconciled");
			throw e;
		}
		// Verify instead of optimistic cancelled: one poll round trip.
		const token = `PIJOB-${nonce()}`;
		let r;
		try {
			r = await this.runRemote(buildPollScript({ id, token }), { timeoutMs: 25000 });
		} catch (e) {
			if (e instanceof ConnectionError) return this.report(id, "unknown", "link lost during stop verify; unknown until reconciled");
			throw e;
		}
		let parsed;
		try { parsed = parsePollOutput(r.stdout, token); }
		catch { parsed = null; }
		if (parsed) this.storeTails(job, parsed); // keep prior tails when verify is unparseable
		if (!parsed) return this.report(id, "unknown", "stop verify unparseable; unknown until reconciled");
		if (parsed.exitCode === null && parsed.props.ActiveState === "active") {
			return this.report(id, "running", `stop requested for unit ${unitFor(id)} but unit still active; deadline still enforces`, parsed);
		}
		withLockSync(this.stateDir, () => {
			this.jobs = this.readDiskOrThrow();
			const cur = this.jobs[id];
			if (!cur || cur.status !== "running") return;
			cur.locallyStopped = true;
			cur.status = "cancelled";
			cur.endedAt = this.now();
			this.saveJobs();
		});
		return this.report(id, "cancelled", `unit ${unitFor(id)} stop verified (exit or inactive)`);
	}
	storeTails(job, parsed) {
		const terms = this.maskTermsFor(job);
		const out = boundOutput(maskSecrets(parsed.stdoutTail, terms));
		const err = boundOutput(maskSecrets(parsed.stderrTail, terms));
		writePrivateFile(this.logPath(job.id, "stdout"), out.text);
		writePrivateFile(this.logPath(job.id, "stderr"), err.text);
		job.truncated = !!(out.truncated || err.truncated || parsed.truncated);
	}
	report(id, status, note, parsed) {
		const job = this.jobs[id];
		let stdout = "", stderr = "", truncated = !!job.truncated;
		if (parsed) {
			const terms = this.maskTermsFor(job);
			const out = boundOutput(maskSecrets(parsed.stdoutTail, terms));
			const err = boundOutput(maskSecrets(parsed.stderrTail, terms));
			stdout = out.text; stderr = err.text;
			truncated = truncated || out.truncated || err.truncated || !!parsed.truncated;
		} else if (job.status !== "running") {
			try {
				if (existsSync(this.logPath(id, "stdout"))) stdout = readFileSync(this.logPath(id, "stdout"), "utf8");
				if (existsSync(this.logPath(id, "stderr"))) stderr = readFileSync(this.logPath(id, "stderr"), "utf8");
			} catch { /* bounded best-effort re-read */ }
		}
		const duration = (job.endedAt ?? this.now()) - job.startedAt;
		const body = {
			job_id: id, status,
			exit_code: status === "completed" || status === "failed" ? (job.exit_code ?? parsed?.exitCode ?? null) : null,
			duration_ms: duration, stdout, stderr, truncated,
			log_reference: this.logPath(id, "stdout"),
			remote_known: status !== "unknown",
			...(note ? { note } : {}),
		};
		return body;
	}
}
