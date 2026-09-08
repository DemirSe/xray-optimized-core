// Fixed-target Pi SSH tools: server_status / server_exec / job_status / job_stop
// as one compact `server` tool + owner `/server-approve` command.
// No live SSH at import; transport spawns only inside tool execution.
import { spawn } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, readFileSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { StringEnum } from "@earendil-works/pi-ai";
import { Type } from "typebox";
import {
	ApprovalStore,
	BlockedError,
	ConnectionError,
	DEFAULT_DEADLINE_SEC,
	DEST_PART_RE,
	JobManager,
	SSH_OPTIONS,
	assertConfigPerms,
	checkTransportBudget,
} from "./core.mjs";

const CONFIG_PATH = join(homedir(), ".pi", "agent", "server-ssh.json");
const STATE_DIR = join(homedir(), ".pi", "agent", "server-jobs");

function loadConfig(): { host: string; user: string; port?: number; keyPath?: string; autoApproveExec: boolean } {
	if (!existsSync(CONFIG_PATH)) {
		throw new BlockedError(`no private SSH config at ${CONFIG_PATH} (0600, outside git). Create {"host","user"} there.`);
	}
	assertConfigPerms(statSync(CONFIG_PATH).mode & 0o777);
	const raw = JSON.parse(readFileSync(CONFIG_PATH, "utf8"));
	if (!DEST_PART_RE.test(raw.user ?? "") || !DEST_PART_RE.test(raw.host ?? "")) {
		throw new BlockedError("private config user/host failed charset guard");
	}
	// ponytail: unknown keys ignored; only autoApproveExec=true opts out of per-command approval.
	return { host: raw.host, user: raw.user, port: raw.port, keyPath: raw.keyPath, autoApproveExec: raw.autoApproveExec === true };
}

// Real transport: argv array only (no shell), script over stdin (argv-safe quoting).
// Bounded buffers: over MAX_TRANSPORT_BYTES the local ssh is killed and the call
// reconciles as lost-link unknown. Local abort kills local ssh only; the remote
// job keeps its systemd deadline (honest).
function makeRunner(dest: string, keyPath?: string, port?: number) {
	const base = [...SSH_OPTIONS];
	if (port) base.push("-p", String(Math.floor(port)));
	if (keyPath) base.push("-i", keyPath);
	return (script: string, opts: { timeoutMs: number }) =>
		new Promise<{ stdout: string; stderr: string; exitCode: number }>((resolve, reject) => {
			const child = spawn("ssh", [...base, "--", dest, "bash", "-s"], { stdio: ["pipe", "pipe", "pipe"] });
			let stdout = "", stderr = "", bytes = 0, settled = false;
			const fail = (e: Error) => { if (!settled) { settled = true; clearTimeout(timer); try { child.kill("KILL"); } catch {} reject(e); } };
			const timer = setTimeout(() => fail(new ConnectionError("ssh timed out")), opts.timeoutMs);
			child.stdout.on("data", (d) => {
				try { checkTransportBudget(bytes, (d as Buffer).length); } catch (e) { fail(e as Error); return; }
				bytes += (d as Buffer).length; stdout += d;
			});
			child.stderr.on("data", (d) => {
				try { checkTransportBudget(bytes, (d as Buffer).length); } catch (e) { fail(e as Error); return; }
				bytes += (d as Buffer).length; stderr += d;
			});
			child.on("error", (e) => fail(new ConnectionError(`ssh spawn: ${(e as Error).message}`)));
			child.stdin.on("error", (e) => fail(new ConnectionError(`ssh stdin: ${(e as Error).message}`)));
			child.on("close", (code) => {
				if (settled) return;
				settled = true; clearTimeout(timer);
				// exit 255 = ssh-level failure (lost link), never a job result
				if (code === 255) reject(new ConnectionError(`ssh link failed: ${stderr.slice(-500)}`));
				else resolve({ stdout, stderr, exitCode: code ?? 1 });
			});
			child.stdin.write(script);
			child.stdin.end();
		});
}

export default function (pi: ExtensionAPI) {
	pi.registerTool({
		name: "server",
		label: "Server",
		description: "Fixed-target SSH tools. server_status is read-only. server_exec needs explicit owner approval (TUI confirm, else one-shot /server-approve) unless owner enabled private-config autoApproveExec. job_status/job_stop track bounded async jobs. Lost link => unknown until reconciled; no auto wake.",
		parameters: Type.Object({
			action: StringEnum(["server_status", "server_exec", "job_status", "job_stop"] as const, { description: "Tool action" }),
			command: Type.Optional(Type.String({ description: "Exact shell command (server_exec only; owner-approved)" })),
			job_id: Type.Optional(Type.String({ description: "Opaque job id (job_status/job_stop)" })),
			deadline_sec: Type.Optional(Type.Number({ description: "Remote deadline 5..3600s (server_exec only)" })),
		}),
		async execute(_id, params, _signal, _onUpdate, ctx) {
			let cfg;
			try {
				cfg = loadConfig();
			} catch (e) {
				throw e; // blocked: no config, fail closed
			}
			const target = `${cfg.user}@${cfg.host}`;
			mkdirSync(STATE_DIR, { recursive: true, mode: 0o700 });
			chmodSync(STATE_DIR, 0o700);
			const mgr = new JobManager({ stateDir: STATE_DIR, runRemote: makeRunner(target, cfg.keyPath, cfg.port), target });
			const approvals = new ApprovalStore(STATE_DIR);
			const done = (obj: unknown) => ({
				content: [{ type: "text" as const, text: JSON.stringify(obj) }],
				details: obj as Record<string, unknown>,
			});
			try {
				if (params.action === "server_status") return done(await mgr.runStatus());
				if (params.action === "job_status") return done(await mgr.poll(params.job_id as string));
				if (params.action === "job_stop") return done(await mgr.stop(params.job_id as string));
				// server_exec: explicit owner approval, never agent-controlled.
				// Owner opt-out: private-config autoApproveExec=true skips the confirm/approval gate.
				// Exact-command logging + bounded output + remote deadline still apply in mgr.exec.
				const command = params.command as string;
				const deadline = Math.floor(Number(params.deadline_sec ?? DEFAULT_DEADLINE_SEC));
				if (!cfg.autoApproveExec) {
					if (ctx.hasUI) {
						const ok = await ctx.ui.confirm("Run on fixed server target?", `Exact command:\n${command}\n\nDeadline: ${deadline}s`);
						if (!ok) return done({ job_id: null, status: "approval_required", note: "owner denied confirm; headless path: owner runs /server-approve" });
					} else if (!approvals.consume({ command, target, deadlineSec: deadline })) {
						return done({ job_id: null, status: "approval_required", note: "headless: owner must issue exact-command/target/deadline-bound one-shot via /server-approve in main session" });
					}
				}
				return done(await mgr.exec({ command, deadlineSec: deadline }));
			} catch (e) {
				if (e instanceof BlockedError) return done({ job_id: params.job_id ?? null, status: "blocked", note: (e as Error).message });
				throw e;
			}
		},
	});

	pi.registerCommand("server-approve", {
		description: "Owner: one-shot approve exact command. Usage: /server-approve [maxDeadlineSec] <exact command>",
		handler: async (args, ctx) => {
			const cfg = loadConfig();
			const target = `${cfg.user}@${cfg.host}`;
			const m = String(args ?? "").match(/^\s*(?:(\d+)\s+)?([\s\S]+?)\s*$/);
			if (!m || !m[2]) {
				ctx.ui.notify("Usage: /server-approve [maxDeadlineSec] <exact command>", "error");
				return;
			}
			const maxDeadline = m[1] ? Math.floor(Number(m[1])) : DEFAULT_DEADLINE_SEC;
			try {
				new ApprovalStore(STATE_DIR).approve({ command: m[2], target, maxDeadlineSec: maxDeadline });
			} catch (e) {
				ctx.ui.notify(`Approve rejected: ${(e as Error).message}`, "error");
				return;
			}
			ctx.ui.notify(`One-shot approved (10min, <=${maxDeadline}s): ${m[2].slice(0, 120)}`, "info");
		},
	});
}
