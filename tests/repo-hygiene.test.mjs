// Repository hygiene checks: absent archives, locked constraints, ignore rules,
// and offline test opt-in guards. Local file reads and local git only. No
// network.
import { strict as assert } from "node:assert";
import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const OLD_ROOT_DOCUMENTS = ["PLAN.md", "REPORT.md", "FORK.md", "DECISIONS.md", "SIM.md"];

test("old root documents and docs/history are absent", () => {
	for (const name of OLD_ROOT_DOCUMENTS) {
		assert.ok(!existsSync(join(ROOT, name)), `${name} must not stay at the repository root`);
	}
	assert.ok(!existsSync(join(ROOT, "docs", "history")), "docs/history must be absent");
});

test("locked constraints are current and single-source", () => {
	const text = readFileSync(join(ROOT, "docs", "constraints.md"), "utf8");
	const phrases = [
		"server-only", "whatsapp.net", "no fallback", "owner approval", "stock client",
		"Hiddify", "Android", "Windows", "Linux", "TUN", "no bypass", "shared UUID",
		"three devices", "IPv4 only", "dual-stack", "IPv6", "manual only",
		"Automatic reboot is off", "subdomain substitution",
	];
	for (const phrase of phrases) {
		assert.ok(text.includes(phrase), `docs/constraints.md must state ${phrase}`);
	}
	assert.ok(text.includes("does not verify any current production state"), "production state must stay unverified here");
});

test("obsolete nested fork CI assets are replaced by the root workflow", () => {
	assert.ok(!existsSync(join(ROOT, "fork", "xray-core", ".github")), "nested fork CI assets must be removed");
	const workflow = readFileSync(join(ROOT, ".github", "workflows", "check.yml"), "utf8");
	assert.ok(/permissions:\n  contents: read/.test(workflow), "workflow must grant contents: read only");
	assert.ok(workflow.includes('node-version: "24"'), "workflow must use Node 24");
	assert.ok(workflow.includes('"1.26.x"'), "workflow must use Go 1.26.x");
	assert.ok(workflow.includes("bash scripts/check.sh"), "workflow must run the shared root check");
	assert.ok(!/ssh|deploy/i.test(workflow), "workflow must not touch production");
});

test("the ECH network test is opt-in only for XRAY_TEST_ECH=1", () => {
	const source = readFileSync(
		join(ROOT, "fork", "xray-core", "transport", "internet", "tls", "ech_test.go"),
		"utf8",
	);
	assert.ok(
		source.includes('os.Getenv("XRAY_TEST_ECH") != "1"'),
		"TestECHDial must run only when XRAY_TEST_ECH is exactly 1",
	);
	assert.ok(
		!source.includes('os.Getenv("XRAY_TEST_ECH") == ""'),
		"an empty check would enable any non-empty value, including 0",
	);
	assert.ok(
		source.indexOf('os.Getenv("XRAY_TEST_ECH")') < source.indexOf("cloudflare.com"),
		"the guard must run before the external call",
	);
	const failTest = source.slice(source.indexOf("func TestECHDialFail"));
	assert.ok(failTest.includes("func TestECHDialFail"), "TestECHDialFail must stay in the file");
	assert.ok(!failTest.includes("XRAY_TEST_ECH"), "TestECHDialFail must stay enabled by default");
});

test("PI_TEST_PACKAGE_DIR override is authoritative for the server-tool tests", () => {
	// A fallback installation is present: the server-tool discovery accepts any
	// candidate package dir, and npm_config_prefix makes the stub visible to the
	// child. The real installed package may also be discoverable.
	const work = mkdtempSync(join(tmpdir(), "pi-override-"));
	try {
		const fallbackPrefix = join(work, "fallback-prefix");
		const fallbackPackage = join(fallbackPrefix, "lib", "node_modules", "@earendil-works", "pi-coding-agent");
		mkdirSync(fallbackPackage, { recursive: true });
		writeFileSync(join(fallbackPackage, "package.json"), JSON.stringify({ name: "@earendil-works/pi-coding-agent", version: "0.0.0-test-fallback" }));
		for (const override of [join(work, "missing-package"), ""]) {
			// NODE_TEST_CONTEXT makes a nested `node --test` skip its files and exit 0.
			const childEnv = { ...process.env, PI_TEST_PACKAGE_DIR: override, npm_config_prefix: fallbackPrefix };
			delete childEnv.NODE_TEST_CONTEXT;
			const child = spawnSync(process.execPath, ["--test", "tests/server-tools.test.mjs"], {
				cwd: ROOT,
				encoding: "utf8",
				timeout: 120_000,
				env: childEnv,
			});
			const output = `${child.stdout}\n${child.stderr}`;
			assert.ok(!child.error, `the child server-tool run must finish within the timeout: ${child.error}`);
			assert.notEqual(child.status, 0, `an invalid PI_TEST_PACKAGE_DIR must fail the server-tool tests, never fall back to ${fallbackPackage}:\n${output}`);
			const expectedError = override ? join(override, "package.json") : "PI_TEST_PACKAGE_DIR is set but empty";
			assert.ok(output.includes(expectedError), `the failure must identify the invalid override:\n${output}`);
		}
	} finally {
		rmSync(work, { recursive: true, force: true });
	}
});

test("ignore rules cover generated outputs and never mask tracked files", () => {
	const generated = execFileSync(
		"git",
		[
			"check-ignore", "--no-index",
			"build/xray-trimmed", "build/sha256.txt",
			"xray-trimmed", "sha256.txt",
			"fork/xray-core/xray-trimmed", "fork/xray-core/sha256.txt",
			".pi/subagents/state.json",
		],
		{ cwd: ROOT, encoding: "utf8" },
	);
	assert.deepEqual(
		generated.trim().split("\n").sort(),
		[
			"build/sha256.txt", "build/xray-trimmed",
			".pi/subagents/state.json",
			"fork/xray-core/sha256.txt", "fork/xray-core/xray-trimmed",
			"sha256.txt", "xray-trimmed",
		].sort(),
	);
	// Every tracked file that exists in the working tree must stay visible to
	// git. This covers the fork sources and fixtures, including the fixture
	// unmasked in fork/xray-core/.gitignore.
	const tracked = execFileSync("git", ["ls-files", "-z"], { cwd: ROOT, encoding: "utf8" })
		.split("\0").filter((p) => p && existsSync(join(ROOT, p)));
	assert.ok(tracked.length > 0, "git ls-files must list tracked files");
	assert.ok(
		tracked.includes("fork/xray-core/common/buf/data/test_MultiBufferReadAllToByte.dat"),
		"the fork fixture must stay tracked",
	);
	let ignored = "";
	try {
		ignored = execFileSync("git", ["check-ignore", "--no-index", "--stdin"], {
			cwd: ROOT, encoding: "utf8", input: `${tracked.join("\n")}\n`,
		});
	} catch (e) {
		if (e.status !== 1) throw e;
	}
	assert.equal(ignored, "", `ignore rules mask tracked files:\n${ignored}`);
});
