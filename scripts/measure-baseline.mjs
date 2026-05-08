#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const yMonoRoot = resolve(scriptDir, "..");
const workspaceRoot = resolve(yMonoRoot, "..");
const defaultLegacyRoot = join(workspaceRoot, "pi-mono");
const defaultOutputPath = join(yMonoRoot, "docs", "baseline", "measurements-results.jsonl");
const activity = "phase-0-baseline-measurements";

const scenarioDefinitions = new Map([
	[
		"legacy-help",
		{
			description: "Legacy pi CLI cold start without TUI, exiting after --help.",
			mode: "headless",
			product: "pi-mono",
			buildCommand: (options) => ({
				executable: process.execPath,
				args: [legacyCliPath(options), "--help"],
				cwd: legacyPackageDir(options),
				env: benchmarkEnv(options),
			}),
			requirements: (options) => [
				commandRequirement("node runtime", process.execPath),
				fileRequirement("legacy built CLI", legacyCliPath(options), "Run the pi-mono TypeScript build before measuring, or point --legacy-root at a built checkout."),
				rssRequirement(),
			],
		},
	],
	[
		"legacy-rpc",
		{
			description: "Legacy pi RPC cold start without TUI, ready when get_state responds.",
			mode: "headless",
			product: "pi-mono",
			stdin: (runNumber) => `${JSON.stringify({ id: `baseline-${runNumber}`, type: "get_state" })}\n`,
			readyWhen: (line, runNumber) => {
				if (line.trim() === "") {
					return false;
				}
				const parsed = JSON.parse(line);
				return parsed?.type === "response" && parsed?.id === `baseline-${runNumber}` && parsed?.command === "get_state";
			},
			closeStdinOnReady: true,
			buildCommand: (options) => ({
				executable: process.execPath,
				args: [legacyCliPath(options), "--mode", "rpc", "--no-session"],
				cwd: legacyPackageDir(options),
				env: benchmarkEnv(options),
			}),
			requirements: (options) => [
				commandRequirement("node runtime", process.execPath),
				fileRequirement("legacy built CLI", legacyCliPath(options), "Run the pi-mono TypeScript build before measuring, or point --legacy-root at a built checkout."),
				rssRequirement(),
			],
		},
	],
	[
		"control-large-stdout",
		{
			description: "Harness control: stream a large stdout payload from a child process.",
			mode: "large-command",
			product: "harness-control",
			buildCommand: (options) => ({
				executable: process.execPath,
				args: ["-e", largeOutputProgram("stdout", options.largeOutputBytes)],
				cwd: yMonoRoot,
				env: process.env,
			}),
			requirements: () => [
				commandRequirement("node runtime", process.execPath),
				rssRequirement(),
			],
		},
	],
	[
		"control-large-stderr",
		{
			description: "Harness control: stream a large stderr payload from a child process.",
			mode: "large-command",
			product: "harness-control",
			buildCommand: (options) => ({
				executable: process.execPath,
				args: ["-e", largeOutputProgram("stderr", options.largeOutputBytes)],
				cwd: yMonoRoot,
				env: process.env,
			}),
			requirements: () => [
				commandRequirement("node runtime", process.execPath),
				rssRequirement(),
			],
		},
	],
	[
		"candidate-help",
		{
			description: "Future y binary cold start without TUI, exiting after --help.",
			mode: "headless",
			product: "y",
			optional: true,
			buildCommand: (options) => ({
				executable: options.candidateBin,
				args: ["--help"],
				cwd: yMonoRoot,
				env: process.env,
			}),
			requirements: (options) => [
				fileRequirement("candidate y binary from --candidate-bin", options.candidateBin, "Build y first, then pass --candidate-bin /path/to/y."),
				rssRequirement(),
			],
		},
	],
]);

function printHelp() {
	console.log(`Usage:
  node y/scripts/measure-baseline.mjs --list
  node y/scripts/measure-baseline.mjs --check --scenario legacy-rpc
  node y/scripts/measure-baseline.mjs --scenario control-large-stdout --runs 3
  node y/scripts/measure-baseline.mjs --all --output y/docs/baseline/measurements-results.jsonl

Options:
  --list                    List scenarios and exit.
  --check                   Validate dependencies for selected scenarios and exit.
  --all                     Select all non-optional scenarios.
  --scenario <id>           Select one scenario. Can be repeated.
  --runs <n>                Measured runs per scenario. Default: 1.
  --warmup <n>              Warmup runs before measured runs. Default: 0.
  --timeout-ms <n>          Per-run timeout. Default: 30000.
  --sample-ms <n>           RSS sample interval. Default: 50.
  --max-output-bytes <n>    Captured stdout/stderr preview bytes. Default: 1048576.
  --large-output-bytes <n>  Bytes emitted by large command controls. Default: 16777216.
  --legacy-root <path>      pi-mono checkout. Default: ${toDisplayPath(defaultLegacyRoot)}.
  --candidate-bin <path>    Future y binary for candidate-* scenarios.
  --output <path>           JSONL result path. Default: ${toDisplayPath(defaultOutputPath)}.
  --no-write                Print summaries without writing JSONL.
  --help                    Show this help.

Notes:
  - Legacy scenarios never build or modify pi-mono. They require an existing built dist/cli.js.
  - RSS is sampled from the process tree with ps(1).
  - RSS is sampled from the process tree with ps(1). Heap metrics are recorded only when the child emits METRIC heap_* lines.`);
}

function parseArgs(argv) {
	const options = {
		scenarios: [],
		all: false,
		check: false,
		list: false,
		runs: 1,
		warmup: 0,
		timeoutMs: 30_000,
		sampleMs: 50,
		maxOutputBytes: 1_048_576,
		largeOutputBytes: 16_777_216,
		legacyRoot: defaultLegacyRoot,
		candidateBin: undefined,
		output: defaultOutputPath,
		write: true,
	};

	for (let index = 0; index < argv.length; index++) {
		const arg = argv[index];
		if (arg === "--help" || arg === "-h") {
			options.help = true;
			continue;
		}
		if (arg === "--list") {
			options.list = true;
			continue;
		}
		if (arg === "--check") {
			options.check = true;
			continue;
		}
		if (arg === "--all") {
			options.all = true;
			continue;
		}
		if (arg === "--no-write") {
			options.write = false;
			continue;
		}
		if (needsValue(arg)) {
			if (index + 1 >= argv.length) {
				throw new Error(`Missing value for ${arg}`);
			}
			const value = argv[++index];
			switch (arg) {
				case "--scenario":
					options.scenarios.push(value);
					break;
				case "--runs":
					options.runs = parsePositiveInteger(value, arg, true);
					break;
				case "--warmup":
					options.warmup = parsePositiveInteger(value, arg, true);
					break;
				case "--timeout-ms":
					options.timeoutMs = parsePositiveInteger(value, arg, false);
					break;
				case "--sample-ms":
					options.sampleMs = parsePositiveInteger(value, arg, false);
					break;
				case "--max-output-bytes":
					options.maxOutputBytes = parsePositiveInteger(value, arg, true);
					break;
				case "--large-output-bytes":
					options.largeOutputBytes = parsePositiveInteger(value, arg, false);
					break;
				case "--legacy-root":
					options.legacyRoot = resolve(value);
					break;
				case "--candidate-bin":
					options.candidateBin = resolve(value);
					break;
				case "--output":
					options.output = resolve(value);
					break;
			}
			continue;
		}
		throw new Error(`Unknown option: ${arg}`);
	}

	return options;
}

function needsValue(arg) {
	return [
		"--scenario",
		"--runs",
		"--warmup",
		"--timeout-ms",
		"--sample-ms",
		"--max-output-bytes",
		"--large-output-bytes",
		"--legacy-root",
		"--candidate-bin",
		"--output",
	].includes(arg);
}

function parsePositiveInteger(value, name, allowZero) {
	const parsed = Number.parseInt(value, 10);
	const valid = Number.isFinite(parsed) && Number.isInteger(parsed) && (allowZero ? parsed >= 0 : parsed > 0);
	if (!valid) {
		throw new Error(`Invalid ${name}: ${value}`);
	}
	return parsed;
}

function selectedScenarioIds(options) {
	if (options.all) {
		return [...scenarioDefinitions.entries()].filter(([, scenario]) => !scenario.optional).map(([id]) => id);
	}
	if (options.scenarios.length > 0) {
		return options.scenarios;
	}
	return [];
}

function validateScenarioIds(ids) {
	for (const id of ids) {
		if (!scenarioDefinitions.has(id)) {
			throw new Error(`Unknown scenario: ${id}. Use --list to see supported scenarios.`);
		}
	}
}

function listScenarios() {
	for (const [id, scenario] of scenarioDefinitions.entries()) {
		const optional = scenario.optional ? " optional" : "";
		console.log(`${id} [${scenario.mode}${optional}] ${scenario.description}`);
	}
}

function legacyPackageDir(options) {
	return join(options.legacyRoot, "packages", "coding-agent");
}

function legacyCliPath(options) {
	return join(legacyPackageDir(options), "dist", "cli.js");
}

function benchmarkEnv(options) {
	return {
		...process.env,
		PI_OFFLINE: "1",
		PI_SKIP_VERSION_CHECK: "1",
		PI_CODING_AGENT_DIR: join(yMonoRoot, ".baseline-agent"),
	};
}

function buildScriptWrappedCommand(command, cwd, env) {
	if (process.platform === "darwin") {
		return {
			executable: "script",
			args: ["-q", "/dev/null", ...command],
			cwd,
			env,
		};
	}

	const commandLine = command.map(shellQuote).join(" ");
	return {
		executable: "script",
		args: ["-q", "-e", "-c", commandLine, "/dev/null"],
		cwd,
		env,
	};
}

function shellQuote(value) {
	return `'${String(value).replaceAll("'", "'\\''")}'`;
}

function largeOutputProgram(streamName, byteCount) {
	return `
const target = ${byteCount};
const chunk = Buffer.alloc(65536, ${streamName === "stdout" ? 65 : 69});
let written = 0;
function writeMore() {
  while (written < target) {
    const remaining = target - written;
    const data = remaining >= chunk.length ? chunk : chunk.subarray(0, remaining);
    if (!process.${streamName}.write(data)) {
      process.${streamName}.once("drain", writeMore);
      written += data.length;
      return;
    }
    written += data.length;
  }
}
writeMore();
`;
}

function commandRequirement(label, command) {
	return { type: "command", label, command };
}

function rssRequirement() {
	return { type: "rss", label: "ps RSS process-tree sampler" };
}

function fileRequirement(label, path, fix) {
	return { type: "file", label, path, fix };
}

function checkRequirement(requirement) {
	if (requirement.type === "rss") {
		const resolved = resolveCommand("ps");
		if (!resolved) {
			return { ok: false, message: "ps for RSS sampling not found on PATH: ps" };
		}
		const result = spawnSync("ps", ["-A", "-o", "pid=", "-o", "ppid=", "-o", "rss="], {
			encoding: "utf8",
			stdio: ["ignore", "pipe", "pipe"],
		});
		if (result.status !== 0 || result.error) {
			const detail = result.error?.message || result.stderr.trim() || `exit status ${result.status}`;
			return { ok: false, message: `ps exists at ${resolved} but cannot sample process-tree RSS: ${detail}` };
		}
		return { ok: true, message: `ps RSS process-tree sampler: ${resolved}` };
	}

	if (requirement.type === "file") {
		if (!requirement.path) {
			return { ok: false, message: `${requirement.label} is not configured. ${requirement.fix ?? ""}`.trim() };
		}
		if (!existsSync(requirement.path)) {
			return { ok: false, message: `${requirement.label} not found: ${requirement.path}. ${requirement.fix ?? ""}`.trim() };
		}
		return { ok: true, message: `${requirement.label}: ${requirement.path}` };
	}

	const resolved = resolveCommand(requirement.command);
	if (!resolved) {
		return { ok: false, message: `${requirement.label} not found on PATH: ${requirement.command}` };
	}
	return { ok: true, message: `${requirement.label}: ${resolved}` };
}

function resolveCommand(command) {
	if (!command) {
		return undefined;
	}
	if (command.includes("/") && existsSync(command)) {
		return command;
	}
	const result = spawnSync("sh", ["-c", "command -v -- \"$1\"", "sh", command], {
		encoding: "utf8",
		stdio: ["ignore", "pipe", "ignore"],
	});
	const resolved = result.stdout.trim();
	return result.status === 0 && resolved ? resolved : undefined;
}

function checkScenarios(ids, options, verbose = true) {
	let ok = true;
	for (const id of ids) {
		const scenario = scenarioDefinitions.get(id);
		const failures = [];
		const checks = scenario.requirements(options).map(checkRequirement);
		for (const check of checks) {
			if (!check.ok) {
				failures.push(check.message);
				ok = false;
			}
		}
		if (verbose) {
			console.log(`${id}: ${failures.length === 0 ? "available" : "unavailable"}`);
			for (const check of checks) {
				console.log(`  ${check.ok ? "ok" : "missing"}: ${check.message}`);
			}
		}
	}
	return ok;
}

async function runScenario(id, scenario, options) {
	const totalRuns = options.warmup + options.runs;
	const measured = [];
	for (let index = 0; index < totalRuns; index++) {
		const measuredIndex = index >= options.warmup ? index - options.warmup + 1 : undefined;
		const result = await runOnce(id, scenario, options, index + 1);
		console.log(`${id} ${measuredIndex === undefined ? `warmup ${index + 1}` : `run ${measuredIndex}`}: elapsed=${formatMs(result.metrics.elapsed_ms)} peak_rss=${formatKb(result.metrics.peak_rss_kb)} stdout=${result.metrics.stdout_bytes} stderr=${result.metrics.stderr_bytes}`);
		if (measuredIndex !== undefined) {
			measured.push({ ...result, measured_run: measuredIndex });
		}
	}
	return measured;
}

async function runOnce(id, scenario, options, runNumber) {
	const command = scenario.buildCommand(options);
	const startedAt = performance.now();
	const startedAtIso = new Date().toISOString();
	let stdoutBytes = 0;
	let stderrBytes = 0;
	let stdoutPreview = "";
	let stderrPreview = "";
	let stdoutBuffer = "";
	let readyElapsedMs;
	let parseError;

	const child = spawn(command.executable, command.args, {
		cwd: command.cwd,
		env: command.env,
		stdio: ["pipe", "pipe", "pipe"],
		shell: process.platform === "win32",
	});

	const rssSamples = [];
	const sample = () => {
		const rss = sampleProcessTreeRssKb(child.pid);
		if (rss !== undefined) {
			rssSamples.push(rss);
		}
	};
	const sampler = setInterval(sample, options.sampleMs);
	sample();

	const timeout = setTimeout(() => {
		child.kill("SIGTERM");
		setTimeout(() => child.kill("SIGKILL"), 2_000).unref();
	}, options.timeoutMs);

	child.stdout.on("data", (chunk) => {
		stdoutBytes += chunk.length;
		stdoutPreview = appendPreview(stdoutPreview, chunk, options.maxOutputBytes);
		if (scenario.readyWhen && readyElapsedMs === undefined) {
			stdoutBuffer = splitLines(stdoutBuffer + chunk.toString("utf8"), (line) => {
				try {
					if (scenario.readyWhen(line, runNumber)) {
						readyElapsedMs = performance.now() - startedAt;
						if (scenario.closeStdinOnReady) {
							child.stdin.end();
						}
					}
				} catch (error) {
					parseError = error instanceof Error ? error.message : String(error);
				}
			});
		}
	});

	child.stderr.on("data", (chunk) => {
		stderrBytes += chunk.length;
		stderrPreview = appendPreview(stderrPreview, chunk, options.maxOutputBytes);
	});

	if (scenario.stdin) {
		child.stdin.write(scenario.stdin(runNumber));
	} else {
		child.stdin.end();
	}

	const exit = await waitForExit(child);
	clearInterval(sampler);
	clearTimeout(timeout);
	sample();

	const elapsedMs = performance.now() - startedAt;
	if (parseError) {
		throw new Error(`${id} failed to parse readiness output: ${parseError}`);
	}
	if (scenario.readyWhen && readyElapsedMs === undefined) {
		throw new Error(`${id} did not reach readiness before exit. stderr preview: ${stderrPreview.trim()}`);
	}
	if (exit.signal) {
		throw new Error(`${id} terminated by ${exit.signal}. stderr preview: ${stderrPreview.trim()}`);
	}
	if (exit.code !== 0) {
		throw new Error(`${id} exited with code ${exit.code}. stderr preview: ${stderrPreview.trim()}`);
	}

	const parsedMetrics = parseMetricLines(`${stdoutPreview}\n${stderrPreview}`);
	const heapMetrics = Object.fromEntries(Object.entries(parsedMetrics).filter(([name]) => name.startsWith("heap_")));
	const notes = [];
	if (Object.keys(heapMetrics).length === 0) {
		notes.push("heap metrics unavailable: child did not emit METRIC heap_* lines");
	}
	}

	return {
		schema_version: 1,
		activity,
		scenario: id,
		description: scenario.description,
		mode: scenario.mode,
		product: scenario.product,
		started_at: startedAtIso,
		command: {
			executable: command.executable,
			args: command.args,
			cwd: command.cwd,
		},
		metrics: {
			elapsed_ms: round(elapsedMs),
			ready_ms: readyElapsedMs === undefined ? null : round(readyElapsedMs),
			peak_rss_kb: rssSamples.length === 0 ? null : Math.max(...rssSamples),
			avg_rss_kb: rssSamples.length === 0 ? null : round(rssSamples.reduce((sum, value) => sum + value, 0) / rssSamples.length),
			stdout_bytes: stdoutBytes,
			stderr_bytes: stderrBytes,
			exit_code: exit.code,
			signal: exit.signal,
			...heapMetrics,
		},
		parsed_metrics: parsedMetrics,
		notes,
	};
}

function waitForExit(child) {
	return new Promise((resolve, reject) => {
		child.once("error", reject);
		child.once("exit", (code, signal) => resolve({ code, signal }));
	});
}

function sampleProcessTreeRssKb(rootPid) {
	if (!rootPid) {
		return undefined;
	}
	const result = spawnSync("ps", ["-A", "-o", "pid=", "-o", "ppid=", "-o", "rss="], {
		encoding: "utf8",
		stdio: ["ignore", "pipe", "ignore"],
	});
	if (result.status !== 0) {
		return undefined;
	}
	const rows = result.stdout
		.split(/\r?\n/)
		.map((line) => line.trim().split(/\s+/).map((part) => Number.parseInt(part, 10)))
		.filter((parts) => parts.length === 3 && parts.every(Number.isFinite))
		.map(([pid, ppid, rss]) => ({ pid, ppid, rss }));
	const childrenByParent = new Map();
	for (const row of rows) {
		const children = childrenByParent.get(row.ppid);
		if (children) {
			children.push(row);
		} else {
			childrenByParent.set(row.ppid, [row]);
		}
	}
	let total = 0;
	const stack = [rootPid];
	const seen = new Set();
	while (stack.length > 0) {
		const pid = stack.pop();
		if (seen.has(pid)) {
			continue;
		}
		seen.add(pid);
		const row = rows.find((candidate) => candidate.pid === pid);
		if (row) {
			total += row.rss;
		}
		for (const child of childrenByParent.get(pid) ?? []) {
			stack.push(child.pid);
		}
	}
	return total === 0 ? undefined : total;
}

function appendPreview(current, chunk, limit) {
	if (current.length >= limit) {
		return current;
	}
	const next = current + chunk.toString("utf8");
	return next.length > limit ? next.slice(0, limit) : next;
}

function splitLines(buffer, onLine) {
	let remaining = buffer;
	while (true) {
		const newline = remaining.indexOf("\n");
		if (newline === -1) {
			return remaining;
		}
		const rawLine = remaining.slice(0, newline);
		onLine(rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine);
		remaining = remaining.slice(newline + 1);
	}
}

function parseMetricLines(text) {
	const metrics = {};
	for (const line of text.split(/\r?\n/)) {
		const match = line.match(/^METRIC\s+([a-zA-Z0-9_.-]+)=(-?\d+(?:\.\d+)?)$/);
		if (match) {
			metrics[match[1]] = Number(match[2]);
		}
	}
	return metrics;
}

function writeResults(path, results) {
	mkdirSync(dirname(path), { recursive: true });
	const lines = results.map((result) => JSON.stringify(result)).join("\n");
	writeFileSync(path, `${lines}${lines ? "\n" : ""}`);
}

function formatMs(value) {
	return `${value.toFixed(1)}ms`;
}

function formatKb(value) {
	return value === null || value === undefined ? "n/a" : `${value} KiB`;
}

function round(value) {
	return Math.round(value * 10) / 10;
}

function toDisplayPath(path) {
	const rel = relative(workspaceRoot, path);
	return rel && !rel.startsWith("..") ? rel.replaceAll("\\", "/") : path;
}

async function main() {
	const options = parseArgs(process.argv.slice(2));
	if (options.help || process.argv.length <= 2) {
		printHelp();
		return;
	}
	if (options.list) {
		listScenarios();
		return;
	}

	const ids = selectedScenarioIds(options);
	validateScenarioIds(ids);
	if (ids.length === 0) {
		throw new Error("No scenarios selected. Use --scenario <id>, --all, or --list.");
	}

	const dependenciesAvailable = checkScenarios(ids, options, options.check);
	if (!dependenciesAvailable) {
		throw new Error("One or more measurement dependencies are missing. See messages above and rerun after fixing them.");
	}
	if (options.check) {
		return;
	}

	const results = [];
	for (const id of ids) {
		const scenario = scenarioDefinitions.get(id);
		results.push(...await runScenario(id, scenario, options));
	}

	if (options.write) {
		writeResults(options.output, results);
		console.log(`Wrote ${results.length} result record(s) to ${toDisplayPath(options.output)}`);
	}
}

main().catch((error) => {
	const message = error instanceof Error ? error.message : String(error);
	console.error(`measure-baseline: ${message}`);
	process.exit(1);
});
