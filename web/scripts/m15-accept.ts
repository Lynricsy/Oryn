#!/usr/bin/env bun
import {
	copyFileSync,
	existsSync,
	mkdirSync,
	openSync,
	readdirSync,
	readFileSync,
	readlinkSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import type { Subprocess } from "bun";

/**
 * M15 验收的真机部分（docs/plan-m15.md 步骤 9.2）：在 orb 里用真实的 gh 执行 `gh api user` 与 `gh pr create`，请求经
 * orb 的 gh 代理（$HOME/.oryn/gh.sock）→ orbd 的出站代理 → link → 控制面 egress（换上 OAuth 用户令牌）→ 假 GitHub。
 * 其余场景（fork、门禁、分诊、自动关闭、发布哨兵、审计、防回流）由 Go 端到端覆盖：
 * `go test ./internal/server/... -run E2EM15`。
 *
 * 用法（web/ 目录）：bun run accept:m15 [-- --help]
 *
 * 自建一套隔离环境（全部在 /var/tmp/oryn-m15-<run>/ 下，结束时删除；M15_KEEP=1 保留，日志在 logs/）：
 *   - 假 GitHub：`go build ./cmd/fakegithub` 后以 `--listen 127.0.0.1:7650 --git-root <tmp>/git` 启动，同一地址同时是
 *     API 与网页基址；经 `/__fake/seed` 种下用户与仓库、`/__fake/oauth` 登记授权码、`/__fake/app` 取 OAuth App 凭据、
 *     `/__fake/calls` 读它收到的请求。
 *   - orynd（127.0.0.1:7660，data_dir 全新）；provider `local`（driver process，orbd = $M15_BIN/orbd，agent = fakeomp）：
 *     orb 的 HOME 是 `<tmp>/orbs/orbs/<orb id>/home`，检出在 `$HOME/workspace/repo`。
 *   - 浏览器不参与：OAuth 的「去 GitHub 授权」一步由脚本直接请求回调地址代替（授权码事先登记到假 GitHub）。
 * 前提：有 go 工具链、git、gh；`make build`（或 M15_BIN 指向含 orynd / orbd / oryn / fakeomp 的目录）。不需要 root、
 * docker 或 provider 镜像（process 驱动的 orbd 直接跑在本机）。
 *
 * 每步打印 `M15 <步骤> PASS|FAIL`，全部 PASS 时退出码 0。
 */

const USAGE = `用法：bun scripts/m15-accept.ts [--help]
  环境变量：M15_BIN（缺省 <仓库>/bin）、M15_KEEP=1（保留临时目录）。`;
if (process.argv.includes("--help") || process.argv.includes("-h")) {
	console.log(USAGE);
	process.exit(0);
}

const ROOT = `${import.meta.dir}/../..`;
const BIN = process.env.M15_BIN ?? `${ROOT}/bin`;
const KEEP = process.env.M15_KEEP === "1";
const RUN = Math.random().toString(36).slice(2, 6);
const TMP = `/var/tmp/oryn-m15-${RUN}`;
const LOGS = `${TMP}/logs`;
const FAKE_PORT = 7650;
const FAKE = `http://127.0.0.1:${FAKE_PORT}`;
const API_PORT = 7660;
const API = `http://127.0.0.1:${API_PORT}`;
/** public_url 要求是域名（WebAuthn 的 RP ID 不接受 IP），回调地址按它生成。 */
const PUBLIC = `http://localhost:${API_PORT}`;

const REPO = "acme/widgets";
const REPO_ID = 42;
const SCOPES =
	"repo workflow read:org admin:repo_hook gist write:packages user:email";
const USERS = {
	owner: { id: 1, login: "owner", name: "The Owner", type: "User" },
	bot: { id: 7, login: "oryn-bot", name: "", type: "User" },
} as const;

// ---------------------------------------------------------------- 工具

const sleep = (ms: number) => Bun.sleep(ms);
const q = (s: string) => `'${s.replaceAll("'", "'\\''")}'`;
const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();
const clip = (s: string, n = 240) =>
	s.length > n ? `${s.slice(0, n)}…（共 ${s.length} 字）` : s;

function sh(
	cmd: string,
	env?: Record<string, string | undefined>,
): { code: number; out: string; stdout: string } {
	const r = Bun.spawnSync(["bash", "-c", cmd], { cwd: ROOT, env });
	return {
		code: r.exitCode ?? -1,
		out: (r.stdout.toString() + r.stderr.toString()).trim(),
		stdout: r.stdout.toString().trim(),
	};
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
	intervalMs = 250,
): Promise<T> {
	const deadline = Date.now() + timeoutMs;
	let last: unknown;
	while (Date.now() < deadline) {
		try {
			const v = await fn();
			if (v) return v as T;
		} catch (e) {
			last = e;
		}
		await sleep(intervalMs);
	}
	throw new Error(
		`等待超时（${timeoutMs}ms）：${what}${last ? `；最后错误：${last instanceof Error ? last.message : String(last)}` : ""}`,
	);
}

// ---------------------------------------------------------------- 结果

let failures = 0;
let stepFails = 0;
const note = (s: string) => console.log(`    · ${s}`);
/** 硬断言：不成立就结束本步（以及其后依赖它的步骤）。 */
const check = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) throw new Error(`断言失败：${what}`);
};
/** 软断言：记下失败继续走，本步结尾判 FAIL。 */
const expect = (ok: boolean, what: string) => {
	note(`${ok ? "✓" : "✗"} ${what}`);
	if (!ok) stepFails++;
};

async function step(name: string, fn: () => Promise<void>) {
	console.log(`M15 ${name}`);
	stepFails = 0;
	const t0 = Date.now();
	try {
		await fn();
	} catch (e) {
		note(`✗ ${e instanceof Error ? e.message : String(e)}`);
		stepFails++;
		console.log(
			`M15 ${name} FAIL（${((Date.now() - t0) / 1000).toFixed(1)}s）`,
		);
		failures++;
		throw e;
	}
	const verdict = stepFails === 0 ? "PASS" : "FAIL";
	if (stepFails) failures++;
	console.log(
		`M15 ${name} ${verdict}（${((Date.now() - t0) / 1000).toFixed(1)}s）`,
	);
}

// ---------------------------------------------------------------- 后台进程

interface Bg {
	name: string;
	proc: Subprocess;
}
const bgs: Bg[] = [];

function bg(
	name: string,
	cmd: string[],
	env?: Record<string, string | undefined>,
): Bg {
	mkdirSync(LOGS, { recursive: true });
	const fd = openSync(`${LOGS}/${name}.log`, "a");
	const proc = Bun.spawn(cmd, {
		cwd: ROOT,
		env: env ?? process.env,
		stdin: "ignore",
		stdout: fd,
		stderr: fd,
	});
	const b = { name, proc };
	bgs.push(b);
	return b;
}

async function stopAll() {
	for (const b of [...bgs].reverse()) {
		if (b.proc.exitCode === null && b.proc.signalCode === null) {
			b.proc.kill("SIGTERM");
			const r = await Promise.race([
				b.proc.exited,
				sleep(8_000).then(() => "timeout"),
			]);
			if (r === "timeout") b.proc.kill("SIGKILL");
		}
	}
	// process 驱动的 orbd 与 agent 不是 provider 的子进程组成员：按工作目录清理
	for (const pid of readdirSync("/proc")) {
		if (!/^\d+$/.test(pid) || Number(pid) === process.pid) continue;
		try {
			if (readlinkSync(`/proc/${pid}/cwd`).startsWith(TMP))
				process.kill(Number(pid), "SIGKILL");
		} catch {
			// 已退出或无权限
		}
	}
	if (!existsSync(TMP)) return;
	if (KEEP) console.log(`保留 ${TMP}（日志在 logs/）`);
	else rmSync(TMP, { recursive: true, force: true });
}

// ---------------------------------------------------------------- HTTP

let token = "";

async function http(
	base: string,
	method: string,
	path: string,
	body?: unknown,
	auth = true,
) {
	const res = await fetch(`${base}${path}`, {
		method,
		headers: {
			...(auth && base === API
				? { Authorization: `Bearer ${token}`, "X-Oryn-CSRF": "1" }
				: {}),
			...(body !== undefined ? { "Content-Type": "application/json" } : {}),
		},
		body: body === undefined ? undefined : JSON.stringify(body),
		redirect: "manual",
	});
	const text = await res.text();
	let json: unknown = null;
	try {
		json = JSON.parse(text);
	} catch {
		// 空体
	}
	return {
		status: res.status,
		text,
		json,
		location: res.headers.get("location") ?? "",
	};
}
async function rjson<T>(
	method: string,
	path: string,
	body?: unknown,
): Promise<T> {
	const r = await http(API, method, path, body);
	if (r.status >= 300)
		throw new Error(`${method} ${path} → ${r.status} ${clip(r.text)}`);
	return r.json as T;
}
async function fakeJson<T>(
	method: string,
	path: string,
	body?: unknown,
): Promise<T> {
	const r = await http(FAKE, method, path, body);
	if (r.status >= 300)
		throw new Error(
			`假 GitHub ${method} ${path} → ${r.status} ${clip(r.text)}`,
		);
	return r.json as T;
}
interface FakeCall {
	method: string;
	path: string;
	query: string;
	auth: string;
	body: string;
}
const fakeCalls = () => fakeJson<FakeCall[]>("GET", "/__fake/calls");

interface ThreadView {
	id: string;
	state: string;
	orbId?: string;
	pulls?: { repo: string; number: number; state: string }[];
}
const getThread = (id: string) =>
	rjson<ThreadView>("GET", `/api/v1/threads/${id}`);

// ---------------------------------------------------------------- orb

const orbHome = (orbId: string) => `${TMP}/orbs/orbs/${orbId}/home`;
const orbRepo = (orbId: string) => `${orbHome(orbId)}/workspace/repo`;
const ORB_PATH = process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin";
/** 以 orb 的 HOME 与 gh 配置目录（与 agent 的环境相同）在检出目录里执行命令。 */
const orbSh = (orbId: string, cmd: string) =>
	sh(
		`cd ${q(orbRepo(orbId))} && env -i PATH=${q(ORB_PATH)} HOME=${q(orbHome(orbId))} GH_CONFIG_DIR=${q(`${orbHome(orbId)}/.oryn/gh`)} GH_NO_UPDATE_NOTIFIER=1 GH_PROMPT_DISABLED=1 LC_ALL=C bash -c ${q(cmd)}`,
	);

// ---------------------------------------------------------------- 步骤

let projectId = "";
let threadId = "";
let orbId = "";
let ownerToken = "";

async function setup() {
	for (const b of ["orynd", "orbd", "oryn", "fakeomp"])
		if (!existsSync(`${BIN}/${b}`))
			throw new Error(`缺少 ${BIN}/${b}：先 make build（或设 M15_BIN）`);
	for (const tool of ["git", "gh", "go"])
		if (sh(`command -v ${tool}`).code !== 0)
			throw new Error(`本机缺少 ${tool}`);
	mkdirSync(`${TMP}/bin`, { recursive: true });
	copyFileSync(`${BIN}/fakeomp`, `${TMP}/bin/fakeomp`);
	writeFileSync(
		`${TMP}/bin/agent`,
		`#!/bin/sh\nexec ${TMP}/bin/fakeomp "$@"\n`,
		{ mode: 0o755 },
	);

	const built = sh(
		`go build -o ${q(`${TMP}/bin/fakegithub`)} ./cmd/fakegithub`,
	);
	check(
		built.code === 0,
		`构建 fakegithub${built.code ? `：${clip(built.out)}` : ""}`,
	);
	const fg = bg("fakegithub", [
		`${TMP}/bin/fakegithub`,
		"--listen",
		`127.0.0.1:${FAKE_PORT}`,
		"--git-root",
		`${TMP}/git`,
	]);
	await waitFor(
		"fakegithub 监听",
		() => {
			if (fg.proc.exitCode !== null)
				throw new Error(
					`fakegithub 已退出：${readFileSync(`${LOGS}/fakegithub.log`, "utf8").slice(-400)}`,
				);
			return readFileSync(`${LOGS}/fakegithub.log`, "utf8").includes(
				"fakegithub listening on",
			);
		},
		15_000,
	);
	await fakeJson("POST", "/__fake/seed", {
		users: Object.values(USERS),
		repos: [
			{
				id: REPO_ID,
				fullName: REPO,
				ownerId: 900,
				defaultBranch: "main",
				permissions: { owner: "admin", "oryn-bot": "write" },
			},
		],
	});
	note(`假 GitHub ${FAKE}：${REPO}（裸仓库在 ${TMP}/git）`);

	writeFileSync(
		`${TMP}/orynd.yaml`,
		`listen: ["127.0.0.1:${API_PORT}"]
public_url: "${PUBLIC}"
portal_domain: ""
data_dir: "${TMP}/data"
log_level: "debug"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h" }
orb_defaults: { provider: "local", type: "" }
github: { api_url: "${FAKE}", web_url: "${FAKE}" }
`,
	);
	bg("orynd", [`${BIN}/orynd`, "--config", `${TMP}/orynd.yaml`]);
	await waitFor(
		"orynd 就绪",
		async () => (await fetch(`${API}/healthz`)).ok,
		30_000,
	);
	const dev = sh(
		`${q(`${BIN}/orynd`)} --config ${q(`${TMP}/orynd.yaml`)} admin create-device --name m15-accept --kind cli`,
	);
	check(
		dev.code === 0,
		`orynd admin create-device${dev.code ? `：${clip(dev.out)}` : ""}`,
	);
	token = dev.stdout.split("\n").at(-1) ?? "";

	const prov = await rjson<{ token: string }>("POST", "/api/v1/providers", {
		name: "local",
	});
	writeFileSync(`${TMP}/provider.token`, `${prov.token}\n`, { mode: 0o600 });
	writeFileSync(
		`${TMP}/provider.yaml`,
		`url: "${API}"\ntoken_file: "${TMP}/provider.token"\norb_url: "${API}"\ndriver: process\n`,
	);
	bg(
		"provider",
		[`${BIN}/oryn`, "provider", "run", "--config", `${TMP}/provider.yaml`],
		{
			...process.env,
			ORYN_ORBD_BIN: `${BIN}/orbd`,
			ORYN_AGENT_BIN: `${TMP}/bin/agent`,
			ORYN_PROCESS_ROOT: `${TMP}/orbs`,
			// process 驱动的 orbd 共用宿主网络：不占 7811
			ORYN_EGRESS_ADDR: "127.0.0.1:0",
		},
	);
	await waitFor(
		"provider 在线",
		async () =>
			(
				await rjson<{ providers: { name: string; online: boolean }[] }>(
					"GET",
					"/api/v1/providers",
				)
			).providers.some((p) => p.name === "local" && p.online),
		60_000,
		500,
	);
	note(`orynd ${API}，provider local（process 驱动）在线`);
}

/** R1 / R2：保存 OAuth App，两个身份各走一遍授权回调。 */
async function oauth() {
	const app = await fakeJson<{
		oauthClientId: string;
		oauthClientSecret: string;
	}>("GET", "/__fake/app");
	const st = await rjson<{
		oauthApp?: { clientId: string };
		callbackUrl: string;
	}>("PUT", "/api/v1/github/oauth-app", {
		clientId: app.oauthClientId,
		clientSecret: app.oauthClientSecret,
		webhookBase: API,
	});
	expect(
		st.oauthApp?.clientId === app.oauthClientId,
		`OAuth App 已保存（clientId ${st.oauthApp?.clientId ?? "无"}）`,
	);
	expect(
		st.callbackUrl === `${PUBLIC}/api/v1/public/github/oauth/callback`,
		`回调地址 ${st.callbackUrl}`,
	);
	for (const [slot, u] of [
		["user", USERS.owner],
		["agent", USERS.bot],
	] as const) {
		const code = `m15-${slot}-${RUN}`;
		await fakeJson("POST", "/__fake/oauth", { code, login: u.login });
		const red = await rjson<{ url: string }>(
			"POST",
			`/api/v1/github/identities/${slot}/authorize`,
		);
		const url = new URL(red.url);
		expect(
			url.searchParams.get("scope") === SCOPES,
			`${slot} 授权地址的 scope = ${url.searchParams.get("scope")}`,
		);
		const cb = await http(
			API,
			"GET",
			`/api/v1/public/github/oauth/callback?code=${code}&state=${encodeURIComponent(url.searchParams.get("state") ?? "")}`,
			undefined,
			false,
		);
		check(
			cb.status === 302 && cb.location.endsWith(`?connected=${slot}`),
			`${slot} 回调 → ${cb.status} ${cb.location}`,
		);
	}
	const gh = await rjson<{
		identities: Record<string, { login: string; status: string } | undefined>;
	}>("GET", "/api/v1/github");
	check(
		gh.identities.user?.login === "owner" &&
			gh.identities.agent?.login === "oryn-bot",
		`两个身份已连接（user=${gh.identities.user?.login}，agent=${gh.identities.agent?.login}）`,
	);
	// 我的账号的令牌（用于确认它没有进 orb）：假 GitHub 按授权顺序签发
	const calls = await fakeCalls();
	const users = calls.filter(
		(c) =>
			c.method === "GET" && c.path === "/user" && c.auth.startsWith("Bearer "),
	);
	ownerToken = users[0]?.auth.slice("Bearer ".length) ?? "";
	check(ownerToken !== "", `取到我的账号的令牌（${ownerToken.slice(0, 6)}…）`);
}

/** R8 / R9：项目绑定 acme/widgets，Oryn 建仓库 hook；项目里开一个线程，等 orb 就绪。 */
async function projectThread() {
	const prj = await rjson<{
		id: string;
		github?: { bound: boolean; repoId: number };
	}>("POST", "/api/v1/projects", {
		name: "widgets",
		repoUrl: `https://github.com/${REPO}`,
	});
	projectId = prj.id;
	check(
		prj.github?.bound === true && prj.github.repoId === REPO_ID,
		`项目绑定到 ${REPO}（repoId ${prj.github?.repoId}）`,
	);
	const hooks = await rjson<{ hooks: { fullName: string; status: string }[] }>(
		"GET",
		"/api/v1/github/hooks",
	);
	expect(
		hooks.hooks.some(
			(h) => h.fullName.toLowerCase() === REPO && h.status === "active",
		),
		`仓库 hook active`,
	);

	const created = await rjson<{ thread: ThreadView }>(
		"POST",
		"/api/v1/threads",
		{
			prompt: "m15 accept: open a pull request with gh",
			projectId,
			executor: { kind: "new_orb", provider: "local" },
		},
	);
	threadId = created.thread.id;
	const th = await waitFor(
		"线程的 orb 就绪",
		async () => {
			const t = await getThread(threadId);
			if (t.state === "error") throw new Error(`线程 ${threadId} 停在 error`);
			return (t.state === "idle" || t.state === "awaiting_input") &&
				t.orbId &&
				existsSync(`${orbRepo(t.orbId)}/.git`)
				? t
				: null;
		},
		120_000,
		500,
	);
	orbId = th.orbId ?? "";
	note(`线程 ${threadId}，orb ${orbId}，检出 ${orbRepo(orbId)}`);
	check(
		existsSync(`${orbHome(orbId)}/.oryn/gh/hosts.yml`),
		"orb 里有 ~/.oryn/gh/hosts.yml（占位令牌）",
	);
}

/** R5：orb 里真实的 `gh api user` 返回我的账号；假 GitHub 收到的是 OAuth 令牌，orb 里没有它。 */
async function ghApiUser() {
	const before = (await fakeCalls()).length;
	const r = orbSh(orbId, "gh api user --jq .login");
	check(
		r.code === 0 && r.stdout === "owner",
		`gh api user --jq .login → ${clip(oneLine(r.out), 160)}（期望 owner）`,
	);
	const user = (await fakeCalls())
		.slice(before)
		.find((c) => c.method === "GET" && c.path === "/user");
	expect(
		user?.auth === `Bearer ${ownerToken}`,
		`假 GitHub 收到的 GET /user 带我的账号的 OAuth 令牌（${clip(user?.auth ?? "无请求", 20)}）`,
	);
	const hosts = readFileSync(`${orbHome(orbId)}/.oryn/gh/hosts.yml`, "utf8");
	expect(
		hosts.includes("oauth_token: oryn-proxied"),
		"hosts.yml 里只有占位令牌",
	);
	const grep = sh(
		`grep -rIl --binary-files=text -F ${q(ownerToken)} ${q(orbHome(orbId))} 2>/dev/null | head -5`,
	);
	expect(
		grep.stdout === "",
		`orb 的 HOME 下 grep 令牌无结果${grep.stdout ? `（${grep.stdout}）` : ""}`,
	);
}

/** R5 / R11 / R23：orb 里提交、git push（经出站代理），再用真实的 `gh pr create` 开 PR；PR 关联到线程并记审计。 */
async function ghPrCreate() {
	const branch = `m15-accept-${RUN}`;
	const push = orbSh(
		orbId,
		`git checkout -q -b ${branch} && echo "m15 ${RUN}" > m15.txt && git add m15.txt && git commit -qm 'm15: accept' && git push -q origin ${branch} 2>&1 && git rev-parse HEAD`,
	);
	check(
		push.code === 0,
		`orb 里提交并 git push（经出站代理）：${clip(oneLine(push.out), 200)}`,
	);
	const head = push.stdout.split("\n").at(-1) ?? "";
	const bare = sh(
		`git --git-dir ${q(`${TMP}/git/${REPO}.git`)} rev-parse refs/heads/${branch}`,
	);
	expect(
		bare.stdout === head,
		`假 GitHub 的裸仓库收到 ${branch}（${head.slice(0, 12)}）`,
	);

	// 不带 -R：gh 按 `git remote -v` 推断仓库（orb 的 origin 经 insteadOf 改写成出站代理地址，已报告为产品问题）
	const plain = orbSh(orbId, `gh pr view ${branch} --json number 2>&1`);
	expect(
		!plain.out.includes(
			"none of the git remotes configured for this repository point to a known GitHub host",
		),
		`检出目录里不带 -R 的 gh 能认出仓库（gh pr view → ${clip(oneLine(plain.out), 200)}）`,
	);
	const before = (await fakeCalls()).length;
	const pr = orbSh(
		orbId,
		`gh pr create -R ${REPO} --base main --head ${branch} --title 'm15: accept' --body 'Opened by m15-accept.'`,
	);
	check(
		pr.code === 0,
		`gh pr create -R ${REPO} → ${clip(oneLine(pr.out), 200)}`,
	);
	const url = pr.stdout.split("\n").at(-1) ?? "";
	expect(/\/acme\/widgets\/pull\/\d+$/.test(url), `gh 打印 PR 地址 ${url}`);
	const number = Number(url.split("/").at(-1));
	const gql = (await fakeCalls())
		.slice(before)
		.filter((c) => c.method === "POST" && c.path === "/graphql");
	const create = gql.find((c) => c.body.includes("createPullRequest"));
	expect(
		!!create && create.auth === `Bearer ${ownerToken}`,
		`createPullRequest 带 OAuth 令牌到达假 GitHub（共 ${gql.length} 个 GraphQL 请求）`,
	);
	expect(
		!!create?.body.includes(`"headRefName":"${branch}"`),
		"请求体原样转发（headRefName 未改写）",
	);

	const th = await waitFor(
		"PR 关联到线程",
		async () => {
			const t = await getThread(threadId);
			return t.pulls?.some((p) => p.repo === REPO && p.number === number)
				? t
				: null;
		},
		30_000,
		500,
	);
	expect(!!th, `线程关联 ${REPO}#${number}`);
	const audit = await rjson<{
		entries: {
			op: string;
			method: string;
			status: number;
			slot: string;
			bodySha256?: string;
		}[];
	}>("GET", `/api/v1/github/audit?thread=${threadId}`);
	const row = audit.entries.find((a) => a.op.startsWith("createPullRequest"));
	expect(
		!!row && row.status === 200 && row.slot === "user" && !!row.bodySha256,
		`审计里有 createPullRequest（${row ? `${row.method} ${row.op} ${row.status} ${row.slot}` : "无"}）`,
	);
	const pushRow = audit.entries.find(
		(a) => a.method === "PUSH" && a.op === `refs/heads/${branch}`,
	);
	expect(!!pushRow, `审计里有推送 refs/heads/${branch}`);
}

// ---------------------------------------------------------------- 主流程

let ok = false;
try {
	await step("环境", setup);
	await step("OAuth App 与两个身份", oauth);
	await step("项目绑定与线程", projectThread);
	await step("gh api user", ghApiUser);
	await step("gh pr create", ghPrCreate);
	ok = failures === 0;
} catch {
	// 已在 step 里打印
} finally {
	await stopAll();
}
console.log(
	ok ? "M15 真机验收 PASS" : `M15 真机验收 FAIL（${failures} 步失败）`,
);
process.exit(ok ? 0 : 1);
