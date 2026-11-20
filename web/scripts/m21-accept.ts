#!/usr/bin/env bun
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
/**
 * M21 真机验收（docs/plan-m21.md 步骤 8 及执行中修订）：Runner 改用 Oryn Core 与控制面下发的无密钥配置。对 dev 部署逐项执行，
 * 每项打印 `R<n> PASS|FAIL|SKIP (秒)` 与证据行，末行 `VERDICT PASS|FAIL`。
 *
 * 用法（web/ 目录，以 root 运行）：bun run accept:m21 [-- --only R1,R3]
 * 环境变量：ORYN_URL（缺省 http://localhost:7600）、ORYN_ROOT、ORYN_TOKEN、ORYN_CREDENTIALS（缺省 .dev/credentials.json）；
 *   M21_RUNNER_URL（runner 的 ORYN_URL，缺省 http://127.0.0.1:7600）；M21_USER（跑 runner 的普通用户，缺省 oryn-m21）；
 *   M21_SSH_USER（临时 sshd 的登录用户，缺省 oryn-m21h）；M21_CORE_SRC（本地 Core 源码 cli.ts，
 *   缺省 /root/Projects/Ling/oryn-core/packages/coding-agent/src/cli.ts）；M21_EGRESS（runner 的 ORYN_EGRESS_ADDR，
 *   缺省 127.0.0.1:17811，顺带验证中转地址随之改写）；M21_OUT（结果目录，缺省 /tmp/oryn-shots/m21-accept）。
 *
 * 固定布置：
 *   - Core 尚未发布：runner 以 ORYN_AGENT_BIN 指向 /var/tmp/m21/bin/omp（包装脚本 `exec bun <M21_CORE_SRC> "$@"`，
 *     runner 用户对源码目录与 /usr/bin/bun 有读 / 执行权限）。下载、sha256 校验、~/.oryn/core 缓存这条路径不经过，
 *     由 corefetch 单测覆盖，待 Core 发布后去掉 ORYN_AGENT_BIN 补跑。
 *   - runner 用户 oryn-m21（不存在就 useradd -m），每次运行清空其 ~/.oryn ~/.omp ~/.agents ~/.ssh 后放哨兵：
 *     ~/.omp/agent/config.yml、~/.agents/skills/sentinel/SKILL.md；~/.ssh 不存在。bin/orbd 拷到 /var/tmp/m21/orbd。
 *     runner 以 `runuser -u oryn-m21 -- env -i HOME=… PATH=/usr/local/bin:/usr/bin:/bin ORYN_URL=… ORYN_RUNNER_TOKEN=…
 *     ORYN_AGENT_BIN=… ORYN_EGRESS_ADDR=… orbd run --runner --dir /tmp/m21-a --dir /tmp/m21-b --dir /tmp/m21-c --terminal`
 *     起在后台，输出追加到 /var/tmp/m21/runner-<RUN>.log。
 *   - /tmp/m21-a（git 仓库，含 .claude/.agents/.omp 三处 skills/m21-*-<RUN>/SKILL.md 与文件库用的 m21-lib-<RUN>.txt）、
 *     /tmp/m21-b、/tmp/m21-c 每次重建，属主 runner 用户。dev 中央 config.yml 的 disabledProviders 含 claude 时，
 *     .claude 那一份不在技能列表里属配置所致，R3 只记录不判定。
 *   - 两个项目 m21-<RUN>-a / -b；SSH：临时 sshd（/usr/bin/sshd -D，127.0.0.1 上的空闲端口，独立主机钥匙与
 *     authorized_keys，登录用户 oryn-m21h，家目录放一个 m21-<RUN>.txt），dev 里一把钥匙 m21-<RUN>，主机：全局
 *     m21-<RUN>-g、项目 a 的 m21-<RUN>-pa、项目 b 的 m21-<RUN>-pb，都指向这台 sshd。主机与项目在 runner 上线、线程开始
 *     之前登记（项目层随 agent.start 交付）。
 *   - 「Core 经 ssh 连接」= 线程用 read 工具读 `ssh://<主机>/~/m21-<RUN>.txt`：Core 19 没有单独的 ssh 工具，ssh:// 经
 *     connection-manager 起 `ssh -M -N -f` master 并复用；master 的环境里 ORYN_SSH_KEY 是起它的线程的私钥，脚本据此认出
 *     各线程的 master。对方项目主机的拒绝、R9 归档后的复用与重连以线程私钥 + Core 的 ControlPath 在 runner 用户下直接
 *     执行 ssh（等同 Core / bash 的调用；归档的线程不能再发消息）。
 *   - 假模型服务（本进程 Bun.serve，OpenAI chat completions 流式）：R2 临时在中央 models.yml 的 providers 下加一个
 *     m21fake-<RUN>（字面 apiKey 被控制面抽进密钥库），记录收到的 Authorization；R2 结束即恢复原 models.yml 并删掉抽出的密钥。
 *   - 线程：$A（项目 a，/tmp/m21-a，dev 默认模型）用于 R2–R6、R8、R9；$B（项目 b，/tmp/m21-b）用于 R6（其 master 在 R8
 *     里作为「别的线程」对照）；$F（无项目，/tmp/m21-c，假模型）用于 R2。
 *   - R8 换代只动线程 A：连续 kill -9 它的 agent 进程组直到监督器自重启用尽（1s/2s/4s）而结束，再发消息由 agent.start
 *     起新一代；不用 restart-sessions，它会顺带重启 dev 里所有带「需重启」标记的会话。
 *   - R9：archived 那一半用全局主机，shipped 那一半用本项目主机——同一个 Core 进程对某主机的常驻 master 被网关断开后，
 *     之后的 ssh:// 操作改为临时连接、不再常驻新 master，要验「master 存活时归档」就得换一台这一代还没连过的主机。
 *   - 结尾（含异常与 Ctrl-C）：停 runner（及该用户全部进程）、删线程、删 runner、删 SSH 主机与钥匙、停 sshd、删项目、
 *     删 R7 的探针文件并核对中央配置文件清单与开始前一致、恢复 models.yml、删文件库里本次存入的文件。两个系统用户保留。
 */
import type { Subprocess } from "bun";

const ROOT = process.env.ORYN_ROOT ?? `${import.meta.dir}/../..`;
const BASE = process.env.ORYN_URL ?? "http://localhost:7600";
const CREDS = process.env.ORYN_CREDENTIALS ?? `${ROOT}/.dev/credentials.json`;
const TOKEN =
	process.env.ORYN_TOKEN ??
	(JSON.parse(readFileSync(CREDS, "utf8")) as { token: string }).token;
const OUT = process.env.M21_OUT ?? "/tmp/oryn-shots/m21-accept";
const DB = `${ROOT}/.dev/data/oryn.db`;
const ORYN = `${ROOT}/bin/oryn`;
const ORYND_LOG = `${ROOT}/.dev/orynd.log`;
const RUNNER_URL = process.env.M21_RUNNER_URL ?? "http://127.0.0.1:7600";
const RUSER = process.env.M21_USER ?? "oryn-m21";
const SSH_USER = process.env.M21_SSH_USER ?? "oryn-m21h";
const CORE_SRC =
	process.env.M21_CORE_SRC ??
	"/root/Projects/Ling/oryn-core/packages/coding-agent/src/cli.ts";
const EGRESS = process.env.M21_EGRESS ?? "127.0.0.1:17811";
const STAGE = "/var/tmp/m21";
const RORBD = `${STAGE}/orbd`;
const AGENT_BIN = `${STAGE}/bin/omp`;
const DIR_A = "/tmp/m21-a";
const DIR_B = "/tmp/m21-b";
const DIR_C = "/tmp/m21-c";
const GIT_ID = "-c user.name=m21 -c user.email=m21@example.invalid";
const SSHD_DIR = `${STAGE}/sshd`;

const onlyArg = process.argv.indexOf("--only");
const ONLY =
	onlyArg >= 0 ? new Set((process.argv[onlyArg + 1] ?? "").split(",")) : null;

const RUN = Math.random().toString(36).slice(2, 6);
const RUNNER_LOG = `${STAGE}/runner-${RUN}.log`;
const SENTINEL = `# m21 sentinel ${RUN}\ntheme: sentinel\n`;
/** 仓库内三处项目级技能目录 → 技能名（.claude 受中央 config.yml 的 disabledProviders 影响，见 R3）。 */
const REPO_SKILLS: Record<string, string> = {
	".claude": `m21-claude-${RUN}`,
	".agents": `m21-agents-${RUN}`,
	".omp": `m21-omp-${RUN}`,
};
const LIB_FILE = `m21-lib-${RUN}.txt`;
const LIB_BODY = `m21 library ${RUN}\n`;
const H_G = `m21-${RUN}-g`;
const H_PA = `m21-${RUN}-pa`;
const H_PB = `m21-${RUN}-pb`;
const SSH_KEY = `m21-${RUN}`;
/** SSH 目标用户家目录下的文件：线程经 Core 的 `ssh://<主机>/~/…` 读取（走 Core 的连接复用 master）。 */
const REMOTE_FILE = `m21-${RUN}.txt`;
const REMOTE_BODY = `m21-${RUN}-remote`;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const sec = (ms: number) => `${(ms / 1000).toFixed(1)}s`;
const one1 = (s: string) => s.replaceAll("\n", " | ");

function sh(
	cmd: string,
	opts: { cwd?: string; stdin?: string } = {},
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], {
		cwd: opts.cwd ?? ROOT,
		stdin: opts.stdin !== undefined ? Buffer.from(opts.stdin) : undefined,
	});
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
}

function must(r: { code: number; out: string }, what: string): string {
	if (r.code !== 0) throw new Error(`${what}失败（${r.code}）：${r.out}`);
	return r.out;
}

interface CliRes {
	code: number;
	out: string;
	stdout: string;
}

async function cli(args: string): Promise<CliRes> {
	const p = Bun.spawn(
		["bash", "-c", `ORYN_CREDENTIALS=${q(CREDS)} ${q(ORYN)} ${args}`],
		{ cwd: ROOT, stdout: "pipe", stderr: "pipe" },
	);
	const [stdout, stderr, code] = await Promise.all([
		new Response(p.stdout).text(),
		new Response(p.stderr).text(),
		p.exited,
	]);
	return { code: code as number, out: `${stdout}${stderr}`.trim(), stdout };
}

function sql<T = Record<string, unknown>>(qs: string): T[] {
	const r = Bun.spawnSync([
		"sqlite3",
		"-json",
		"-cmd",
		".timeout 5000",
		DB,
		qs,
	]);
	if (r.exitCode !== 0)
		throw new Error(`sqlite3：${r.stderr.toString().trim()}`);
	const s = r.stdout.toString().trim();
	return s ? (JSON.parse(s) as T[]) : [];
}
const one = <T = Record<string, unknown>>(qs: string) => sql<T>(qs)[0];

interface ApiRes {
	status: number;
	json: unknown;
	text: string;
}

async function api(
	method: string,
	path: string,
	body?: unknown,
	raw = false,
): Promise<ApiRes> {
	const res = await fetch(`${BASE}${path}`, {
		method,
		headers: {
			Authorization: `Bearer ${TOKEN}`,
			"X-Oryn-CSRF": "1",
			...(body !== undefined
				? {
						"Content-Type": raw
							? "application/octet-stream"
							: "application/json",
					}
				: {}),
		},
		body:
			body === undefined
				? undefined
				: raw
					? (body as string)
					: JSON.stringify(body),
		redirect: "manual",
	});
	const text = await res.text();
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 空体（204）或原文
	}
	return { status: res.status, json, text };
}

async function getJson<T>(path: string): Promise<T> {
	const r = await api("GET", path);
	if (r.status !== 200) throw new Error(`GET ${path} → ${r.status} ${r.text}`);
	return r.json as T;
}

async function waitFor<T>(
	what: string,
	fn: () =>
		| Promise<T | undefined | null | false>
		| T
		| undefined
		| null
		| false,
	timeoutMs: number,
	intervalMs = 500,
): Promise<T> {
	const deadline = Date.now() + timeoutMs;
	let last: unknown;
	while (Date.now() < deadline) {
		try {
			const v = await fn();
			if (v) return v;
		} catch (e) {
			last = e;
		}
		await sleep(intervalMs);
	}
	throw new Error(
		`超时（${timeoutMs}ms）：${what}${last ? `；最后错误 ${String(last)}` : ""}`,
	);
}

// ---------------------------------------------------------------- 结果

type Verdict = "PASS" | "FAIL" | "SKIP";
class Skip extends Error {}
const results: {
	id: string;
	verdict: Verdict;
	seconds: number;
	evidence: string[];
}[] = [];
const setupNotes: string[] = [];
const cleanupNotes: string[] = [];
let evidence: string[] = [];
const note = (s: string) => {
	evidence.push(s);
	console.log(`    · ${s}`);
};
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
const snote = (s: string) => {
	setupNotes.push(s);
	console.log(`    [准备] ${s}`);
};

function failArtifacts(id: string) {
	sh(`mkdir -p ${q(OUT)}`);
	const log = `${OUT}/${id}-FAIL-${RUN}.orynd.log`;
	writeFileSync(log, sh(`tail -n 300 ${q(ORYND_LOG)}`).stdout);
	note(`orynd.log 末尾 ${log}`);
	if (existsSync(RUNNER_LOG)) {
		const rl = `${OUT}/${id}-FAIL-${RUN}.runner.log`;
		writeFileSync(rl, sh(`tail -n 300 ${q(RUNNER_LOG)}`).stdout);
		note(`runner 日志末尾 ${rl}`);
	}
}

// ---------------------------------------------------------------- 契约模型（只取用到的字段）

interface RunnerView {
	id: string;
	name: string;
	online: boolean;
	orbId: string;
	features: string[];
	orbdVersion?: string;
}
interface Entry {
	seq: number;
	type: string;
	payload: Record<string, unknown>;
}
interface AssistantFile {
	id: string;
	name: string;
	size: number;
	source: string;
	sourceThreadId?: string;
}
interface AuditRow {
	id: number;
	at: number;
	thread_id: string | null;
	host_name: string;
	kind: string;
	detail: string;
	ok: number;
}
interface ApplyStatus {
	threads: { threadId: string; title: string; reasons: string[] }[];
	pendingReloads: number;
	push: { pending: boolean; appliedAt?: number; failed: { orbId: string }[] };
}

const runners = async () =>
	(await getJson<{ runners: RunnerView[] }>("/api/v1/runners")).runners;

// ---------------------------------------------------------------- 线程

async function entriesOf(tid: string): Promise<Entry[]> {
	const out: Entry[] = [];
	let after = 0;
	for (let i = 0; i < 200; i++) {
		const p = await getJson<{ entries: Entry[]; hasMore: boolean }>(
			`/api/v1/threads/${tid}/entries?after=${after}&limit=500`,
		);
		out.push(...p.entries);
		const last = p.entries.at(-1);
		if (!p.hasMore || !last) break;
		after = last.seq;
	}
	return out;
}

function textOf(c: unknown): string {
	if (typeof c === "string") return c;
	if (!Array.isArray(c)) return "";
	return c
		.map((x) =>
			x &&
			typeof x === "object" &&
			"type" in x &&
			x.type === "text" &&
			"text" in x
				? String(x.text ?? "")
				: "",
		)
		.join("");
}

async function lastAssistant(tid: string): Promise<string> {
	const es = await entriesOf(tid);
	for (let i = es.length - 1; i >= 0; i--) {
		const e = es[i];
		if (e?.type !== "message") continue;
		const m = e.payload.message as
			| { role?: string; content?: unknown }
			| undefined;
		if (m?.role !== "assistant") continue;
		const t = textOf(m.content).trim();
		if (t) return t;
	}
	return "";
}

/** 转录里的工具调用（assistant 消息的 toolCall 块）：`名字 参数 JSON`（参数截到 200 字符）。 */
async function toolCalls(tid: string, afterSeq = 0): Promise<string[]> {
	const out: string[] = [];
	for (const e of await entriesOf(tid)) {
		if (e.seq <= afterSeq || e.type !== "message") continue;
		const m = e.payload.message as { role?: string; content?: unknown };
		if (m?.role !== "assistant" || !Array.isArray(m.content)) continue;
		for (const b of m.content as {
			type?: string;
			name?: string;
			arguments?: unknown;
		}[])
			if (b?.type === "toolCall" && b.name)
				out.push(
					`${b.name} ${JSON.stringify(b.arguments ?? {}).slice(0, 200)}`,
				);
	}
	return out;
}

interface ThreadRow {
	state: string;
	last_seq: number;
	orb_id: string | null;
	created_at: number;
}
const threadRow = (tid: string) =>
	one<ThreadRow>(
		`select state, last_seq, orb_id, created_at from threads where id='${tid}'`,
	);

const SETTLED: Record<string, true> = {
	idle: true,
	awaiting_input: true,
	error: true,
};

async function waitTurn(tid: string, beforeSeq: number, timeoutMs = 600_000) {
	return waitFor(
		`线程 ${tid} 回合结束`,
		() => {
			const r = threadRow(tid);
			if (!r || !SETTLED[r.state] || r.last_seq <= beforeSeq) return undefined;
			const n = one<{ n: number }>(
				`select count(*) n from entries where thread_id='${tid}' and seq>${beforeSeq} and type='message' and instr(cast(payload as text), '"role":"assistant"')>0`,
			)?.n;
			return n ? r.state : undefined;
		},
		timeoutMs,
		1000,
	);
}

/** 发一条消息并等到回合结束，返回最后一条 assistant 文本与发出前的 seq。 */
async function say(tid: string, msg: string, timeoutMs = 600_000) {
	const before = threadRow(tid)?.last_seq ?? 0;
	const t0 = Date.now();
	const r = await cli(`thread send ${tid} ${q(msg)}`);
	if (r.code !== 0) throw new Error(`thread send 失败：${r.out}`);
	const st = await waitTurn(tid, before, timeoutMs);
	check(
		st === "idle",
		`${tid} 回合结束于 idle（${st}，${sec(Date.now() - t0)}）`,
	);
	return { text: await lastAssistant(tid), before, t0 };
}

// ---------------------------------------------------------------- runner 用户与宿主准备

let uhome = "";
let hostPrepared = false;

const asUser = (cmd: string, env: Record<string, string> = {}) =>
	sh(
		`runuser -u ${RUSER} -- env -i HOME=${q(uhome)} USER=${RUSER} LOGNAME=${RUSER} PATH=/usr/local/bin:/usr/bin:/bin ${Object.entries(
			env,
		)
			.map(([k, v]) => q(`${k}=${v}`))
			.join(" ")} bash -c ${q(cmd)}`,
	);

/** 宿主 root 的 models.yml 里字面 apiKey 的前 12 位（跳过 `!命令`、环境变量名与过短的值）。 */
function hostModelKeys(): string[] {
	const f = `${homedir()}/.omp/agent/models.yml`;
	if (!existsSync(f)) return [];
	const doc = Bun.YAML.parse(readFileSync(f, "utf8")) as {
		providers?: Record<string, { apiKey?: unknown } | null>;
	} | null;
	const out: string[] = [];
	for (const v of Object.values(doc?.providers ?? {})) {
		const k = typeof v?.apiKey === "string" ? v.apiKey : "";
		if (k.startsWith("!") || /^[A-Z_][A-Z0-9_]*$/.test(k) || k.length < 12)
			continue;
		out.push(k.slice(0, 12));
	}
	return out;
}

/** 文件的「sha256 大小 mtime（纳秒）」指纹；不存在为 absent。 */
const fileSig = (p: string) =>
	sh(
		`if [ -e ${q(p)} ]; then echo "$(sha256sum < ${q(p)} | cut -c1-16) $(stat -c '%s %.9Y' ${q(p)})"; else echo absent; fi`,
	).stdout;
/** 目录树指纹（路径 大小 mtime）；不存在为 absent。 */
const treeSig = (p: string) =>
	sh(
		`if [ -e ${q(p)} ]; then find ${q(p)} -printf '%p %s %T@\\n' | sort; else echo absent; fi`,
	).stdout;

let sentinelSig = "";
let agentsSig = "";

function prepareHost() {
	if (hostPrepared) return;
	if (process.getuid?.() !== 0)
		throw new Error("要以 root 运行（useradd / runuser / sshd）");
	if (!existsSync(CORE_SRC)) throw new Error(`没有本地 Core 源码 ${CORE_SRC}`);
	for (const u of [RUSER, SSH_USER])
		must(
			sh(`id -u ${u} >/dev/null 2>&1 || useradd -m -s /bin/bash ${u}`),
			`建用户 ${u}`,
		);
	// 登录目标用户：useradd 的 `!` 口令在 UsePAM no 下算锁定，改成 `*`（同样不能口令登录）
	must(sh(`usermod -p '*' ${SSH_USER}`), `解锁 ${SSH_USER}`);
	uhome = must(sh(`getent passwd ${RUSER} | cut -d: -f6`), "取家目录");
	sh(`pkill -KILL -u ${RUSER}`);
	must(
		asUser(
			`cd ~ && rm -rf .oryn .omp .agents .ssh && mkdir -p .omp/agent .agents/skills/sentinel && printf %s ${q(SENTINEL)} > .omp/agent/config.yml && printf '%s\\n' '---' 'name: sentinel' 'description: 用户级哨兵技能（Runner 不应看到）' '---' 'sentinel' > .agents/skills/sentinel/SKILL.md`,
		),
		"放哨兵",
	);
	sentinelSig = fileSig(`${uhome}/.omp/agent/config.yml`);
	agentsSig = treeSig(`${uhome}/.agents`);
	snote(
		`用户 ${RUSER}（HOME ${uhome}）：清空 ~/.oryn ~/.omp ~/.agents ~/.ssh，哨兵 ~/.omp/agent/config.yml（${sentinelSig}）、~/.agents/skills/sentinel/SKILL.md；~/.ssh 不存在`,
	);
	must(
		sh(
			`install -d -m 755 ${STAGE} ${STAGE}/bin && install -m 755 ${q(`${ROOT}/bin/orbd`)} ${RORBD} && printf '%s\\n' '#!/bin/sh' ${q(`exec bun ${CORE_SRC} "$@"`)} > ${AGENT_BIN} && chmod 755 ${AGENT_BIN}`,
		),
		"拷贝 orbd / 写 Core 包装脚本",
	);
	const ver = asUser(`${AGENT_BIN} --version`);
	must(ver, "runner 用户运行本地 Core");
	snote(
		`orbd → ${RORBD}（${sh(`${RORBD} --version 2>&1 | head -n1`).stdout}）；ORYN_AGENT_BIN=${AGENT_BIN}（exec bun ${CORE_SRC}，runner 用户执行 --version → ${ver.stdout}）`,
	);
	must(
		sh(
			`rm -rf ${DIR_A} ${DIR_B} ${DIR_C} && install -d -o ${RUSER} -g ${RUSER} -m 755 ${DIR_A} ${DIR_B} ${DIR_C}`,
		),
		"重建目录",
	);
	const skillCmds = Object.entries(REPO_SKILLS).map(
		([dir, name]) =>
			`mkdir -p ${dir}/skills/${name} && printf '%s\\n' '---' 'name: ${name}' 'description: M21 验收用的仓库内技能（${dir}）' '---' 'repo skill' > ${dir}/skills/${name}/SKILL.md`,
	);
	must(
		asUser(
			`cd ${DIR_A} && git init -q -b main && ${skillCmds.join(" && ")} && printf %s ${q(LIB_BODY)} > ${LIB_FILE} && echo m21 > README.md && git add -A && git ${GIT_ID} commit -qm init`,
		),
		`准备 ${DIR_A}`,
	);
	snote(
		`${DIR_A}（git 仓库，含 ${Object.entries(REPO_SKILLS)
			.map(([d, n]) => `${d}/skills/${n}`)
			.join("、")}、${LIB_FILE}）、${DIR_B}、${DIR_C} 已重建，属主 ${RUSER}`,
	);
	hostPrepared = true;
}

// ---------------------------------------------------------------- 假模型服务

const FAKE_PROVIDER = `m21fake-${RUN}`;
const FAKE_MODEL = "m21-fake";
const FAKE_KEY = `m21fake-${RUN}-${crypto.randomUUID().replaceAll("-", "")}`;
const FAKE_REPLY = `m21-fake-reply-${RUN}`;
const fakeSeen: { path: string; auth: string; at: number }[] = [];

function sse(chunks: unknown[]): Response {
	const body = `${chunks.map((c) => `data: ${JSON.stringify(c)}\n\n`).join("")}data: [DONE]\n\n`;
	return new Response(body, {
		headers: { "Content-Type": "text/event-stream" },
	});
}

const fakeSrv = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(r: Request) {
		const url = new URL(r.url);
		fakeSeen.push({
			path: url.pathname,
			auth: r.headers.get("authorization") ?? "",
			at: Date.now(),
		});
		if (r.method === "GET" && url.pathname.endsWith("/models"))
			return Response.json({
				object: "list",
				data: [{ id: FAKE_MODEL, object: "model" }],
			});
		const body = (await r.json().catch(() => ({}))) as { stream?: boolean };
		const created = Math.floor(Date.now() / 1000);
		const base = { id: "chatcmpl-m21", created, model: FAKE_MODEL };
		if (!body.stream)
			return Response.json({
				...base,
				object: "chat.completion",
				choices: [
					{
						index: 0,
						message: { role: "assistant", content: FAKE_REPLY },
						finish_reason: "stop",
					},
				],
				usage: { prompt_tokens: 10, completion_tokens: 3, total_tokens: 13 },
			});
		return sse([
			{
				...base,
				object: "chat.completion.chunk",
				choices: [
					{
						index: 0,
						delta: { role: "assistant", content: FAKE_REPLY },
						finish_reason: null,
					},
				],
			},
			{
				...base,
				object: "chat.completion.chunk",
				choices: [{ index: 0, delta: {}, finish_reason: "stop" }],
				usage: { prompt_tokens: 10, completion_tokens: 3, total_tokens: 13 },
			},
		]);
	},
});

// ---------------------------------------------------------------- SSH：临时 sshd、钥匙、主机

let sshdProc: Subprocess | undefined;
let sshdPort = 0;
let sshReady = false;
let sshSkip = "";
const sshHosts: { name: string; project?: string }[] = [];
let sshKeyMade = false;

function freePort(): number {
	const used = new Set(
		sh("ss -ltnH | awk '{print $4}' | sed 's/.*://'").stdout.split("\n"),
	);
	for (let i = 0; i < 200; i++) {
		const p = 22000 + Math.floor(Math.random() * 2000);
		if (!used.has(String(p))) return p;
	}
	throw new Error("找不到空闲端口");
}

async function startSshd() {
	if (!existsSync("/usr/bin/sshd")) {
		sshSkip = "本机没有 /usr/bin/sshd";
		return;
	}
	sshdPort = freePort();
	const cfg = `${SSHD_DIR}/sshd_config`;
	must(
		sh(
			`rm -rf ${SSHD_DIR} && install -d -m 755 ${SSHD_DIR} && ssh-keygen -q -t ed25519 -N '' -f ${SSHD_DIR}/host_ed25519 && : > ${SSHD_DIR}/authorized_keys && chmod 644 ${SSHD_DIR}/authorized_keys`,
		),
		"准备 sshd 目录",
	);
	writeFileSync(
		cfg,
		[
			`Port ${sshdPort}`,
			"ListenAddress 127.0.0.1",
			`HostKey ${SSHD_DIR}/host_ed25519`,
			"PidFile none",
			`AuthorizedKeysFile ${SSHD_DIR}/authorized_keys`,
			"StrictModes no",
			"PasswordAuthentication no",
			"KbdInteractiveAuthentication no",
			"PubkeyAuthentication yes",
			"UsePAM no",
			`AllowUsers ${SSH_USER}`,
			"Subsystem sftp internal-sftp",
			"",
		].join("\n"),
	);
	must(sh(`/usr/bin/sshd -t -f ${cfg}`), "sshd -t");
	sshdProc = Bun.spawn(
		[
			"bash",
			"-c",
			`exec /usr/bin/sshd -D -e -f ${cfg} >>${SSHD_DIR}/sshd.log 2>&1`,
		],
		{ stdin: "ignore", stdout: "ignore", stderr: "ignore" },
	);
	await waitFor(
		`sshd 监听 ${sshdPort}`,
		() => sh(`ss -ltnH 'sport = :${sshdPort}'`).stdout !== "",
		10_000,
		200,
	);
	const home = must(sh(`getent passwd ${SSH_USER} | cut -d: -f6`), "取家目录");
	must(
		sh(
			`printf '%s\\n' ${q(REMOTE_BODY)} > ${q(`${home}/${REMOTE_FILE}`)} && chown ${SSH_USER}: ${q(`${home}/${REMOTE_FILE}`)}`,
		),
		"写远端文件",
	);
	snote(
		`临时 sshd：/usr/bin/sshd -D -f ${cfg}（127.0.0.1:${sshdPort}，登录用户 ${SSH_USER}，日志 ${SSHD_DIR}/sshd.log）；远端文件 ${home}/${REMOTE_FILE}`,
	);
}

async function addHost(name: string, project?: string) {
	const r = await cli(
		`ssh add ${name} --host 127.0.0.1 --port ${sshdPort} --user ${SSH_USER} --key ${SSH_KEY} --desc ${q(`M21 验收 ${RUN}`)}${project ? ` --project ${project}` : ""}`,
	);
	sshHosts.push({ name, project });
	if (r.code !== 0) throw new Error(`oryn ssh add ${name} 失败：${r.out}`);
	snote(
		`SSH 主机 ${name}（${project ? `项目 ${project}` : "全局"}）：${one1(r.out)}`,
	);
}

// ---------------------------------------------------------------- 项目

const projects: { name: string; id: string }[] = [];

async function createProject(name: string): Promise<string> {
	const r = await cli(`project create ${name} --json`);
	if (r.code !== 0)
		throw new Error(`oryn project create ${name} 失败：${r.out}`);
	const id = (JSON.parse(r.stdout) as { id: string }).id;
	projects.push({ name, id });
	snote(`项目 ${name} = ${id}`);
	return id;
}

let prjA = "";
let prjB = "";

/** 项目与 SSH 主机：要在 runner 上线、线程开始之前就位（全局主机随 omphome.apply、项目主机随 agent.start 交付）。 */
async function prepareControl() {
	if (prjA) return;
	prjA = await createProject(`m21-${RUN}-a`);
	prjB = await createProject(`m21-${RUN}-b`);
	try {
		await startSshd();
		if (sshSkip) return;
		const k = await cli(`ssh key gen ${SSH_KEY}`);
		if (k.code !== 0) throw new Error(`oryn ssh key gen 失败：${k.out}`);
		sshKeyMade = true;
		const pub = k.stdout.trim().split("\n").at(-1) ?? "";
		writeFileSync(`${SSHD_DIR}/authorized_keys`, `${pub}\n`);
		snote(
			`dev 钥匙 ${SSH_KEY}：${pub.slice(0, 40)}… 写入 ${SSHD_DIR}/authorized_keys`,
		);
		await addHost(H_G);
		await addHost(H_PA, `m21-${RUN}-a`);
		await addHost(H_PB, `m21-${RUN}-b`);
		sshReady = true;
	} catch (e) {
		sshSkip = `SSH 布置失败：${e instanceof Error ? e.message : String(e)}`;
		snote(sshSkip);
	}
}

// ---------------------------------------------------------------- runner

let runner: RunnerView | undefined;
let runnerToken = "";
let runnerProc: Subprocess | undefined;
let runnerStartedAt = 0;

const orbdPid = () =>
	sh(`pgrep -u ${RUSER} -f ${q(`^${RORBD} run`)}`).stdout.split("\n")[0] ?? "";

async function createRunner(): Promise<RunnerView> {
	const name = `m21-${RUN}`;
	const r = await cli(`runner create ${name}`);
	if (r.code !== 0) throw new Error(`oryn runner create 失败：${r.out}`);
	runnerToken = r.stdout.trim().split("\n")[0] ?? "";
	check(
		runnerToken.startsWith("ort_"),
		`oryn runner create ${name} → ort_ 令牌`,
	);
	const rv = (await runners()).find((x) => x.name === name);
	if (!rv) throw new Error(`GET runners 里没有 ${name}`);
	runner = rv;
	note(`runner ${name} = ${rv.id} orb=${rv.orbId}`);
	return rv;
}

function startRunner() {
	const env = [
		`HOME=${uhome}`,
		`USER=${RUSER}`,
		`LOGNAME=${RUSER}`,
		"SHELL=/bin/bash",
		"LANG=C.UTF-8",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		`ORYN_URL=${RUNNER_URL}`,
		`ORYN_RUNNER_TOKEN=${runnerToken}`,
		`ORYN_AGENT_BIN=${AGENT_BIN}`,
		`ORYN_EGRESS_ADDR=${EGRESS}`,
	]
		.map(q)
		.join(" ");
	const args = `run --runner --dir ${DIR_A} --dir ${DIR_B} --dir ${DIR_C} --terminal`;
	runnerStartedAt = Date.now();
	runnerProc = Bun.spawn(
		[
			"bash",
			"-c",
			`exec runuser -u ${RUSER} -- env -i ${env} ${RORBD} ${args} >>${q(RUNNER_LOG)} 2>&1`,
		],
		{ cwd: "/", stdin: "ignore", stdout: "ignore", stderr: "ignore" },
	);
	note(
		`起 runner：orbd ${args}（ORYN_AGENT_BIN、ORYN_EGRESS_ADDR=${EGRESS}；日志 ${RUNNER_LOG}）`,
	);
}

async function stopRunner() {
	const p = runnerProc;
	if (!p) return;
	runnerProc = undefined;
	sh(`pkill -TERM -u ${RUSER} -f ${q(`^${RORBD} run`)}`);
	const done = await Promise.race([
		p.exited.then(() => true),
		sleep(15_000).then(() => false),
	]);
	if (!done) {
		sh(`pkill -KILL -u ${RUSER}`);
		p.kill(9);
		await p.exited;
	}
}

async function waitRunnerOnline(): Promise<RunnerView> {
	const id = runner?.id;
	if (!id) throw new Error("还没有 runner");
	return waitFor(
		`runner ${id} 在线`,
		async () => {
			if (runnerProc?.exitCode != null)
				throw new Error(
					`runner 进程已退出（${runnerProc.exitCode}）：${sh(`tail -n 20 ${q(RUNNER_LOG)}`).stdout}`,
				);
			const r = (await runners()).find((x) => x.id === id);
			return r?.online ? r : undefined;
		},
		120_000,
		1000,
	);
}

/** runner orb 进入 ready 的事件时刻（本次启动之后最早的一条）。 */
const readyAt = (orb: string) =>
	one<{ ts: number }>(
		`select ts from orb_events where orb_id='${orb}' and to_state='ready' and ts>=${runnerStartedAt} order by id limit 1`,
	)?.ts;

async function ensureRunnerReady(): Promise<RunnerView> {
	prepareHost();
	await prepareControl();
	if (!runner) await createRunner();
	if (!runnerProc) startRunner();
	const r = await waitRunnerOnline();
	const orb = r.orbId;
	await waitFor(`runner orb ${orb} ready`, () => readyAt(orb), 120_000, 500);
	return r;
}

// ---------------------------------------------------------------- 线程准备

const threadsMade: string[] = [];
let tA = "";
let tB = "";

async function newRunnerThread(o: {
	dir: string;
	tag: string;
	projectId?: string;
	prompt?: string;
	model?: { provider: string; id: string };
}): Promise<string> {
	const r = runner;
	if (!r) throw new Error("还没有 runner");
	const res = await api("POST", "/api/v1/threads", {
		prompt: o.prompt ?? "只回复 ok",
		title: `m21 ${RUN} ${o.tag}`,
		...(o.projectId ? { projectId: o.projectId } : {}),
		...(o.model ? { model: o.model } : {}),
		executor: { kind: "runner", runnerId: r.id, dir: o.dir },
	});
	const id = (res.json as { thread?: { id?: string } } | null)?.thread?.id;
	if (res.status !== 201 && res.status !== 200)
		throw new Error(`POST threads → ${res.status} ${res.text}`);
	if (!id) throw new Error(`POST threads 没有返回线程 id：${res.text}`);
	threadsMade.push(id);
	const st = await waitTurn(id, 0);
	note(
		`线程 ${o.tag} = ${id}（${o.dir}${o.projectId ? `，项目 ${o.projectId}` : ""}）首回合结束于 ${st}`,
	);
	check(st === "idle", `${id} 首回合 idle`);
	return id;
}

async function ensureA(): Promise<string> {
	await ensureRunnerReady();
	if (!tA)
		tA = await newRunnerThread({ dir: DIR_A, tag: "A", projectId: prjA });
	return tA;
}

async function ensureB(): Promise<string> {
	await ensureRunnerReady();
	if (!tB)
		tB = await newRunnerThread({ dir: DIR_B, tag: "B", projectId: prjB });
	return tB;
}

/** 线程 agent 进程（bun …cli.ts --mode rpc-ui … 的 --session-dir 含线程 id）的 pid。 */
const agentPids = (tid: string) =>
	sh(`pgrep -u ${RUSER} -f -- ${q(`--mode rpc-ui.*${tid}`)}`)
		.stdout.split("\n")
		.filter(Boolean);

function procEnv(pid: string): Record<string, string> {
	const r = sh(`tr '\\0' '\\n' < /proc/${pid}/environ`);
	const out: Record<string, string> = {};
	for (const l of r.stdout.split("\n")) {
		const i = l.indexOf("=");
		if (i > 0) out[l.slice(0, i)] = l.slice(i + 1);
	}
	return out;
}

/** 线程 agent 的 ORYN_SSH_KEY 与其公钥。 */
function threadKey(tid: string): { pid: string; key: string; pub: string } {
	const pid = agentPids(tid)[0];
	if (!pid) throw new Error(`找不到线程 ${tid} 的 agent 进程`);
	const key = procEnv(pid).ORYN_SSH_KEY ?? "";
	if (!key) throw new Error(`线程 ${tid} 的 agent 环境里没有 ORYN_SSH_KEY`);
	const pub = must(sh(`ssh-keygen -y -f ${q(key)}`), `ssh-keygen -y ${key}`);
	return { pid, key, pub };
}

/** runner 用户的 ssh master（`ssh -M`）进程：pid → 命令行。 */
function sshMasters(): Map<string, string> {
	const out = new Map<string, string>();
	for (const l of sh(`pgrep -u ${RUSER} -a -x ssh`).stdout.split("\n")) {
		const m = l.match(/^(\d+) (.*)$/);
		if (m?.[1] && m[2] && /(^| )-M( |$)|\[mux\]/.test(m[2]))
			out.set(m[1], m[2]);
	}
	return out;
}

const audits = (where: string) =>
	sql<AuditRow>(
		`select id, at, thread_id, host_name, kind, detail, ok from ssh_audit where ${where} order by id`,
	);
const auditLine = (a: AuditRow) =>
	`#${a.id} ${a.kind}${a.ok ? "" : "(失败)"} ${a.host_name} thread=${a.thread_id ?? "-"} ${a.detail.slice(0, 80)}`;

async function applyStatus() {
	return getJson<ApplyStatus>("/api/v1/agent-config/apply-status");
}
async function waitPushed(what: string) {
	await waitFor(
		`${what}下发完成`,
		async () => !(await applyStatus()).push.pending,
		120_000,
		500,
	);
}

// ---------------------------------------------------------------- 场景

async function R1() {
	note(
		"建 runner 并以普通用户起 orbd run --runner --terminal；核对 omphome.apply 先于 ready、models.yml 只有占位符与本机中转、用户配置不动",
	);
	prepareHost();
	await prepareControl();
	await createRunner();
	startRunner();
	const r = await waitRunnerOnline();
	note(
		`GET runners：online=${r.online} features=${r.features.join(",")} orbd=${r.orbdVersion}`,
	);
	check(r.features.includes("omphome-apply"), "hello 特性含 omphome-apply");
	const ready = await waitFor(
		`runner orb ready`,
		() => readyAt(r.orbId),
		120_000,
		500,
	);
	const evs = sql<{
		ts: number;
		from_state: string;
		to_state: string;
		detail: string;
	}>(
		`select ts, from_state, to_state, detail from orb_events where orb_id='${r.orbId}' and ts>=${runnerStartedAt} order by id`,
	);
	for (const e of evs)
		note(
			`orb 事件 ${e.ts} ${e.from_state}→${e.to_state} ${e.detail.slice(0, 160)}`,
		);
	const agentDir = `${uhome}/.oryn/omp/agent`;
	const models = `${agentDir}/models.yml`;
	const manifest = `${uhome}/.oryn/omp-home.manifest.json`;
	const mt = (p: string) => Number(sh(`stat -c %.3Y ${q(p)}`).stdout) * 1000;
	check(existsSync(models), `${models} 存在`);
	check(existsSync(manifest), `${manifest} 存在`);
	note(
		`models.yml mtime ${mt(models).toFixed(0)}，manifest mtime ${mt(manifest).toFixed(0)}，connecting→ready ${ready}`,
	);
	check(
		mt(manifest) <= ready,
		"omphome.apply 写完（manifest 落盘）早于 orb 进入 ready",
	);
	// models.yml：所有 apiKey 是占位符，所有 baseUrl 指向本机中转
	const doc = Bun.YAML.parse(readFileSync(models, "utf8"));
	const keys: string[] = [];
	const urls: string[] = [];
	const walk = (v: unknown) => {
		if (Array.isArray(v)) v.forEach(walk);
		else if (v && typeof v === "object")
			for (const [k, x] of Object.entries(v)) {
				if (k === "apiKey" && typeof x === "string") keys.push(x);
				else if (k === "baseUrl" && typeof x === "string") urls.push(x);
				else walk(x);
			}
	};
	walk(doc);
	const prefix = `http://${EGRESS}/llm/`;
	note(
		`models.yml：${keys.length} 个 apiKey、${urls.length} 个 baseUrl；例 ${keys[0] ?? "-"} / ${urls[0] ?? "-"}`,
	);
	const badKeys = keys.filter((k) => !k.startsWith("oryn-ph-"));
	const badUrls = urls.filter((u) => !u.startsWith(prefix));
	check(
		keys.length > 0 && badKeys.length === 0,
		`apiKey 全是 oryn-ph- 占位符（不合规 ${badKeys.length}）`,
	);
	check(
		urls.length > 0 && badUrls.length === 0,
		`baseUrl 全以 ${prefix} 开头（不合规：${badUrls.join(", ") || "无"}）`,
	);
	const text = readFileSync(models, "utf8");
	check(
		!text.includes("ORYN_MODEL_KEY_"),
		"models.yml 不含 ORYN_MODEL_KEY_ 降级",
	);
	const leaks = hostModelKeys().filter(
		(p) => sh(`grep -rlF -- ${q(p)} ${q(`${uhome}/.oryn/omp`)}`).stdout !== "",
	);
	check(
		leaks.length === 0,
		`~/.oryn/omp 下没有宿主 models.yml 的字面密钥（查了 ${hostModelKeys().length} 个前缀，命中 ${leaks.length}）`,
	);
	const pid = orbdPid();
	const listens = sh("ss -ltnpH")
		.stdout.split("\n")
		.filter((l) => l.includes(`pid=${pid},`))
		.map((l) => l.split(/\s+/)[3]);
	note(`runner orbd pid=${pid} 监听：${listens.join(", ")}`);
	check(listens.includes(EGRESS), `出站通道常驻 ${EGRESS}`);
	check(
		!existsSync(`${uhome}/.oryn/core`),
		"设了 ORYN_AGENT_BIN：没有下载 Core（~/.oryn/core 不存在；下载 / 校验 / 缓存待 Core 发布后补跑）",
	);
	await sleep(2000);
	const now = fileSig(`${uhome}/.omp/agent/config.yml`);
	check(
		now === sentinelSig,
		`哨兵 ~/.omp/agent/config.yml 内容与 mtime 不变（${now}）`,
	);
	check(treeSig(`${uhome}/.agents`) === agentsSig, "~/.agents 不变");
	check(!existsSync(`${uhome}/.ssh`), "~/.ssh 未被创建");
}

async function R2() {
	note(
		"线程 A（dev 默认模型）回复；另起线程 F 用临时的假模型 provider，核对假服务收到的 Authorization 是 orynd 注入的真实 key",
	);
	const a = await ensureA();
	const ra = await lastAssistant(a);
	note(`线程 A 回复：${one1(ra).slice(0, 120)}`);
	check(ra !== "", "线程 A 收到助手回复（dev 已配置的模型，经 runner 中转）");
	// 临时加假 provider
	const orig = await api("GET", "/api/v1/agent-config/files/models.yml");
	if (orig.status !== 200) throw new Error(`GET models.yml → ${orig.status}`);
	modelsOrig = orig.text;
	writeFileSync(`${STAGE}/models.orig-${RUN}.yml`, modelsOrig, { mode: 0o600 });
	const block = [
		`  ${FAKE_PROVIDER}:`,
		`    baseUrl: http://127.0.0.1:${fakeSrv.port}/v1`,
		`    apiKey: ${FAKE_KEY}`,
		"    api: openai-completions",
		"    auth: apiKey",
		"    models:",
		`      - id: ${FAKE_MODEL}`,
		"        name: M21 Fake",
		"        contextWindow: 32000",
		"        maxTokens: 4096",
	].join("\n");
	if (!/^providers:[ \t]*\n/m.test(modelsOrig))
		throw new Error("中央 models.yml 没有顶层 providers:");
	const patched = modelsOrig.replace(
		/^providers:[ \t]*\n/m,
		(m) => `${m}${block}\n`,
	);
	try {
		const put = await api(
			"PUT",
			"/api/v1/agent-config/files/models.yml",
			patched,
			true,
		);
		check(
			put.status === 200,
			`PUT models.yml（加 ${FAKE_PROVIDER}）→ ${put.status}`,
		);
		extracted =
			(put.json as { extractedSecrets?: string[] }).extractedSecrets ?? [];
		note(`抽进密钥库：${extracted.join(", ") || "无"}`);
		await waitPushed("models.yml ");
		const rm = `${uhome}/.oryn/omp/agent/models.yml`;
		await waitFor(
			`runner 的 models.yml 出现 ${FAKE_PROVIDER}`,
			() => readFileSync(rm, "utf8").includes(FAKE_PROVIDER),
			60_000,
			500,
		);
		const p = (
			Bun.YAML.parse(readFileSync(rm, "utf8")) as {
				providers: Record<string, { apiKey?: string; baseUrl?: string }>;
			}
		).providers[FAKE_PROVIDER];
		note(
			`runner 上的 ${FAKE_PROVIDER}：apiKey=${p?.apiKey} baseUrl=${p?.baseUrl}`,
		);
		check(
			!!p?.apiKey?.startsWith("oryn-ph-"),
			"runner 上该 provider 的 apiKey 是占位符",
		);
		check(
			!readFileSync(rm, "utf8").includes(FAKE_KEY),
			"runner 的 models.yml 不含真实 key",
		);
		const n0 = fakeSeen.length;
		const f = await newRunnerThread({
			dir: DIR_C,
			tag: "F",
			prompt: "hi",
			model: { provider: FAKE_PROVIDER, id: FAKE_MODEL },
		});
		const rf = await lastAssistant(f);
		note(`线程 F 回复：${rf.slice(0, 120)}`);
		check(rf.includes(FAKE_REPLY), `线程 F 的回复来自假模型（${FAKE_REPLY}）`);
		const seen = fakeSeen
			.slice(n0)
			.filter((s) => s.path.includes("chat/completions"));
		note(
			`假服务收到 ${seen.length} 个补全请求：${seen.map((s) => `${s.path} auth=${s.auth.slice(0, 22)}…`).join("；")}`,
		);
		check(seen.length > 0, "假服务收到补全请求（经 runner 中转 → orynd）");
		check(
			seen.every((s) => s.auth === `Bearer ${FAKE_KEY}`),
			"每个请求的 Authorization 都是真实 key（由 orynd 注入；runner 上只有占位符）",
		);
	} finally {
		await restoreModels((s) => note(s));
	}
}

let modelsOrig = "";
let extracted: string[] = [];

async function restoreModels(log: (s: string) => void) {
	if (!modelsOrig) return;
	const put = await api(
		"PUT",
		"/api/v1/agent-config/files/models.yml",
		modelsOrig,
		true,
	);
	const back = await api("GET", "/api/v1/agent-config/files/models.yml");
	log(
		`恢复 models.yml → ${put.status}；与原文${back.text === modelsOrig ? "一致" : `不一致！原文备份 ${STAGE}/models.orig-${RUN}.yml`}`,
	);
	if (back.text === modelsOrig) modelsOrig = "";
	for (const s of extracted) {
		const r = await cli(`secrets rm ${q(s)}`);
		log(`删除抽出的密钥 ${s} → ${r.code === 0 ? "ok" : r.out}`);
	}
	extracted = [];
	await waitPushed("恢复 models.yml ").catch((e) => log(String(e)));
}

/** 下发到 runner 的中央 config.yml 里 disabledProviders 是否含 p。 */
function centralDisables(p: string): boolean {
	const f = `${uhome}/.oryn/omp/agent/config.yml`;
	if (!existsSync(f)) return false;
	const doc = Bun.YAML.parse(readFileSync(f, "utf8")) as {
		disabledProviders?: unknown;
	} | null;
	return (
		Array.isArray(doc?.disabledProviders) && doc.disabledProviders.includes(p)
	);
}

async function R3() {
	note(
		"线程 A 的 get_skills：用户级 ~/.agents/skills/sentinel 不在，仓库内 .agents / .omp（与未被中央配置关闭时的 .claude）技能在",
	);
	const a = await ensureA();
	const r = await cli(`thread rpc ${a} ${q('{"type":"get_skills"}')}`);
	if (r.code !== 0) throw new Error(`thread rpc get_skills 失败：${r.out}`);
	const data = JSON.parse(r.stdout) as {
		skills?: {
			name: string;
			path?: string;
			filePath?: string;
			baseDir?: string;
		}[];
		data?: { skills?: { name: string }[] };
	};
	const skills = (data.skills ?? data.data?.skills ?? []) as {
		name: string;
		path?: string;
		filePath?: string;
		baseDir?: string;
	}[];
	const loc = (s: (typeof skills)[number]) =>
		s.filePath ?? s.path ?? s.baseDir ?? "";
	note(`技能 ${skills.length} 个：${skills.map((s) => s.name).join(", ")}`);
	const pid = agentPids(a)[0] ?? "";
	if (pid) {
		const env = procEnv(pid);
		note(
			`agent pid=${pid} PI_CONFIG_DIR=${env.PI_CONFIG_DIR} ORYN_ISOLATE_USER_SOURCES=${env.ORYN_ISOLATE_USER_SOURCES}`,
		);
	}
	check(skills.length > 0, "get_skills 有结果");
	check(
		!skills.some(
			(s) => s.name === "sentinel" || loc(s).startsWith(`${uhome}/.agents/`),
		),
		"用户级 sentinel（~/.agents/skills）不在列表里",
	);
	for (const [dir, name] of Object.entries(REPO_SKILLS)) {
		const s = skills.find((x) => x.name === name);
		if (dir === ".claude" && centralDisables("claude")) {
			note(
				`中央 config.yml 的 disabledProviders 含 claude：仓库内 ${dir}/skills/${name} ${s ? "仍在（意外）" : "不在"}，属 dev 配置所致，不计入判定`,
			);
			continue;
		}
		check(!!s, `仓库内 ${dir}/skills/${name} 在列表里（${s ? loc(s) : "-"}）`);
	}
	check(
		skills.some((s) => s.name === "oryn-library"),
		"runner 版 oryn-library 技能在列表里",
	);
}

async function R4() {
	note(
		"Runner 带 --terminal：特性含 terminal，agent 初始化（含 terminal_configure）成功",
	);
	const a = await ensureA();
	const r = (await runners()).find((x) => x.id === runner?.id);
	check(
		!!r?.features.includes("terminal"),
		`runner 特性含 terminal（${r?.features.join(",")}）`,
	);
	const fails = sh(`grep -c 'agent init failed' ${q(RUNNER_LOG)}`).stdout;
	note(`runner 日志里 agent init failed 次数：${fails}`);
	check(fails === "0", "runner 日志里没有 agent init failed");
	const st = await cli(`thread rpc ${a} ${q('{"type":"get_state"}')}`);
	check(st.code === 0, `线程 A 的 agent 应答 get_state（${st.code}）`);
	const tools = await cli(
		`thread rpc ${a} ${q('{"type":"get_active_tools"}')}`,
	);
	note(
		`get_active_tools：${one1(tools.stdout).replace(/\s+/g, " ").slice(0, 400)}`,
	);
}

async function R5() {
	note(
		`线程 A 调用 oryn_library_put 存入 ${LIB_FILE}；文件库出现该文件，来源为线程 A`,
	);
	const a = await ensureA();
	const r = await say(
		a,
		`调用 oryn_library_put 工具，path 为 ${LIB_FILE}（就在当前工作目录），存好后只回复 done`,
	);
	note(`回复：${one1(r.text).slice(0, 160)}`);
	const tools = await toolCalls(a, r.before);
	note(`本回合工具调用：${tools.join(" ; ") || "无"}`);
	// 扩展工具是 discoverable：直接调用，或经 xd://oryn_library_put 设备（write）调用
	check(
		tools.some(
			(t) =>
				t.startsWith("oryn_library_put ") ||
				t.includes("xd://oryn_library_put"),
		),
		"调用了 oryn_library_put（直接或经 xd:// 设备）",
	);
	const files = (
		await getJson<{ files: AssistantFile[] }>("/api/v1/assistant/files")
	).files;
	const f = files.find((x) => x.name === LIB_FILE);
	note(`文件库：${f ? JSON.stringify(f) : "没有该文件"}`);
	check(!!f, `文件库里有 ${LIB_FILE}`);
	if (!f) return;
	libFile = f.id;
	check(
		f.source === "orb" && f.sourceThreadId === a,
		`来源 orb、sourceThreadId=${f.sourceThreadId}`,
	);
	const res = await fetch(`${BASE}/api/v1/assistant/files/${f.id}/content`, {
		headers: { Authorization: `Bearer ${TOKEN}` },
	});
	const body = await res.text();
	check(body === LIB_BODY, `内容一致（${res.status}，${body.length} 字节）`);
}
let libFile = "";

/**
 * 让线程经 Core 的 `ssh://` 读取（read 工具，走 Core 的连接复用 master）在 hosts 上读 REMOTE_FILE，返回回复、工具调用与该回合
 * 的时间窗；断言用了 ssh:// 读取、读到的内容正确。
 */
async function readRemote(tid: string, tag: string, hosts: string[]) {
	const urls = hosts.map((h) => `ssh://${h}/~/${REMOTE_FILE}`);
	const t0 = Date.now();
	const r = await say(
		tid,
		`用 read 工具依次读取 ${urls.join("、")}（不要用 bash），只回复每个文件的内容，一行一个`,
	);
	const t1 = Date.now();
	const tools = await toolCalls(tid, r.before);
	note(`${tag} 回复：${one1(r.text).slice(0, 160)}；工具 ${tools.join(" ; ")}`);
	for (const u of urls)
		check(
			tools.some((t) => t.startsWith("read ") && t.includes(u)),
			`${tag} 用 read 读了 ${u}`,
		);
	check(r.text.includes(REMOTE_BODY), `${tag} 读到远端内容 ${REMOTE_BODY}`);
	return { t0, t1 };
}

/** [t0, t1] 内 host 上的审计行：至少一条成功 exec，且 exec / connect 全记在 tid 上。 */
function checkWindow(
	tid: string,
	tag: string,
	host: string,
	t0: number,
	t1: number,
) {
	const rows = audits(`at>=${t0} and at<=${t1} and host_name='${host}'`);
	for (const x of rows) note(`审计 ${auditLine(x)}`);
	const used = rows.filter((x) => x.kind === "exec" || x.kind === "connect");
	check(
		rows.some((x) => x.kind === "exec" && x.ok),
		`${tag} 在 ${host} 上有成功的 exec`,
	);
	check(
		used.every((x) => x.thread_id === tid),
		`${tag} 回合内 ${host} 的连接与 exec 全记到 ${tid}`,
	);
	return rows;
}

async function R6() {
	note(
		"两个项目各一个 Runner 线程经 Core 的 ssh://（read 工具，连接复用 master）读同一台全局主机：审计分别记到两个线程；本项目主机可连、对方项目主机被拒",
	);
	await ensureRunnerReady();
	if (!sshReady) throw new Skip(sshSkip || "SSH 未布置");
	const a = await ensureA();
	const b = await ensureB();
	const wa = await readRemote(a, "A", [H_G, H_PA]);
	const kA1 = threadKey(a);
	const mastersA = [...sshMasters().keys()].filter(
		(p) => procEnv(p).ORYN_SSH_KEY === kA1.key,
	);
	note(`A 的 ssh master：${mastersA.join(",") || "无"}`);
	check(mastersA.length > 0, "A 的 Core 起了以 A 的密钥认证的 ssh master");
	const wb = await readRemote(b, "B", [H_G]);
	const kB = threadKey(b);
	note(`A 的密钥 ${kA1.key}；B 的密钥 ${kB.key}`);
	check(kA1.pub !== kB.pub, "两个线程的 SSH 公钥不同");
	checkWindow(a, "A", H_G, wa.t0, wa.t1);
	checkWindow(a, "A", H_PA, wa.t0, wa.t1);
	const rb = checkWindow(b, "B", H_G, wb.t0, wb.t1);
	check(
		rb.some((x) => x.kind === "connect" && x.ok && x.thread_id === b),
		"B 对全局主机有自己的成功连接（没有复用 A 已认证的 master）",
	);
	// 本项目 / 对方项目主机：以线程自己的私钥直连本机网关（bash 的用法），网关按线程项目放行
	const port = procEnv(kA1.pid).ORYN_SSH_PORT ?? "7822";
	const via = (key: string, host: string, cmd: string) =>
		asUser(
			`ssh -F ~/.oryn/ssh/config -o BatchMode=yes -o ControlMaster=no -o ControlPath=none -o ConnectTimeout=15 -p ${port} ${host}@127.0.0.1 ${q(cmd)}`,
			{ ORYN_SSH_KEY: key },
		);
	const t1 = Date.now();
	const okB = via(kB.key, H_PB, `echo m21-${RUN}-B-pb`);
	note(`B 的密钥 → ${H_PB}：exit=${okB.code} ${one1(okB.out).slice(0, 120)}`);
	check(
		okB.code === 0 && okB.stdout.includes(`m21-${RUN}-B-pb`),
		`B 连本项目主机 ${H_PB} 成功`,
	);
	const denyA = via(kA1.key, H_PB, "echo nope");
	note(
		`A 的密钥 → ${H_PB}：exit=${denyA.code} ${one1(denyA.out).slice(0, 160)}`,
	);
	const denyB = via(kB.key, H_PA, "echo nope");
	note(
		`B 的密钥 → ${H_PA}：exit=${denyB.code} ${one1(denyB.out).slice(0, 160)}`,
	);
	check(
		denyA.code !== 0 && !denyA.stdout.includes("nope"),
		`A 连对方项目主机 ${H_PB} 被拒`,
	);
	check(
		denyB.code !== 0 && !denyB.stdout.includes("nope"),
		`B 连对方项目主机 ${H_PA} 被拒`,
	);
	const rows2 = audits(`at>=${t1} and host_name in ('${H_PA}','${H_PB}')`);
	for (const x of rows2) note(`审计 ${auditLine(x)}`);
	check(
		rows2.some(
			(x) => x.kind === "deny" && x.thread_id === a && x.host_name === H_PB,
		),
		`审计有 A → ${H_PB} 的 deny`,
	);
	check(
		rows2.some(
			(x) => x.kind === "deny" && x.thread_id === b && x.host_name === H_PA,
		),
		`审计有 B → ${H_PA} 的 deny`,
	);
	check(!existsSync(`${uhome}/.ssh`), "用过 SSH 之后 ~/.ssh 仍未被创建");
	check(
		fileSig(`${uhome}/.omp/agent/config.yml`) === sentinelSig,
		"哨兵 ~/.omp/agent/config.yml 仍不变",
	);
}

const PROBE_OK = `m21-probe-ok-${RUN}.json`;
const PROBE_SECRET = `m21-probe-secret-${RUN}.json`;
let probesPut = false;
let centralBefore = "";

const centralList = async () =>
	(
		await getJson<{ files: { path: string; sha256: string }[] }>(
			"/api/v1/agent-config/files",
		)
	).files
		.map((f) => `${f.path} ${f.sha256}`)
		.sort()
		.join("\n");

async function R7() {
	note(
		"中央配置放一个含形似密钥串的文件：Runner 上没有该文件、orb 事件备注写了路径；对照文件照常下发；结束时删除恢复",
	);
	const r = await ensureRunnerReady();
	centralBefore = await centralList();
	const tok = `ghp_${Array.from({ length: 36 }, () => "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"[Math.floor(Math.random() * 62)]).join("")}`;
	const t0 = Date.now();
	probesPut = true;
	const p1 = await api(
		"PUT",
		`/api/v1/agent-config/files/${PROBE_OK}`,
		`{"m21": "${RUN}"}\n`,
		true,
	);
	const p2 = await api(
		"PUT",
		`/api/v1/agent-config/files/${PROBE_SECRET}`,
		`{"mcpServers": {"m21": {"command": "true", "env": {"TOKEN": "${tok}"}}}}\n`,
		true,
	);
	note(
		`PUT ${PROBE_OK} → ${p1.status}；PUT ${PROBE_SECRET} → ${p2.status} ${p2.text.slice(0, 200)}`,
	);
	check(p1.status === 200 && p2.status === 200, "两个探针文件写入中央配置");
	await waitPushed("探针 ");
	const dir = `${uhome}/.oryn/omp/agent`;
	await waitFor(
		`${PROBE_OK} 下发到 runner`,
		() => existsSync(`${dir}/${PROBE_OK}`),
		60_000,
		500,
	);
	check(
		existsSync(`${dir}/${PROBE_OK}`),
		`对照文件 ${PROBE_OK} 已下发到 runner`,
	);
	check(
		!existsSync(`${dir}/${PROBE_SECRET}`),
		`含形似密钥串的 ${PROBE_SECRET} 不在 runner 的 ~/.oryn/omp/agent`,
	);
	check(
		sh(`grep -rlF -- ${q(tok)} ${q(`${uhome}/.oryn`)}`).stdout === "",
		"测试 token 不出现在 runner 的 ~/.oryn 下",
	);
	const ev = await waitFor(
		"orb 事件备注",
		() =>
			sql<{ detail: string }>(
				`select detail from orb_events where orb_id='${r.orbId}' and ts>=${t0} and instr(detail,'runner config withheld')>0 order by id`,
			).find((e) => e.detail.includes(PROBE_SECRET)),
		30_000,
		500,
	);
	note(`orb 事件：${ev.detail.slice(0, 300)}`);
	check(!ev.detail.includes(tok), "事件备注不含 token 本身");
	await removeProbes((s) => note(s));
	await waitFor(
		`${PROBE_OK} 从 runner 上清掉`,
		() => !existsSync(`${dir}/${PROBE_OK}`),
		60_000,
		500,
	);
	check(!existsSync(`${dir}/${PROBE_OK}`), "删除后对照文件从 runner 上清掉");
	const after = await centralList();
	check(after === centralBefore, "中央配置文件清单与 sha256 恢复原状");
}

async function removeProbes(log: (s: string) => void) {
	if (!probesPut) return;
	for (const p of [PROBE_OK, PROBE_SECRET]) {
		const d = await api("DELETE", `/api/v1/agent-config/files/${p}`);
		log(`删除中央配置 ${p} → ${d.status}`);
	}
	probesPut = false;
	await waitPushed("删除探针 ").catch((e) => log(String(e)));
}

async function R8() {
	note(
		"同一线程换一代监督器（连续 kill -9 agent 进程组直到监督器用尽自重启、结束 → 再发消息由 agent.start 拉起）：公钥变化，新公钥重新认证，旧连接被网关关闭，别的线程的连接不受影响",
	);
	await ensureRunnerReady();
	if (!sshReady) throw new Skip(sshSkip || "SSH 未布置");
	const a = await ensureA();
	// 先确保有一条旧公钥认证的 Core 连接（R6 单跑时现建）
	const w0 = await readRemote(a, "换代前 A", [H_G]);
	const k1 = threadKey(a);
	const masters1 = sshMasters();
	note(
		`换代前：agent pid=${k1.pid} 密钥 ${k1.key}；ssh master ${[...masters1].map(([p, c]) => `${p}:${c.slice(0, 100)}`).join(" ; ") || "无"}`,
	);
	// 旧一代的 master：环境里 ORYN_SSH_KEY 是旧私钥（master 由 Core 起，继承其环境）；别的线程的 master 不应受影响
	const old = [...masters1.keys()].filter(
		(p) => procEnv(p).ORYN_SSH_KEY === k1.key,
	);
	const others = [...masters1.keys()].filter((p) => !old.includes(p));
	checkWindow(a, "换代前 A", H_G, w0.t0, w0.t1);
	// 换代：只动线程 A——连续杀掉 agent 进程组（崩溃），自重启（沿用同一把密钥）用尽后监督器结束、公钥注销；
	// 不用 restart-sessions（它会顺带重启 dev 里所有带「需重启」标记的会话）
	const killed: string[] = [];
	for (let i = 0; i < 8; i++) {
		const pid = agentPids(a)[0];
		if (!pid) break;
		const pgid = sh(`ps -o pgid= -p ${pid}`).stdout.trim();
		sh(`kill -KILL -- -${pgid || pid}`);
		killed.push(pid);
		await waitFor(
			`agent ${pid} 退出`,
			() => !existsSync(`/proc/${pid}`),
			10_000,
			200,
		);
		// 等自重启（延迟 1s / 2s / 4s，加启动时间）；12 秒内没有新进程即视为监督器已结束
		await waitFor(
			"新 agent 进程",
			() => agentPids(a).some((p) => !killed.includes(p)),
			12_000,
			300,
		).catch(() => undefined);
	}
	note(`依次杀掉的 agent：${killed.join(",")}`);
	check(
		agentPids(a).length === 0,
		`监督器自重启用尽后结束（杀了 ${killed.length} 次）`,
	);
	note("线程 A 的 agent 已退出");
	const tStop = Date.now();
	const gone = await waitFor(
		"旧 ssh master 退出",
		() => (old.every((p) => !existsSync(`/proc/${p}`)) ? true : undefined),
		30_000,
		500,
	).catch(() => false);
	note(
		`旧 ssh master（${old.join(",") || "无"}）${gone ? "均已退出" : `仍在：${old.filter((p) => existsSync(`/proc/${p}`)).join(",")}`}`,
	);
	check(old.length > 0, "换代前存在 Core 的 ssh master（连接复用）");
	check(gone === true, "旧一代公钥认证的连接已关闭（旧 master 退出）");
	const w1 = await readRemote(a, "换代后 A", [H_G]);
	const k2 = threadKey(a);
	note(`换代后：agent pid=${k2.pid} 密钥 ${k2.key}`);
	check(k2.pid !== k1.pid, "agent 是新进程");
	check(k2.pub !== k1.pub, "公钥变了（新一代监督器）");
	check(k2.key !== k1.key, "私钥路径按监督器代分开");
	check(!existsSync(k1.key), "旧一代私钥已删除");
	const rows = checkWindow(a, "换代后 A", H_G, w1.t0, w1.t1);
	const conn = rows.find((x) => x.kind === "connect" && x.ok && x.at >= tStop);
	const exec1 = rows.find((x) => x.kind === "exec" && x.ok);
	check(
		!!conn && !!exec1 && conn.id < exec1.id,
		`换代后线程 A 多一条成功连接（新公钥认证），且早于这次的 exec（${conn ? auditLine(conn) : "无"}）`,
	);
	const othersAlive = others.filter((p) => existsSync(`/proc/${p}`));
	note(
		`别的线程的 ssh master（${others.join(",") || "无"}）仍在：${othersAlive.join(",") || "无"}`,
	);
	check(othersAlive.length === others.length, "别的线程的 master 不受影响");
	const masters2 = sshMasters();
	note(
		`换代后 ssh master：${[...masters2].map(([p, c]) => `${p}:${c.slice(0, 100)}`).join(" ; ") || "无"}`,
	);
	check(
		[...masters2.keys()].every((p) => !old.includes(p)),
		"没有沿用旧 master",
	);
}

/** runner 用户里环境 ORYN_SSH_KEY 为 key 的 ssh master：pid → 命令行。 */
const mastersOf = (key: string) =>
	[...sshMasters()].filter(([p]) => procEnv(p).ORYN_SSH_KEY === key);

/**
 * 以线程密钥与 Core 的 ControlPath（取自 master 命令行）在 host 上执行一条命令，等同 Core 经 ssh.json 的
 * `127.0.0.1:<网关端口>`、用户名 = 主机名发起的连接：master 还在就复用它开新通道，不在就直连网关（ControlMaster=no，
 * 不新建 master）。
 */
function coreLikeSsh(
	key: string,
	port: string,
	controlPath: string,
	host: string,
	cmd: string,
) {
	return asUser(
		`ssh -F ~/.oryn/omp/ssh/config -o BatchMode=yes -o ControlMaster=no -o ControlPath=${q(controlPath)} -o ConnectTimeout=15 -p ${port} ${host}@127.0.0.1 ${q(cmd)}`,
		{ ORYN_SSH_KEY: key },
	);
}

async function R9() {
	note(
		"Runner 线程经 Core 建立 SSH master 后归档（archived，再用 shipped 重复）：网关关闭该连接，复用旧 master / 新连接都失败；取消归档后能再连",
	);
	await ensureRunnerReady();
	if (!sshReady) throw new Skip(sshSkip || "SSH 未布置");
	const a = await ensureA();
	// 同一个 Core 进程里，master 被网关断开之后对该主机不再常驻新 master（之后每次操作临时连接），所以 shipped 那一半换用
	// 本项目主机 H_PA（这一代 agent 还没连过，Core 会新起常驻 master）
	for (const [field, host] of [
		["archived", H_G],
		["shipped", H_PA],
	] as const) {
		note(`—— ${field}（${host}）`);
		await readRemote(a, `${field} 前 A`, [host]);
		const k = threadKey(a);
		const port = procEnv(k.pid).ORYN_SSH_PORT ?? "7822";
		const ms = mastersOf(k.key);
		note(
			`A 的 ssh master：${ms.map(([p, c]) => `${p}:${c.slice(0, 160)}`).join(" ; ") || "无"}`,
		);
		check(ms.length > 0, `${field} 前 A 有以自己密钥认证的 master`);
		const cp = ms[0]?.[1].match(/ControlPath=(\S+)/)?.[1] ?? "";
		check(cp !== "", `master 命令行里有 ControlPath（${cp}）`);
		const pre = coreLikeSsh(
			k.key,
			port,
			cp,
			host,
			`echo m21-${RUN}-${field}-pre`,
		);
		note(
			`${field} 前经 master 执行：exit=${pre.code} ${one1(pre.out).slice(0, 120)}`,
		);
		check(
			pre.code === 0 && pre.stdout.includes(`m21-${RUN}-${field}-pre`),
			`${field} 前复用 master 执行成功`,
		);
		const t0 = Date.now();
		const p1 = await api("PATCH", `/api/v1/threads/${a}`, { [field]: true });
		check(
			p1.status === 200,
			`PATCH threads/${a} {${field}:true} → ${p1.status}`,
		);
		const gone = await waitFor(
			"master 退出",
			() => (ms.every(([p]) => !existsSync(`/proc/${p}`)) ? true : undefined),
			30_000,
			500,
		).catch(() => false);
		note(
			`master ${ms.map(([p]) => p).join(",")} ${gone ? "已退出" : "仍在"}；agent ${agentPids(a).join(",") || "不在"}`,
		);
		check(gone === true, `${field} 后网关关闭了该连接（master 退出）`);
		const denied = coreLikeSsh(
			k.key,
			port,
			cp,
			host,
			`echo m21-${RUN}-${field}-denied`,
		);
		note(
			`${field} 后再执行：exit=${denied.code} ${one1(denied.out).slice(0, 200)}`,
		);
		check(
			denied.code !== 0 &&
				!denied.stdout.includes(`m21-${RUN}-${field}-denied`),
			`${field} 后以该线程的密钥执行 ssh 失败（旧 master 不可复用、新连接被拒）`,
		);
		for (const x of audits(
			`at>=${t0} and (thread_id='${a}' or host_name='${host}')`,
		))
			note(`审计 ${auditLine(x)}`);
		// shipped:true 顺带归档；只撤 shipped 不取消归档，所以恢复时一并 archived:false
		const undo =
			field === "shipped"
				? { shipped: false, archived: false }
				: { archived: false };
		const p2 = await api("PATCH", `/api/v1/threads/${a}`, undo);
		check(
			p2.status === 200,
			`PATCH threads/${a} ${JSON.stringify(undo)} → ${p2.status}`,
		);
		const kNow = threadKey(a);
		check(kNow.pid === k.pid, `取消后 agent 仍是同一进程（${kNow.pid}）`);
		const t1 = Date.now();
		const again = await waitFor(
			"取消后能再连",
			() => {
				const r = coreLikeSsh(
					k.key,
					port,
					cp,
					host,
					`echo m21-${RUN}-${field}-again`,
				);
				return r.code === 0 && r.stdout.includes(`m21-${RUN}-${field}-again`)
					? r
					: undefined;
			},
			30_000,
			1000,
		).catch(() => undefined);
		check(!!again, `取消 ${field} 后以该线程的密钥能再连 ${host}`);
		const rows = audits(`at>=${t1} and host_name='${host}'`);
		for (const x of rows) note(`审计 ${auditLine(x)}`);
		check(
			rows.some((x) => x.kind === "connect" && x.ok && x.thread_id === a),
			"取消后的连接是线程 A 的一次新认证",
		);
	}
	// Core 自己再连（ssh:// 读取）
	await readRemote(a, "取消 shipped 后 A", [H_PA]);
}

async function R10() {
	note(
		"崩溃重启退避中被 agent.stop：网关断开该线程现有 SSH 连接（f687e32c7a）",
	);
	throw new Skip(
		"Runner 上无法从外部可靠地在退避窗口内触发一次「不自带注销」的 agent.stop：退避固定 1s/2s/4s（Config.RestartDelays，runner 无环境变量可调）；控制面对 runner 线程发 agent.stop 的路径只有删线程（deleteThread 先 dropThreadKey，网关断开与该修复无关，不能区分）、refreshLocked / restart-sessions（要求线程先有「需重启」标记，会波及 dev 里其它带标记的会话，且 4 秒窗口内是否发出 stop 取决于 agent 流与状态，未核实）与 fork share 回退；该分支由 orbdrt 单测（1 小时退避后 agent.stop 收到终态）覆盖",
	);
}

const TESTS: [string, () => Promise<void>][] = [
	["R1", R1],
	["R2", R2],
	["R3", R3],
	["R4", R4],
	["R5", R5],
	["R6", R6],
	["R7", R7],
	["R8", R8],
	["R9", R9],
	["R10", R10],
];

// ---------------------------------------------------------------- 收尾

let cleaned = false;
async function cleanup() {
	if (cleaned) return;
	cleaned = true;
	const cn = (s: string) => {
		cleanupNotes.push(s);
		console.log(`    · ${s}`);
	};
	const tryApi = (m: string, p: string) =>
		api(m, p).catch(
			(e) => ({ status: 0, text: String(e), json: null }) as ApiRes,
		);
	await removeProbes(cn).catch((e) => cn(`删探针失败：${String(e)}`));
	await restoreModels(cn).catch((e) =>
		cn(`恢复 models.yml 失败：${String(e)}`),
	);
	if (libFile) {
		const r = await tryApi("DELETE", `/api/v1/assistant/files/${libFile}`);
		cn(`删除文件库文件 ${libFile} → ${r.status}`);
	}
	for (const t of threadsMade) {
		const r = await tryApi("DELETE", `/api/v1/threads/${t}`);
		cn(`删除线程 ${t} → ${r.status}`);
	}
	await stopRunner().catch((e) => cn(`停 runner 失败：${String(e)}`));
	if (hostPrepared) sh(`pkill -KILL -u ${RUSER}`);
	if (runner) {
		const r = await tryApi("DELETE", `/api/v1/runners/${runner.id}`);
		cn(`删除 runner ${runner.name} → ${r.status}`);
		runner = undefined;
	}
	for (const h of sshHosts) {
		const r = await cli(
			`ssh rm ${h.name}${h.project ? ` --project ${h.project}` : ""}`,
		);
		cn(`删除 SSH 主机 ${h.name} → ${r.code === 0 ? "ok" : r.out}`);
	}
	if (sshKeyMade) {
		const r = await cli(`ssh key rm ${SSH_KEY}`);
		cn(`删除 SSH 钥匙 ${SSH_KEY} → ${r.code === 0 ? "ok" : r.out}`);
	}
	if (sshdProc) {
		sshdProc.kill(15);
		await sshdProc.exited;
		cn(`停临时 sshd（127.0.0.1:${sshdPort}）`);
	}
	for (const p of projects) {
		const r = await tryApi("DELETE", `/api/v1/projects/${p.id}`);
		cn(
			`删除项目 ${p.name} → ${r.status} ${r.status >= 300 ? r.text.slice(0, 120) : ""}`,
		);
	}
	fakeSrv.stop(true);
}

for (const s of ["SIGINT", "SIGTERM"] as const)
	process.on(s, () => {
		console.log(`\n收到 ${s}，清理后退出`);
		void cleanup().finally(() => process.exit(130));
	});

// ---------------------------------------------------------------- 主流程

console.log(
	`M21 验收 run=${RUN} base=${BASE} runner 用户 ${RUSER} Core=${CORE_SRC} egress=${EGRESS} 假模型 127.0.0.1:${fakeSrv.port}`,
);

try {
	for (const [id, fn] of TESTS) {
		if (ONLY && !ONLY.has(id)) continue;
		evidence = [];
		console.log(`── ${id}`);
		const t0 = Date.now();
		const took = () => Number(((Date.now() - t0) / 1000).toFixed(1));
		try {
			await fn();
			console.log(`${id} PASS (${took()}s)`);
			results.push({ id, verdict: "PASS", seconds: took(), evidence });
		} catch (e) {
			if (e instanceof Skip) {
				note(`跳过：${e.message}`);
				console.log(`${id} SKIP (${took()}s)`);
				results.push({ id, verdict: "SKIP", seconds: took(), evidence });
				continue;
			}
			note(`错误：${e instanceof Error ? e.message : String(e)}`);
			failArtifacts(id);
			console.log(`${id} FAIL (${took()}s)`);
			results.push({ id, verdict: "FAIL", seconds: took(), evidence });
		}
	}
} finally {
	console.log("── 收尾");
	await cleanup();
}

sh(`mkdir -p ${q(OUT)}`);
const out = `${OUT}/results-${RUN}.json`;
await Bun.write(
	out,
	JSON.stringify(
		{ run: RUN, setup: setupNotes, cleanup: cleanupNotes, results },
		null,
		2,
	),
);
console.log(`\n汇总（${out}）`);
for (const r of results) console.log(`${r.id} ${r.verdict} ${r.seconds}s`);
const failed = results.some((r) => r.verdict === "FAIL");
console.log(`VERDICT ${failed ? "FAIL" : "PASS"}`);
process.exit(failed ? 1 : 0);
