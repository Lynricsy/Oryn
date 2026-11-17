/**
 * mock 后端的 M18（SSH 主机与网关）部分：主机、钥匙、测试与快照、重置指纹、网关审计、线程可用的主机、SFTP 文件浏览，
 * 校验与错误码同 orynd（`internal/sshgw`）。`MockServer.handleFetch` 调 `handle`，不归这里的请求返回 null；写操作后 `deps.changed()`。
 * 预置：钥匙 2 把；全局主机 `bastion`、`prod-web`（经 `bastion`，已测试成功带快照）、`down`（测试连不上）；
 * 第一个 mock 项目下 1 台 `staging-db`（密码登录）。
 * `fs/*` 改写成 `orbs/ssh-<主机 id>/fs/*` 交给 `orbfs` 的内存文件树（主机 id 不以 `e4` / `e8` 结尾，避开它的特例）。
 * runner 线程的 omp 命令 `get_ssh_hosts` / `ssh_manage` / `ssh_test` 由 `runnerCmd` 处理（内存里一份小列表）。
 */
import type { Orb, Schemas, Thread } from "../api/client";
import type {
	RpcSshHostInfo,
	RpcSshHostInput,
	RpcSshHostsResult,
	RpcSshTestResult,
} from "../omp/types";
import { projects as fixtureProjects } from "./fixtures";
import { handleOrbFetch } from "./orbfs";

type Host = Schemas["SshHost"];
type Key = Schemas["SshKey"];
type Audit = Schemas["SshAuditEntry"];
type TestResult = Schemas["SshTestResult"];

export interface SshDeps {
	/** SSH 主机、钥匙或测试结果变了：推送 WS `ssh.changed` */
	changed(): void;
	threads: ReadonlyMap<string, Thread>;
	orbs: ReadonlyMap<string, Orb>;
}

const MIN = 60_000;
const HOUR = 60 * MIN;
const NOW = Date.now();
const MAX_JUMP_CHAIN = 5;
const HOST_NAME_RE = /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$/;
const KEY_NAME_RE = /^[A-Za-z0-9_][A-Za-z0-9_. -]{0,63}$/;
const ADDRESS_RE = /^[A-Za-z0-9_.:-]+$/;

const MOCK_PROJECT = fixtureProjects[0]?.id ?? "";

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

function fakeKey(seed: string): string {
	let s = "AAAAC3NzaC1lZDI1NTE5AAAAI";
	for (let i = 0; s.length < 68; i++)
		s += "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"[
			(seed.charCodeAt(i % seed.length) * (i + 7)) % 62
		];
	return s;
}

function fingerprintOf(seed: string): string {
	return `SHA256:${fakeKey(`${seed}-fp`).slice(20, 63)}`;
}

const SNAPSHOT: Schemas["SshSnapshot"] = {
	load1: 0.42,
	cpuPct: 12.5,
	memUsedKb: 2_874_112,
	memTotalKb: 8_046_404,
	disks: [
		{ mount: "/", usedKb: 18_874_368, totalKb: 41_943_040 },
		{ mount: "/data", usedKb: 412_090_368, totalKb: 488_376_320 },
	],
};

function initialKeys(): Key[] {
	return [
		{
			id: "sshk_mock_deploy",
			name: "deploy",
			type: "ssh-ed25519",
			publicKey: `ssh-ed25519 ${fakeKey("deploy")} deploy`,
			fingerprint: fingerprintOf("deploy"),
			hostCount: 0,
			createdAt: NOW - 30 * 24 * HOUR,
		},
		{
			id: "sshk_mock_legacy",
			name: "legacy rsa",
			type: "ssh-rsa",
			publicKey: `ssh-rsa ${fakeKey("legacy")} legacy rsa`,
			fingerprint: fingerprintOf("legacy"),
			hostCount: 0,
			createdAt: NOW - 90 * 24 * HOUR,
		},
	];
}

function initialHosts(): Host[] {
	const base = {
		port: 22,
		hasPassword: false,
		description: "",
		tags: [] as string[],
		enabled: true,
		allowAutomation: false,
		allowForward: false,
		createdAt: NOW - 7 * 24 * HOUR,
		updatedAt: NOW - 2 * HOUR,
	};
	return [
		{
			...base,
			id: "ssh_mock_bastion",
			name: "bastion",
			address: "203.0.113.10",
			username: "jump",
			authType: "key",
			keyId: "sshk_mock_deploy",
			description: "公网跳板",
			tags: ["infra"],
			allowAutomation: true,
			hostKey: {
				type: "ssh-ed25519",
				fingerprint: fingerprintOf("bastion"),
				pinnedAt: NOW - 7 * 24 * HOUR,
			},
			lastTest: { at: NOW - 3 * HOUR, ok: true, os: "linux" },
		},
		{
			...base,
			id: "ssh_mock_prodweb",
			name: "prod-web",
			address: "10.0.1.21",
			username: "deploy",
			authType: "key",
			keyId: "sshk_mock_deploy",
			jumpHostId: "ssh_mock_bastion",
			description: "生产 Web 节点",
			tags: ["prod", "web"],
			allowForward: true,
			hostKey: {
				type: "ssh-ed25519",
				fingerprint: fingerprintOf("prod-web"),
				pinnedAt: NOW - 6 * 24 * HOUR,
			},
			lastTest: { at: NOW - 40 * MIN, ok: true, os: "linux" },
		},
		{
			...base,
			id: "ssh_mock_down",
			name: "down",
			address: "10.0.9.99",
			username: "root",
			authType: "key",
			keyId: "sshk_mock_legacy",
			description: "已下线的旧机器",
			lastTest: {
				at: NOW - 5 * HOUR,
				ok: false,
				error: "连不上 10.0.9.99:22：dial tcp 10.0.9.99:22: i/o timeout",
			},
		},
		{
			...base,
			id: "ssh_mock_stagingdb",
			projectId: MOCK_PROJECT,
			name: "staging-db",
			address: "staging-db.internal",
			port: 2222,
			username: "postgres",
			authType: "password",
			hasPassword: true,
			description: "预发数据库",
			tags: ["staging"],
		},
	];
}

function initialAudit(): Audit[] {
	const rows: Omit<Audit, "id">[] = [
		{
			at: NOW - 3 * HOUR,
			source: "owner",
			hostId: "ssh_mock_bastion",
			hostName: "bastion",
			kind: "test",
			detail: "",
			durationMs: 412,
			ok: true,
		},
		{
			at: NOW - 50 * MIN,
			source: "orb",
			hostId: "ssh_mock_prodweb",
			hostName: "prod-web",
			kind: "connect",
			detail: "",
			ok: true,
		},
		{
			at: NOW - 49 * MIN,
			source: "orb",
			hostId: "ssh_mock_prodweb",
			hostName: "prod-web",
			kind: "exec",
			detail: "systemctl status nginx --no-pager",
			exitCode: 0,
			durationMs: 182,
			ok: true,
		},
		{
			at: NOW - 48 * MIN,
			source: "orb",
			hostId: "ssh_mock_prodweb",
			hostName: "prod-web",
			kind: "exec",
			detail: "tail -n 200 /var/log/nginx/error.log | grep -i upstream",
			exitCode: 1,
			durationMs: 95,
			ok: false,
		},
		{
			at: NOW - 30 * MIN,
			source: "orb",
			hostId: "ssh_mock_down",
			hostName: "down",
			kind: "deny",
			detail: '主机 "down" 不允许全自动线程使用（可在 设置 → SSH 主机 里放开）',
			ok: false,
		},
		{
			at: NOW - 10 * MIN,
			source: "tool",
			hostId: "ssh_mock_bastion",
			hostName: "bastion",
			kind: "exec",
			detail: "uptime",
			exitCode: 0,
			durationMs: 640,
			ok: true,
		},
	];
	return rows.map((r, i) => ({ ...r, id: i + 1 }));
}

/** runner 那台机器自己的 `ssh.json`（mock 里共用一份）。 */
function initialRunnerHosts(): RpcSshHostInfo[] {
	return [
		{
			name: "nas",
			host: "192.168.1.20",
			username: "admin",
			port: 22,
			description: "家里的 NAS",
			scope: "user",
			editable: true,
			source: "~/.omp/agent/ssh.json",
			os: "linux",
		},
		{
			name: "github.com",
			host: "github.com",
			username: "git",
			scope: "native",
			editable: false,
			source: "~/.ssh/config",
		},
	];
}

/** 处理器抛出的校验错误：按状态码与文案回给客户端。 */
class HttpError extends Error {
	readonly status: number;
	readonly code: string;
	constructor(status: number, code: string, message: string) {
		super(message);
		this.status = status;
		this.code = code;
	}
}

const invalid = (m: string) => new HttpError(400, "invalid_request", m);
const conflict = (m: string) => new HttpError(409, "conflict", m);

export class MockSsh {
	private hosts: Host[] = initialHosts();
	private keys: Key[] = initialKeys();
	private audit: Audit[] = initialAudit();
	private runnerHosts: RpcSshHostInfo[] = initialRunnerHosts();
	private seq = 1;
	private auditSeq = 100;

	private readonly deps: SshDeps;

	constructor(deps: SshDeps) {
		this.deps = deps;
	}

	private keyView(k: Key): Key {
		return {
			...k,
			hostCount: this.hosts.filter((h) => h.keyId === k.id).length,
		};
	}

	private record(row: Omit<Audit, "id" | "at">) {
		this.audit.push({ ...row, id: this.auditSeq++, at: Date.now() });
	}

	private host(id: string): Host {
		const h = this.hosts.find((x) => x.id === id);
		if (!h) throw new HttpError(404, "not_found", "主机不存在");
		return h;
	}

	/** 校验合并后的主机（创建与更新共用）；`password` 是这次请求带的密码。 */
	private validate(h: Host, password: string | undefined, creating: boolean) {
		if (!HOST_NAME_RE.test(h.name))
			throw invalid(
				"主机名只能含字母、数字与 _ . -，不能以 . 或 - 开头，最长 64",
			);
		if (
			!h.address ||
			h.address.length > 255 ||
			!ADDRESS_RE.test(h.address) ||
			h.address.startsWith("-")
		)
			throw invalid("地址不能为空，且只能是主机名或 IP");
		if (!Number.isInteger(h.port) || h.port < 1 || h.port > 65535)
			throw invalid("端口必须在 1–65535");
		if (
			!h.username ||
			h.username.length > 64 ||
			/[\s\p{Cc}]/u.test(h.username) ||
			h.username.startsWith("-")
		)
			throw invalid("登录用户不能为空，且不能含空白");
		if (h.authType === "key") {
			if (!h.keyId) throw invalid("请选择一把钥匙");
			if (!this.keys.some((k) => k.id === h.keyId)) throw invalid("钥匙不存在");
		} else if (creating && !password) throw invalid("请填写密码");
		if (h.tags.length > 16 || h.tags.some((t) => [...t].length > 32))
			throw invalid("标签最多 16 个，每个最长 32 字符");
		if ([...h.description].length > 500) throw invalid("描述最长 500 字符");
		if (
			h.projectId &&
			!fixtureProjects.some((p) => p.id === h.projectId) &&
			h.projectId !== MOCK_PROJECT
		)
			throw new HttpError(404, "not_found", "项目不存在");
		if (h.jumpHostId) {
			const jump = this.hosts.find((x) => x.id === h.jumpHostId);
			if (!jump) throw invalid("跳板不存在");
			if (!jump.enabled) throw invalid("跳板已停用");
			if (jump.projectId && jump.projectId !== h.projectId)
				throw invalid("跳板须是全局主机或同项目的主机");
			let cur: Host | undefined = jump;
			for (let n = 1; cur; n++) {
				if (cur.id === h.id) throw invalid("跳板链存在循环");
				if (n > MAX_JUMP_CHAIN) throw invalid("跳板链超过 5 级");
				const next: string | undefined = cur.jumpHostId;
				cur = next ? this.hosts.find((x) => x.id === next) : undefined;
			}
		}
		if (
			this.hosts.some(
				(x) =>
					x.id !== h.id && x.name === h.name && x.projectId === h.projectId,
			)
		)
			throw conflict("同名主机已存在");
	}

	private normTags(tags: unknown): string[] {
		if (!Array.isArray(tags)) return [];
		const seen = new Set<string>();
		const out: string[] = [];
		for (const raw of tags) {
			const t = String(raw).trim();
			if (!t || seen.has(t.toLowerCase())) continue;
			seen.add(t.toLowerCase());
			out.push(t);
		}
		return out;
	}

	private createHost(b: Schemas["SshHostCreate"]): Host {
		const now = Date.now();
		const h: Host = {
			id: `ssh_mock_n${this.seq++}`,
			...(b.projectId ? { projectId: b.projectId } : {}),
			name: String(b.name ?? "").trim(),
			address: String(b.address ?? "").trim(),
			port: b.port ?? 22,
			username: String(b.username ?? "").trim(),
			authType: b.authType,
			...(b.authType === "key" && b.keyId ? { keyId: b.keyId } : {}),
			hasPassword: b.authType === "password" && !!b.password,
			...(b.jumpHostId ? { jumpHostId: b.jumpHostId } : {}),
			description: String(b.description ?? "").trim(),
			tags: this.normTags(b.tags),
			enabled: b.enabled ?? true,
			allowAutomation: b.allowAutomation ?? false,
			allowForward: b.allowForward ?? false,
			...(b.compat !== undefined ? { compat: b.compat } : {}),
			createdAt: now,
			updatedAt: now,
		};
		this.validate(h, b.password, true);
		this.hosts.push(h);
		return h;
	}

	private updateHost(id: string, p: Schemas["SshHostUpdate"]): Host {
		const old = this.host(id);
		const next: Host = { ...old, updatedAt: Date.now() };
		if (p.name !== undefined) next.name = p.name.trim();
		if (p.address !== undefined) next.address = p.address.trim();
		if (p.port !== undefined) next.port = p.port;
		if (p.username !== undefined) next.username = p.username.trim();
		if (p.authType !== undefined) next.authType = p.authType;
		if (p.keyId !== undefined) next.keyId = p.keyId;
		if (p.description !== undefined) next.description = p.description.trim();
		if (p.tags !== undefined) next.tags = this.normTags(p.tags);
		if (p.enabled !== undefined) next.enabled = p.enabled;
		if (p.allowAutomation !== undefined)
			next.allowAutomation = p.allowAutomation;
		if (p.allowForward !== undefined) next.allowForward = p.allowForward;
		if (p.jumpHostId === null) delete next.jumpHostId;
		else if (p.jumpHostId !== undefined) next.jumpHostId = p.jumpHostId;
		if (p.compat === null) delete next.compat;
		else if (p.compat !== undefined) next.compat = p.compat;
		if (next.authType === "key") next.hasPassword = false;
		else {
			delete next.keyId;
			if (p.password) next.hasPassword = true;
			else if (!old.hasPassword || old.authType !== "password")
				throw invalid("请填写密码");
		}
		this.validate(next, p.password, false);
		if (old.enabled && !next.enabled)
			if (this.hosts.some((h) => h.jumpHostId === id && h.enabled))
				throw conflict("还有启用中的主机拿它当跳板");
		if (!old.enabled && next.enabled && next.jumpHostId) {
			const jump = this.hosts.find((h) => h.id === next.jumpHostId);
			if (!jump?.enabled) throw conflict("它的跳板已停用");
		}
		// 换了机器：旧指纹不再适用
		if (next.address !== old.address || next.port !== old.port)
			delete next.hostKey;
		this.hosts = this.hosts.map((h) => (h.id === id ? next : h));
		return next;
	}

	private testHost(id: string): TestResult {
		const h = this.host(id);
		const now = Date.now();
		const failing = h.name === "down";
		const res: TestResult = failing
			? {
					ok: false,
					checkedAt: now,
					latencyMs: 10_004,
					error: `连不上 ${h.address}:${h.port}：dial tcp ${h.address}:${h.port}: i/o timeout`,
				}
			: {
					ok: true,
					checkedAt: now,
					latencyMs: h.jumpHostId ? 238 : 87,
					os: "linux",
					hostKeyFingerprint: h.hostKey?.fingerprint ?? fingerprintOf(h.name),
					snapshot: SNAPSHOT,
				};
		const next: Host = {
			...h,
			lastTest: {
				at: now,
				ok: res.ok,
				...(res.error ? { error: res.error } : {}),
				...(res.os ? { os: res.os } : {}),
			},
			...(res.ok && !h.hostKey
				? {
						hostKey: {
							type: "ssh-ed25519",
							fingerprint: res.hostKeyFingerprint ?? "",
							pinnedAt: now,
						},
					}
				: {}),
		};
		this.hosts = this.hosts.map((x) => (x.id === id ? next : x));
		this.record({
			source: "owner",
			hostId: h.id,
			hostName: h.name,
			kind: "test",
			detail: res.error ?? "",
			durationMs: res.latencyMs,
			ok: res.ok,
		});
		return res;
	}

	private createKey(b: Schemas["SshKeyCreate"]): Key {
		const name = String(b.name ?? "").trim();
		if (!KEY_NAME_RE.test(name))
			throw invalid("钥匙名只能含字母、数字、空格与 _ . -，最长 64");
		if (this.keys.some((k) => k.name === name)) throw conflict("钥匙名已存在");
		let type = "ssh-ed25519";
		if (b.mode === "import") {
			const pem = String(b.privateKey ?? "");
			if (!pem.includes("PRIVATE KEY")) throw invalid("无法解析这把私钥");
			if (pem.includes("ENCRYPTED") && !b.passphrase)
				throw invalid("这把私钥有口令，请一并填写");
			if (pem.includes("RSA")) type = "ssh-rsa";
		}
		const key: Key = {
			id: `sshk_mock_n${this.seq++}`,
			name,
			type,
			publicKey: `${type} ${fakeKey(name)} ${name}`,
			fingerprint: fingerprintOf(name),
			hostCount: 0,
			createdAt: Date.now(),
		};
		this.keys.push(key);
		return key;
	}

	/** 线程所在 orb 能用的主机：全局与本项目的启用主机，同名时项目盖过全局；全自动 orb 上未放开的进 blocked。 */
	private threadHosts(threadId: string): Schemas["ThreadSshHosts"] | null {
		const t = this.deps.threads.get(threadId);
		if (!t) return null;
		const orb = t.orbId ? this.deps.orbs.get(t.orbId) : undefined;
		const automation = orb?.automation === true;
		if (!orb || orb.kind === "runner" || (t.kind ?? "work") !== "work")
			return { managed: false, automation, hosts: [] };
		const byName = new Map<string, Host>();
		for (const h of this.hosts)
			if (h.enabled && !h.projectId && !byName.has(h.name))
				byName.set(h.name, h);
		for (const h of this.hosts)
			if (h.enabled && h.projectId && h.projectId === orb.projectId)
				byName.set(h.name, h);
		const hosts = [...byName.values()]
			.sort((a, b) => a.name.localeCompare(b.name))
			.map((host) =>
				automation && !host.allowAutomation
					? { host, usable: false, blocked: "automation" as const }
					: { host, usable: true },
			);
		return { managed: true, automation, hosts };
	}

	private async fs(
		req: Request,
		id: string,
		op: string,
		url: URL,
	): Promise<Response> {
		this.host(id);
		const target = new URL(
			`/api/v1/orbs/ssh-${encodeURIComponent(id)}/fs/${op}${url.search}`,
			url.origin,
		);
		const method = req.method.toUpperCase();
		const res = await handleOrbFetch(
			new Request(target, {
				method,
				headers: req.headers,
				body:
					method === "GET" || method === "HEAD"
						? undefined
						: await req.arrayBuffer(),
			}),
		);
		if (!res) return error(404, "not_found", "not found");
		if (op !== "roots" || !res.ok) return res;
		const r = (await res.json()) as Schemas["FsRoots"];
		return json({ home: r.home });
	}

	async handle(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		const threadHosts = /^threads\/([^/]+)\/ssh-hosts$/.exec(p);
		if (!p.startsWith("ssh/") && !threadHosts) return null;
		const body = async <T>(): Promise<T> => {
			try {
				return (await req.json()) as T;
			} catch {
				return {} as T;
			}
		};
		try {
			if (threadHosts && m === "GET") {
				const r = this.threadHosts(decodeURIComponent(threadHosts[1] ?? ""));
				return r ? json(r) : error(404, "not_found", "线程不存在");
			}
			if (p === "ssh/hosts" && m === "GET")
				return json({
					hosts: [...this.hosts].sort(
						(a, b) =>
							Number(!!a.projectId) - Number(!!b.projectId) ||
							(a.projectId ?? "").localeCompare(b.projectId ?? "") ||
							a.name.localeCompare(b.name),
					),
				});
			if (p === "ssh/hosts" && m === "POST") {
				const h = this.createHost(await body<Schemas["SshHostCreate"]>());
				this.deps.changed();
				return json(h, 201);
			}
			const fsm = /^ssh\/hosts\/([^/]+)\/fs\/([a-z]+)$/.exec(p);
			if (fsm) return await this.fs(req, fsm[1] ?? "", fsm[2] ?? "", url);
			const hm = /^ssh\/hosts\/([^/]+)(?:\/(test|reset-host-key))?$/.exec(p);
			if (hm) {
				const id = hm[1] ?? "";
				const sub = hm[2];
				if (!sub && m === "GET") return json(this.host(id));
				if (!sub && m === "PATCH") {
					const h = this.updateHost(id, await body<Schemas["SshHostUpdate"]>());
					this.deps.changed();
					return json(h);
				}
				if (!sub && m === "DELETE") {
					this.host(id);
					if (this.hosts.some((h) => h.jumpHostId === id))
						throw conflict("主机被其他主机用作跳板");
					this.hosts = this.hosts.filter((h) => h.id !== id);
					this.deps.changed();
					return new Response(null, { status: 204 });
				}
				if (sub === "test" && m === "POST") {
					const r = this.testHost(id);
					this.deps.changed();
					return json(r);
				}
				if (sub === "reset-host-key" && m === "POST") {
					const { hostKey: _, ...rest } = this.host(id);
					const next: Host = { ...rest, updatedAt: Date.now() };
					this.hosts = this.hosts.map((h) => (h.id === id ? next : h));
					this.deps.changed();
					return json(next);
				}
			}
			if (p === "ssh/keys" && m === "GET")
				return json({
					keys: [...this.keys]
						.sort((a, b) => a.name.localeCompare(b.name))
						.map((k) => this.keyView(k)),
				});
			if (p === "ssh/keys" && m === "POST") {
				const k = this.createKey(await body<Schemas["SshKeyCreate"]>());
				this.deps.changed();
				return json(this.keyView(k), 201);
			}
			const km = /^ssh\/keys\/([^/]+)$/.exec(p);
			if (km) {
				const id = km[1] ?? "";
				const key = this.keys.find((k) => k.id === id);
				if (!key) throw new HttpError(404, "not_found", "钥匙不存在");
				if (m === "PATCH") {
					const name = String(
						(await body<Schemas["SshKeyRename"]>()).name ?? "",
					).trim();
					if (!KEY_NAME_RE.test(name))
						throw invalid("钥匙名只能含字母、数字、空格与 _ . -，最长 64");
					if (this.keys.some((k) => k.id !== id && k.name === name))
						throw conflict("钥匙名已存在");
					const next: Key = {
						...key,
						name,
						publicKey: `${key.publicKey.split(" ").slice(0, 2).join(" ")} ${name}`,
					};
					this.keys = this.keys.map((k) => (k.id === id ? next : k));
					this.deps.changed();
					return json(this.keyView(next));
				}
				if (m === "DELETE") {
					const n = this.keyView(key).hostCount;
					if (n > 0) throw conflict(`钥匙正被 ${n} 台主机使用`);
					this.keys = this.keys.filter((k) => k.id !== id);
					this.deps.changed();
					return new Response(null, { status: 204 });
				}
			}
			if (p === "ssh/audit" && m === "GET") {
				const q = url.searchParams;
				const before = Number(q.get("before") ?? Number.MAX_SAFE_INTEGER);
				const limit = Number(q.get("limit") ?? 50);
				if (!(before >= 1) || !(limit >= 1 && limit <= 200))
					throw invalid("before 与 limit 必须是正整数（limit ≤ 200）");
				const host = q.get("host");
				const thread = q.get("thread");
				const rows = this.audit
					.filter(
						(a) =>
							a.id < before &&
							(!host || a.hostId === host) &&
							(!thread || a.threadId === thread),
					)
					.sort((a, b) => b.id - a.id);
				const page = rows.slice(0, limit);
				const last = page[page.length - 1];
				return json({
					entries: page,
					...(rows.length > limit && last ? { nextBefore: last.id } : {}),
				});
			}
			return null;
		} catch (e) {
			if (e instanceof HttpError) return error(e.status, e.code, e.message);
			throw e;
		}
	}

	/** runner 线程的 omp SSH 命令；返回应答 data，失败抛 Error（文案即 omp 的错误）。 */
	runnerCmd(
		type: "get_ssh_hosts" | "ssh_manage" | "ssh_test",
		frame: Record<string, unknown>,
	): RpcSshHostsResult | RpcSshHostInfo | { deleted: true } | RpcSshTestResult {
		if (type === "get_ssh_hosts")
			return {
				openSshAvailable: true,
				hosts: this.runnerHosts,
				warnings: [],
			};
		const name = String(frame.name ?? "");
		if (type === "ssh_test") {
			const host = frame.host as RpcSshHostInput & { name: string };
			const ok = host.host !== "unreachable.example";
			return {
				name: host.name,
				ok,
				checkedAt: new Date().toISOString(),
				...(ok
					? { os: "linux", shell: "bash" }
					: {
							error:
								"ssh: connect to host unreachable.example port 22: Connection timed out",
						}),
			};
		}
		const scope = frame.scope === "project" ? "project" : "user";
		if (frame.action === "delete") {
			this.runnerHosts = this.runnerHosts.filter(
				(h) => !(h.name === name && h.scope === scope),
			);
			return { deleted: true };
		}
		const prevName = String(frame.previousName ?? name);
		const prevScope = frame.previousScope ?? scope;
		const input = (frame.host ?? {}) as RpcSshHostInput;
		const info: RpcSshHostInfo = {
			...input,
			name,
			scope,
			editable: true,
			source:
				scope === "user" ? "~/.omp/agent/ssh.json" : "<cwd>/.omp/ssh.json",
		};
		const rest = this.runnerHosts.filter((h) =>
			frame.action === "update"
				? !(h.name === prevName && h.scope === prevScope)
				: true,
		);
		if (rest.some((h) => h.name === name && h.scope === scope))
			throw new Error(`SSH host "${name}" already exists`);
		this.runnerHosts = [...rest, info];
		return info;
	}
}
