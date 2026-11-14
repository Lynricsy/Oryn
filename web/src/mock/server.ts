/**
 * 浏览器内 mock 服务端：实现足够的 REST 与 WS（计划 C3）让界面无需后端即可体验。
 * 发送提示词时按 fakeomp 的帧序列回放：agent_start → 用户消息 → 流式 assistant（思考 / 文本 / 工具调用）→
 * bash 工具生命周期 → 最终回答 → agent_end → prompt_result → session_settled，期间按序复制条目。
 * 提示词含 `ask` 时发一个 ask 对话框并等待作答；含 `plan` 时发计划提案并等待审批。
 * 环境（M2.5）：orb 带 `lastSetup` / `lastResume`，提供 setup-runs 与日志接口；failed orb 的线程 `wake` 后恢复；
 * 提示词点名 `oryn-setup` 技能时，回合结束后补一条手动重跑成功的记录（模拟 agent 修好环境）。
 * 设置：Git 身份（GET / PUT / DELETE）与项目的新建 / 修改（含 git 身份覆盖，空串清除）。
 * 受管服务与 portal（M4）：主线程的 orb 带两个服务（`web` 有 portal、`worker` 失败）；线程带 `data:` 形态的
 * `portalUrlTemplate`；服务启停 / 重启 / 日志与 portal 公开 / 终止改内存状态并广播 `orb`。
 * 终端（M3，同 fakeomp 2.4）：`terminal_*` 命令的替身 shell（回显；`flood <n>` 洪泛、`count <n>` 逐行输出、`echo …`、`exit`），
 * `term.watch` 开关、`term.resized` 广播、按单连接 1s 窗口的输出量模拟背压（`term.lagged`）；提示词含 `pty` 时
 * bash 工具以 `pty:true` 在 `kind:"command"` 终端里等用户输入 `y`。orb 的 fs / git REST 由 `./orbfs` 应答。
 * `localStorage["oryn.debug"]==="1"` 时挂 `window.__orynMock`（验收脚本用：断线、重启 agent 等）。
 * 公开分享（M5）：主线程带一条快照分享与一条实时分享（固定 slug 见 `MOCK_SHARE_SLUGS`）与一个已发布的附件；
 * 分享 / 发布的增删查、匿名 REST（`public/shares/*`）与匿名 WS（按 URL 识别，只转发公开白名单内的消息），
 * 创建分享前按服务端同一套正则扫描疑似密钥（409 `secrets_found`）。通知相关接口交给 `./notify`。
 * 桌面与浏览器 Profile（M8）交给 `./desktop`：新建线程按 `browserProfileId` / `profileMode` 给新 orb 定 Profile。
 */
import type { Entry, Orb, Project, Schemas, Thread } from "../api/client";
import type { RpcTerminalInfo, SessionState } from "../omp/types";
import { activeBranch, isMainFile } from "../state/transcript";
import { MockAutomation } from "./automation";
import { MockDesktop } from "./desktop";
import {
	assistant,
	buildM9Transcripts,
	buildMainTranscript,
	buildToolZooTranscript,
	projects as fixtureProjects,
	threads as fixtureThreads,
	MAIN_THREAD,
	MOCK_PORTAL_TEMPLATE,
	mainState,
	makeOrb,
	mockSpeedCaps,
	NOW,
	orbEnv,
	providers,
	SESSION_FILE,
	serviceLogs,
	setupLogs,
	TOOL_ZOO_THREAD,
	toolZooLiveFrames,
	toolZooReplayFrames,
} from "./fixtures";
import { handleNotifyFetch } from "./notify";
import { handleOrbFetch } from "./orbfs";

type Json = Record<string, unknown>;

export interface MockSocket {
	/** FakeWebSocket 的连接地址：匿名分享 WS 据此分流。 */
	readonly url?: string;
	deliver(msg: Json): void;
	close?(): void;
}

/** 主线程上的两条固定分享（截图脚本直接打开 `/s/<slug>`）。 */
export const MOCK_SHARE_SLUGS = {
	snapshot: "5a0c1e7f3b9d42a8c6e1f0b2d4a69c3e",
	live: "c3e9a14b7d2f4e6a8b0c5d1e9f2a7b64",
} as const;

/** 匿名 WS 转发的帧类型（同 orynd `internal/realtime/public.go` 的白名单）。 */
const PUBLIC_FRAMES: Record<string, true> = {
	message_start: true,
	message_update: true,
	message_end: true,
	tool_execution_start: true,
	tool_execution_update: true,
	tool_stream_update: true,
	tool_execution_end: true,
	agent_start: true,
	agent_end: true,
	subagent_lifecycle: true,
	subagent_progress: true,
};

/** 分享前的密钥扫描规则（同 orynd `internal/share/scan.go`；mock 没有密钥库明文，不做 `vault_secret`）。 */
const SHARE_RULES: { rule: Schemas["ShareFindingRule"]; re: RegExp }[] = [
	{ rule: "private_key", re: /-----BEGIN [A-Z ]*PRIVATE KEY-----/g },
	{ rule: "api_key_sk", re: /sk-[A-Za-z0-9_-]{20,}/g },
	{
		rule: "github_token",
		re: /gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{22,}/g,
	},
	{ rule: "aws_access_key", re: /AKIA[0-9A-Z]{16}/g },
	{ rule: "google_api_key", re: /AIza[0-9A-Za-z_-]{35}/g },
	{ rule: "slack_token", re: /xox[baprs]-[A-Za-z0-9-]{10,}/g },
	{
		rule: "jwt",
		re: /eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}/g,
	},
	{ rule: "oryn_token", re: /(?:opv|oot|odt|ost|ows|owt)_[A-Za-z0-9_-]{43}/g },
	{
		rule: "bearer_header",
		re: /authorization["':\s]+bearer\s+[A-Za-z0-9._~+/-]{20,}/gi,
	},
	{
		rule: "url_credentials",
		re: /[a-z][a-z0-9+.-]{1,15}:\/\/[^/\s:@"]+:[^/\s:@"]{6,}@/g,
	},
];

/** 脱敏预览：前 4 字符 + `…` + 后 2 字符；短于 12 字符只留前 2。 */
function redact(s: string): string {
	return s.length < 12
		? `${s.slice(0, 2)}…`
		: `${s.slice(0, 4)}…${s.slice(-2)}`;
}

/** 16 字节随机 → 32 位小写十六进制（同 orynd 的 slug）。 */
function newSlug(): string {
	const b = new Uint8Array(16);
	crypto.getRandomValues(b);
	return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

/** 匿名接口的响应：一律 noindex、不缓存。 */
function publicJson(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: {
			"Content-Type": "application/json",
			"X-Robots-Tag": "noindex, nofollow",
			"Cache-Control": "no-store",
		},
	});
}

type MockShare = Schemas["ThreadShare"] & { leafEntryId?: string };
type MockPublished = Schemas["PublishedFile"] & { slug: string };

/** omp 快照返回的回滚上限（字符）。 */
const SNAPSHOT_CHARS = 262_144;
const FLOOD_FRAME = 32_768;
const PROMPT = "fake$ ";
/** CSI 序列与其它 ESC 双字节序列。 */
// biome-ignore lint/suspicious/noControlCharactersInRegex: 匹配终端转义序列必须用 ESC
const ESCAPE_SEQ = /\u001b\[[0-9;]*[A-Za-z~]|\u001b./g;

/** 角色模型的候选（get_model_roles 的 candidates）与 set_model 的查找表。 */
const MOCK_ROLE_MODELS = [
	{ provider: "anthropic", id: "claude-sonnet-5", name: "Claude Sonnet 5" },
	{ provider: "anthropic", id: "claude-opus-5-5", name: "Claude Opus 5.5" },
	{ provider: "anthropic", id: "claude-fable-5-2", name: "Claude Fable 5.2" },
	{ provider: "openai", id: "gpt-6.1-sol", name: "GPT-6.1 Sol" },
	{ provider: "openai-codex", id: "gpt-6-astra", name: "GPT-6 Astra" },
	{ provider: "google", id: "gemini-4-carbon", name: "Gemini 4 Carbon" },
	{ provider: "deepseek", id: "deepseek-flash", name: "DeepSeek Flash" },
];

interface MockTerm {
	info: RpcTerminalInfo;
	data: string;
	seq: number;
	line: string;
	/** 正在执行的命令被 ^C 中断。 */
	aborted: boolean;
	/** `kind:"command"` 终端的输入回调（pty 命令等待作答）。 */
	onInput?: (data: string) => void;
}

function json(body: unknown, status = 200): Response {
	return new Response(body === undefined ? null : JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

/** 项目 git 身份覆盖须两项同时给出（与服务端一致）；两项都为空串表示清除。 */
function badGitPair(name?: string, email?: string): Response | null {
	if (name === undefined && email === undefined) return null;
	const n = name?.trim() ?? "";
	const e = email?.trim() ?? "";
	if ((n === "" && e === "") || (n !== "" && e.includes("@"))) return null;
	return json(
		{
			code: "invalid_request",
			message: "gitAuthorName 与 gitAuthorEmail 须同时给出",
		},
		400,
	);
}

function sleep(ms: number) {
	return new Promise((r) => setTimeout(r, ms));
}

/** 同 orynd（internal/threads/activity.go）：工具调用的意图，缺省时取 path / pattern / command，再缺省取工具名。 */
function toolActivity(name: string, args: unknown, intent?: unknown): string {
	const a = (args && typeof args === "object" ? args : {}) as Json;
	const pick = (...keys: string[]) =>
		[intent, ...keys.map((k) => a[k])]
			.map((v) => (typeof v === "string" ? v.replace(/\s+/g, " ").trim() : ""))
			.find(Boolean);
	const v = pick("i", "intent", "path", "pattern", "command") || name;
	return v.length > 120 ? `${v.slice(0, 119)}…` : v;
}

/** 帧里的线程活动：tool_execution_start，或 message_update 的 toolcall_end（带完整 toolCall）。 */
function frameActivity(frame: Json): string | undefined {
	if (frame.type === "tool_execution_start")
		return toolActivity(String(frame.toolName), frame.args, frame.intent);
	const ev = frame.assistantMessageEvent as Json | undefined;
	if (frame.type !== "message_update" || ev?.type !== "toolcall_end") return;
	const call = ev.toolCall as Json | undefined;
	return call && toolActivity(String(call.name), call.arguments, call.intent);
}

/** 离开 working 的线程不带活动。 */
function settleActivity(t: Thread): Thread {
	if (t.state === "working" || t.activity === undefined) return t;
	const { activity: _, ...rest } = t;
	return rest;
}

export class MockServer {
	readonly threads = new Map<string, Thread>();
	readonly entries = new Map<string, Entry[]>();
	readonly states = new Map<string, SessionState>();
	readonly orbs = new Map<string, Orb>();
	readonly pendingUi = new Map<string, Json[]>();
	/** setup / resume 运行记录（按 orb，新的在前）与日志。 */
	readonly setupRuns = new Map<string, Schemas["OrbSetupRun"][]>();
	readonly setupLogs = new Map<string, string>(Object.entries(setupLogs));
	/** 受管服务日志（服务名 → 文本）。 */
	readonly serviceLogs = new Map<string, string>(Object.entries(serviceLogs));
	readonly planProposals = new Map<string, Json>();
	/** 快照 `agent.live` 里的在途帧（按线程）。 */
	readonly liveFrames = new Map<string, Json[]>();
	/** 首次订阅（afterSeq=0）时按位置插在条目之间投递的帧（按线程）。 */
	readonly replayFrames = new Map<
		string,
		{ afterSeq: number; frame: Json }[]
	>();
	readonly projects: Project[] = fixtureProjects.map((p) => ({ ...p }));
	/** 浏览器 Profile 与 orb 桌面（M8）。 */
	readonly desktop = new MockDesktop({
		orbs: this.orbs,
		setOrb: (orb) => this.setOrb(orb),
		projects: () => this.projects,
		threadTitle: (id) => this.threads.get(id)?.title,
	});
	/** Space、助手对话、定时、操作记录与协调宿主状态（M9）。 */
	readonly automation = new MockAutomation({
		threads: this.threads,
		addThread: (t) => this.addThread(t),
		upsertThread: (t) => this.upsertThread(t),
		removeThread: (id) => {
			this.threads.delete(id);
			this.broadcast({
				type: "index.patch",
				upsert: [],
				remove: [id],
				rev: ++this.rev,
			});
		},
		schedulesChanged: (scheduleId) =>
			this.broadcast({ type: "schedules.changed", scheduleId }),
	});
	gitIdentity: Schemas["GitIdentity"] | undefined = {
		name: "Oryn Owner",
		email: "owner@oryn.example",
	};
	/** agent 头像（内存态）；都缺省 = 默认品牌标。 */
	agentIdentity: Schemas["AvatarIdentity"] = {};
	/** 用户头像（内存态）；默认给一个 emoji，截图里能看到效果。 */
	userIdentity: Schemas["AvatarIdentity"] = { avatarIcon: "🦊" };
	readonly queues = new Map<
		string,
		{ steering: string[]; followUp: string[] }
	>();
	private readonly sockets = new Set<MockSocket>();
	private readonly subs = new Map<MockSocket, Set<string>>();
	private readonly waiters = new Map<string, (value: Json) => void>();
	private rev = 1;
	private idSeq = 100;
	private readonly terms = new Map<string, Map<string, MockTerm>>();
	private readonly termWatch = new Map<MockSocket, Set<string>>();
	private readonly termWindow = new Map<
		MockSocket,
		{ start: number; chars: number }
	>();
	private termSeq = 0;
	/** 消息附件（控制面副本）：id → 元数据与内容。 */
	readonly attachments = new Map<
		string,
		{ meta: Schemas["Attachment"]; data: Blob }
	>();
	/** 公开分享（未撤销的）：id → 分享；撤销即删除。 */
	readonly shares = new Map<string, MockShare>();
	/** 已发布的附件：id → 发布记录。 */
	readonly published = new Map<string, MockPublished>();
	/** 匿名分享 WS：连接 → 所属分享与上次推送的状态 / 标题。 */
	private readonly publicSockets = new Map<
		MockSocket,
		{ shareId: string; threadId: string; state: string; title: string }
	>();
	/** 单连接 1s 内可收的终端输出（字符）；超出即模拟服务端丢帧并发 `term.lagged`。 */
	termLag = 2 << 20;
	/** 帧间隔（毫秒）；测试里设为 0。 */
	delay = 45;
	files: Record<string, string> = {
		"config.yml": "theme: dark\ndefaultThinkingLevel: high\n",
		"models.yml": `providers:\n  anthropic:\n    apiKey: \${oryn:secret:omp/models/anthropic/apiKey}\n  openrouter:\n    baseUrl: https://openrouter.ai/api/v1\n    apiKey: \${oryn:secret:omp/models/openrouter/apiKey}\n`,
		"AGENTS.md": "# 约定\n\n- 中文注释\n- 先写测试\n",
		"skills/review/SKILL.md": "---\nname: review\n---\n审查改动。\n",
	};

	constructor() {
		for (const t of fixtureThreads) {
			this.threads.set(t.id, { ...t, portalUrlTemplate: MOCK_PORTAL_TEMPLATE });
			this.entries.set(t.id, []);
			if (t.orbId && !this.orbs.has(t.orbId)) {
				const ids = fixtureThreads
					.filter((x) => x.orbId === t.orbId)
					.map((x) => x.id);
				this.orbs.set(t.orbId, {
					...makeOrb(t.orbId, ids),
					...orbEnv[t.orbId],
				});
			}
		}
		for (const orb of this.orbs.values())
			this.setupRuns.set(
				orb.id,
				[orb.lastResume, orb.lastSetup]
					.filter((r): r is Schemas["OrbSetupRun"] => Boolean(r))
					.sort((a, b) => b.finishedAt - a.finishedAt),
			);
		this.entries.set(MAIN_THREAD, buildMainTranscript());
		const zoo = buildToolZooTranscript();
		this.entries.set(TOOL_ZOO_THREAD, zoo);
		for (const [id, list] of Object.entries(buildM9Transcripts()))
			this.entries.set(id, list);
		this.liveFrames.set(TOOL_ZOO_THREAD, toolZooLiveFrames());
		this.replayFrames.set(TOOL_ZOO_THREAD, toolZooReplayFrames(zoo));
		for (const t of fixtureThreads)
			this.states.set(t.id, {
				...mainState(),
				todoPhases: t.id === MAIN_THREAD ? mainState().todoPhases : [],
			});
		try {
			if (globalThis.localStorage?.getItem("oryn.debug") === "1")
				(globalThis as { __orynMock?: MockServer }).__orynMock = this;
		} catch {
			// 无 localStorage
		}
		this.pendingUi.set("T-0193a1b2-0000-7000-8000-00000000a004", [
			{
				type: "extension_ui_request",
				id: "ui-mock-1",
				method: "select",
				title: "允许执行 `docker exec orb-… pkill -HUP -x orbd`？",
				options: ["允许一次", "本会话总是允许", "拒绝"],
			},
		]);
		this.seedShares();
	}

	/** 主线程：一条快照分享（2 小时前）、一条实时分享（20 分钟前）与一个已发布的附件。 */
	private seedShares() {
		const main = (this.entries.get(MAIN_THREAD) ?? []).filter((e) =>
			isMainFile(e.file),
		);
		const last = main[main.length - 1];
		this.addShare(MAIN_THREAD, "snapshot", NOW - 2 * 3_600_000, {
			slug: MOCK_SHARE_SLUGS.snapshot,
			untilSeq: last?.seq ?? 0,
			leafEntryId: last?.entryId,
		});
		this.addShare(MAIN_THREAD, "live", NOW - 20 * 60_000, {
			slug: MOCK_SHARE_SLUGS.live,
		});
		const meta: Schemas["Attachment"] = {
			id: "att_01JCZ8MOCKRECONNECTTRACE00",
			threadId: MAIN_THREAD,
			name: "reconnect-trace.log",
			mime: "text/plain; charset=utf-8",
			size: 1834,
			sha256: "0".repeat(64),
			createdAt: NOW - 3 * 3_600_000,
		};
		this.attachments.set(meta.id, {
			meta,
			data: new Blob(["ws open\nws close 1006\nretry in 500ms\n"]),
		});
		this.publishAttachment(meta.id, NOW - 90 * 60_000);
	}

	private origin(): string {
		return globalThis.location?.origin ?? "http://localhost";
	}

	private addShare(
		threadId: string,
		mode: Schemas["ShareMode"],
		createdAt: number,
		opts: {
			slug?: string;
			untilSeq?: number;
			leafEntryId?: string;
			findings?: number;
		} = {},
	): MockShare {
		const slug = opts.slug ?? newSlug();
		const share: MockShare = {
			id: `shr_${(this.idSeq++).toString(36).toUpperCase().padStart(26, "0")}`,
			threadId,
			threadTitle: this.threads.get(threadId)?.title ?? "",
			mode,
			slug,
			url: `${this.origin()}/s/${slug}`,
			createdAt,
			findings: opts.findings ?? 0,
			...(mode === "snapshot"
				? { untilSeq: opts.untilSeq, leafEntryId: opts.leafEntryId }
				: {}),
		};
		this.shares.set(share.id, share);
		this.syncPublicShares(threadId);
		return share;
	}

	/** 线程摘要里的未撤销分享数（0 时省略），变化经 index.patch 推送。 */
	private syncPublicShares(threadId: string) {
		const t = this.threads.get(threadId);
		if (!t) return;
		const n = [...this.shares.values()].filter(
			(s) => s.threadId === threadId,
		).length;
		if ((t.publicShares ?? 0) === n) return;
		const { publicShares: _, ...rest } = t;
		this.upsertThread(n > 0 ? { ...rest, publicShares: n } : rest);
	}

	private publishAttachment(id: string, createdAt = Date.now()): MockPublished {
		const existing = [...this.published.values()].find(
			(f) => f.attachmentId === id,
		);
		if (existing) return existing;
		const a = this.attachments.get(id);
		if (!a) throw new Error(`mock: 附件 ${id} 不存在`);
		const slug = newSlug();
		const file: MockPublished = {
			id: `pub_${(this.idSeq++).toString(36).toUpperCase().padStart(26, "0")}`,
			attachmentId: id,
			threadId: a.meta.threadId,
			name: a.meta.name,
			mime: a.meta.mime,
			size: a.meta.size,
			url: `${this.origin()}/f/${slug}/${encodeURIComponent(a.meta.name)}`,
			createdAt,
			slug,
		};
		this.published.set(file.id, file);
		a.meta = { ...a.meta, publicUrl: file.url };
		return file;
	}

	private revokeShare(share: MockShare) {
		this.shares.delete(share.id);
		for (const [s, sub] of this.publicSockets) {
			if (sub.shareId !== share.id) continue;
			s.deliver({ type: "share.revoked" });
			this.publicSockets.delete(s);
			setTimeout(() => s.close?.(), 10);
		}
		this.syncPublicShares(share.threadId);
	}

	/** 公开范围：活动分支上、`until` 之内的主会话条目（快照取创建时的上界与叶子，实时取当前）。 */
	private shareScope(share: MockShare): {
		until: number;
		leaf?: string;
		entries: Entry[];
	} {
		const main = (this.entries.get(share.threadId) ?? []).filter((e) =>
			isMainFile(e.file),
		);
		const until =
			share.mode === "snapshot"
				? (share.untilSeq ?? 0)
				: (main[main.length - 1]?.seq ?? 0);
		const within = main.filter((e) => e.seq <= until);
		const leaf =
			share.mode === "snapshot"
				? share.leafEntryId
				: within[within.length - 1]?.entryId;
		const entries = activeBranch(within, leaf)
			.flat()
			.sort((a, b) => a.seq - b.seq);
		return { until, leaf, entries };
	}

	private scanShare(share: MockShare): Schemas["ShareScan"] {
		const { entries } = this.shareScope(share);
		const findings: Schemas["ShareFinding"][] = [];
		for (const e of entries) {
			const text = JSON.stringify(e.payload);
			const p = (e.payload ?? {}) as Json;
			const msg = (p.message ?? {}) as Json;
			for (const { rule, re } of SHARE_RULES) {
				const hits = text.match(re);
				if (!hits) continue;
				findings.push({
					rule,
					seq: e.seq,
					entryId: e.entryId,
					preview: redact(hits[0] ?? ""),
					count: hits.length,
					...(typeof msg.role === "string" ? { role: msg.role } : {}),
					...(typeof msg.toolName === "string" ? { tool: msg.toolName } : {}),
				});
			}
		}
		return {
			findings: findings.slice(0, 200),
			scanned: entries.length,
			truncated: findings.length > 200,
		};
	}

	private activeShareBySlug(slug: string): MockShare | undefined {
		return [...this.shares.values()].find((s) => s.slug === slug);
	}

	/** 分享、文件发布与匿名接口（计划 M5 1.1）。 */
	private async handleShareFetch(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		const pub =
			/^public\/shares\/([0-9a-f]{32})(?:\/(entries|blobs\/[0-9a-f]{64}))?$/.exec(
				p,
			);
		if (pub) {
			const share = this.activeShareBySlug(pub[1] ?? "");
			const t = share && this.threads.get(share.threadId);
			if (!share || !t || m !== "GET")
				return publicJson({ code: "not_found", message: "share" }, 404);
			const scope = this.shareScope(share);
			if (pub[2] === "entries") {
				const after = Number(url.searchParams.get("after") ?? 0);
				const limit = Math.min(
					500,
					Math.max(1, Number(url.searchParams.get("limit") ?? 200)),
				);
				const rest = scope.entries.filter((e) => e.seq > after);
				return publicJson({
					entries: rest.slice(0, limit),
					hasMore: rest.length > limit,
					lastSeq: scope.until,
					leafEntryId: scope.leaf,
				});
			}
			// mock 的转录图片都是内联 base64，没有 blob 存储
			if (pub[2])
				return publicJson({ code: "not_found", message: "blob" }, 404);
			const attachments = [...this.attachments.values()]
				.map((a) => a.meta)
				.filter(
					(a) =>
						a.threadId === t.id &&
						(share.mode === "live" || a.createdAt <= share.createdAt),
				)
				.map((a) => ({
					id: a.id,
					name: a.name,
					size: a.size,
					mime: a.mime,
					...(a.publicUrl ? { url: a.publicUrl } : {}),
				}));
			const body: Schemas["PublicShare"] = {
				mode: share.mode,
				title: t.title || "Untitled",
				sharedAt: share.createdAt,
				threadCreatedAt: t.createdAt,
				lastSeq: scope.until,
				leafEntryId: scope.leaf,
				transcriptRev: 0,
				attachments,
				...(share.mode === "live" ? { state: t.state } : {}),
			};
			return publicJson(body);
		}
		const body = async (): Promise<Json> => {
			try {
				return (await req.json()) as Json;
			} catch {
				return {};
			}
		};
		const strip = ({ leafEntryId: _, ...s }: MockShare) => s;
		const newest = <T extends { createdAt: number }>(a: T, b: T) =>
			b.createdAt - a.createdAt;
		const threadShares = /^threads\/([^/]+)\/shares$/.exec(p);
		if (threadShares) {
			const tid = threadShares[1] ?? "";
			if (!this.threads.has(tid))
				return json({ code: "not_found", message: "thread" }, 404);
			if (m === "GET")
				return json({
					shares: [...this.shares.values()]
						.filter((s) => s.threadId === tid)
						.sort(newest)
						.map(strip),
				});
			if (m === "POST") {
				const b = await body();
				const mode = b.mode === "live" ? "live" : "snapshot";
				const main = (this.entries.get(tid) ?? []).filter((e) =>
					isMainFile(e.file),
				);
				const last = main[main.length - 1];
				const draft: MockShare = {
					id: "",
					threadId: tid,
					threadTitle: "",
					mode,
					slug: "",
					url: "",
					createdAt: Date.now(),
					findings: 0,
					untilSeq: last?.seq ?? 0,
					leafEntryId: last?.entryId,
				};
				const scan = this.scanShare(draft);
				const hits = scan.findings.reduce((n, f) => n + f.count, 0);
				if (hits > 0 && b.acknowledgeFindings !== true)
					return json(
						{
							code: "secrets_found",
							message: `发现 ${hits} 处疑似密钥`,
							scan,
						},
						409,
					);
				const share = this.addShare(tid, mode, Date.now(), {
					untilSeq: last?.seq ?? 0,
					leafEntryId: last?.entryId,
					findings: hits,
				});
				return json(strip(share), 201);
			}
		}
		if (p === "shares" && m === "GET")
			return json({
				shares: [...this.shares.values()].sort(newest).map(strip),
				files: [...this.published.values()]
					.sort(newest)
					.map(({ slug: _, ...f }) => f),
			});
		const shareMatch = /^shares\/([^/]+)$/.exec(p);
		if (shareMatch && m === "DELETE") {
			const share = this.shares.get(shareMatch[1] ?? "");
			if (!share) return json({ code: "not_found", message: "share" }, 404);
			this.revokeShare(share);
			return new Response(null, { status: 204 });
		}
		const attPublish = /^attachments\/([^/]+)\/publish$/.exec(p);
		if (attPublish && m === "POST") {
			const a = this.attachments.get(attPublish[1] ?? "");
			if (!a) return json({ code: "not_found", message: "attachment" }, 404);
			if (!a.meta.threadId)
				return json({ code: "invalid", message: "附件尚未绑定线程" }, 400);
			const { slug: _, ...f } = this.publishAttachment(a.meta.id);
			return json(f);
		}
		const orbPublish = /^orbs\/([^/]+)\/fs\/publish$/.exec(p);
		if (orbPublish && m === "POST") {
			const b = await body();
			const path = typeof b.path === "string" ? b.path : "";
			const threadId = typeof b.threadId === "string" ? b.threadId : "";
			const t = this.threads.get(threadId);
			if (!t || t.orbId !== orbPublish[1])
				return json({ code: "not_found", message: "thread" }, 404);
			const file = await handleOrbFetch(
				new Request(
					`${this.origin()}/api/v1/orbs/${orbPublish[1]}/fs/file?path=${encodeURIComponent(path)}`,
				),
			);
			if (!file?.ok) return file ?? json({ code: "not_found" }, 404);
			const data = await file.blob();
			const name = path.slice(path.lastIndexOf("/") + 1) || "file";
			const meta: Schemas["Attachment"] = {
				id: `att_${(this.idSeq++).toString(36).toUpperCase().padStart(26, "0")}`,
				threadId,
				name,
				mime: data.type || "application/octet-stream",
				size: data.size,
				sha256: "0".repeat(64),
				createdAt: Date.now(),
			};
			this.attachments.set(meta.id, { meta, data });
			const { slug: _, ...f } = this.publishAttachment(meta.id);
			return json(f, 201);
		}
		const pubFile = /^published-files\/([^/]+)$/.exec(p);
		if (pubFile && m === "DELETE") {
			const f = this.published.get(pubFile[1] ?? "");
			if (!f) return json({ code: "not_found", message: "file" }, 404);
			this.published.delete(f.id);
			const a = this.attachments.get(f.attachmentId);
			if (a) {
				const { publicUrl: __, ...meta } = a.meta;
				a.meta = meta;
			}
			return new Response(null, { status: 204 });
		}
		return null;
	}

	// ------------------------------------------------------------ REST

	async handleFetch(req: Request): Promise<Response | null> {
		const url = new URL(req.url, location.origin);
		const path = url.pathname;
		if (path === "/healthz") return json({ ok: true, version: "0.1.0-mock" });
		if (!path.startsWith("/api/v1/")) return null;
		const notifyRes = await handleNotifyFetch(req);
		if (notifyRes) return notifyRes;
		const shareRes = await this.handleShareFetch(
			req,
			path.slice("/api/v1/".length),
			req.method.toUpperCase(),
			url,
		);
		if (shareRes) return shareRes;
		const orbRes = await handleOrbFetch(req);
		if (orbRes) return orbRes;
		const p = path.slice("/api/v1/".length);
		const m = req.method.toUpperCase();
		const body = async (): Promise<Json> => {
			try {
				return (await req.json()) as Json;
			} catch {
				return {};
			}
		};
		const autoRes = await this.automation.handle(req, p, m, url);
		if (autoRes) return autoRes;
		const deskRes = await this.desktop.handle(req, p, m);
		if (deskRes) return deskRes;
		if (p === "me")
			return json({
				displayName: "owner",
				createdAt: NOW - 90 * 86_400_000,
				auth: { kind: "web" },
				passkeyCount: 1,
			});
		if (p === "ws-ticket")
			return json({ ticket: "mock-ticket", expiresIn: 30 });
		if (p === "projects" && m === "GET")
			return json({
				projects: this.projects.filter((x) => !x.archivedAt),
			});
		if (p === "projects" && m === "POST") {
			const b = (await body()) as Schemas["ProjectCreate"];
			const bad = badGitPair(b.gitAuthorName, b.gitAuthorEmail);
			if (bad) return bad;
			const created: Project = {
				// 新建时空串等同缺省
				...(Object.fromEntries(
					Object.entries(b).filter(([, v]) => v !== ""),
				) as Schemas["ProjectCreate"]),
				id: `prj_mock${this.idSeq++}`,
				shallowClone: Boolean(b.shallowClone),
				createdAt: Date.now(),
				updatedAt: Date.now(),
			};
			this.projects.push(created);
			return json(created, 201);
		}
		const projectMatch = /^projects\/([^/]+)$/.exec(p);
		if (projectMatch) {
			const i = this.projects.findIndex((x) => x.id === projectMatch[1]);
			const cur = this.projects[i];
			if (!cur) return json({ code: "not_found", message: "project" }, 404);
			if (m === "PATCH") {
				const { archived, ...b } = (await body()) as Schemas["ProjectPatch"];
				const bad = badGitPair(b.gitAuthorName, b.gitAuthorEmail);
				if (bad) return bad;
				const next: Project = { ...cur, updatedAt: Date.now() };
				for (const [k, v] of Object.entries(b)) {
					const key = k as keyof Project;
					if (v === "") delete next[key];
					else Object.assign(next, { [key]: v });
				}
				if (archived !== undefined)
					next.archivedAt = archived ? Date.now() : undefined;
				this.projects[i] = next;
				return json(next);
			}
			if (m === "DELETE") {
				this.projects.splice(i, 1);
				return new Response(null, { status: 204 });
			}
			return json(cur);
		}
		const avatarMatch = /^settings\/(agent|user)-identity$/.exec(p);
		if (avatarMatch) {
			const key = avatarMatch[1] === "agent" ? "agentIdentity" : "userIdentity";
			if (m === "PUT") {
				const b = (await body()) as Schemas["AvatarIdentity"];
				const avatarIcon = b.avatarIcon?.trim() || undefined;
				const avatarImage = b.avatarImage || undefined;
				if (avatarIcon && avatarImage)
					return json(
						{
							code: "invalid_request",
							message: "avatarIcon 与 avatarImage 至多给一个",
						},
						400,
					);
				if (
					avatarImage &&
					!/^data:image\/(png|webp|jpeg);base64,/.test(avatarImage)
				)
					return json(
						{ code: "invalid_request", message: "avatarImage 格式无效" },
						400,
					);
				if (avatarImage && avatarImage.length > 128 << 10)
					return json(
						{ code: "too_large", message: "avatarImage 超过 128KiB" },
						413,
					);
				this[key] = { avatarIcon, avatarImage };
			}
			return json(this[key]);
		}
		if (p === "settings/git-identity") {
			if (m === "PUT") {
				const b = (await body()) as Schemas["GitIdentity"];
				const name = String(b.name ?? "").trim();
				const email = String(b.email ?? "").trim();
				if (!name || !email.includes("@"))
					return json(
						{ code: "invalid_request", message: "git 名字与邮箱无效" },
						400,
					);
				this.gitIdentity = { name, email };
			} else if (m === "DELETE") {
				this.gitIdentity = undefined;
				return new Response(null, { status: 204 });
			}
			return json(this.gitIdentity ? { identity: this.gitIdentity } : {});
		}
		if (p === "providers" && m === "GET") return json({ providers });
		if (p === "providers" && m === "POST") {
			const b = await body();
			return json(
				{
					provider: {
						...providers[0],
						id: `prov_${this.idSeq++}`,
						name: String(b.name),
					},
					token: "opv_mock_ONLY_SHOWN_ONCE",
				},
				201,
			);
		}
		if (p === "devices" && m === "GET") {
			return json({
				devices: [
					{
						id: "dev_1",
						name: "laptop-cli",
						kind: "cli",
						platform: "linux-x64",
						createdAt: NOW - 86_400_000,
						lastSeenAt: NOW - 600_000,
						current: false,
					},
					{
						id: "dev_2",
						name: "iPad",
						kind: "mobile",
						platform: "ios",
						createdAt: NOW - 7 * 86_400_000,
						lastSeenAt: NOW - 3 * 3_600_000,
					},
				],
			});
		}
		const pairMatch = /^devices\/pair\/([A-Z0-9-]+)$/.exec(p);
		if (pairMatch && m === "GET") {
			if (pairMatch[1] !== "ABCD-EFGH")
				return json({ code: "not_found", message: "unknown pairing" }, 404);
			return json({
				code: "ABCD-EFGH",
				name: "laptop-cli",
				kind: "cli",
				platform: "linux-x64",
				ip: "203.0.113.7",
				userAgent: "oryn/0.1.0 (linux; amd64)",
				createdAt: Date.now() - 30_000,
				expiresAt: Date.now() + 570_000,
			});
		}
		if (p === "devices/pair/approve")
			return new Response(null, { status: 204 });
		if (p === "secrets" && m === "GET") {
			return json({
				secrets: [
					{
						name: "omp/models/anthropic/apiKey",
						mode: "proxy",
						createdAt: NOW,
						updatedAt: NOW - 3_600_000,
					},
					{
						name: "git/github.com/token",
						mode: "proxy",
						hosts: ["github.com"],
						createdAt: NOW,
						updatedAt: NOW - 86_400_000,
					},
					{
						name: "NPM_TOKEN",
						mode: "env",
						createdAt: NOW,
						updatedAt: NOW - 2 * 86_400_000,
					},
				],
			});
		}
		if (p.startsWith("secrets/")) return new Response(null, { status: 204 });
		if (p === "agent-config/files") {
			return json({
				files: Object.entries(this.files).map(([path, content]) => ({
					path,
					size: content.length,
					mode: 420,
					sha256: "0".repeat(64),
					updatedAt: NOW - 3_600_000,
				})),
			});
		}
		if (p.startsWith("agent-config/files/")) {
			const fp = decodeURIComponent(p.slice("agent-config/files/".length));
			if (m === "GET")
				return new Response(this.files[fp] ?? "", {
					status: this.files[fp] === undefined ? 404 : 200,
				});
			if (m === "PUT") {
				this.files[fp] = await req.text();
				return json({
					file: {
						path: fp,
						size: this.files[fp]?.length ?? 0,
						mode: 420,
						sha256: "0".repeat(64),
						updatedAt: Date.now(),
					},
					extractedSecrets: [],
					warnings: [],
				});
			}
			if (m === "DELETE") {
				delete this.files[fp];
				return new Response(null, { status: 204 });
			}
		}
		if (p === "orbs" && m === "GET")
			return json({ orbs: [...this.orbs.values()] });
		const metricsMatch = /^orbs\/([^/]+)\/metrics$/.exec(p);
		if (metricsMatch) {
			const samples = Array.from({ length: 60 }, (_, i) => {
				const ts = Date.now() - (59 - i) * 30_000;
				return {
					ts,
					cpuPct: 18 + 30 * Math.abs(Math.sin(i / 6)) + (i % 7) * 2,
					memUsedMB: 2100 + 400 * Math.sin(i / 11) + i * 6,
					memLimitMB: 8192,
					diskUsedMB: 5400,
					netRxBytes: i * 120_000,
					netTxBytes: i * 40_000,
				};
			});
			return json({ orbId: metricsMatch[1], step: 30, samples });
		}
		const runsMatch = /^orbs\/([^/]+)\/setup-runs(?:\/([^/]+)\/log)?$/.exec(p);
		if (runsMatch && m === "GET") {
			const runs = this.setupRuns.get(runsMatch[1] ?? "") ?? [];
			if (!runsMatch[2]) return json({ runs: runs.slice(0, 20) });
			const log = runs.some((r) => r.id === runsMatch[2])
				? this.setupLogs.get(runsMatch[2])
				: undefined;
			if (log === undefined)
				return json({ code: "not_found", message: "setup run" }, 404);
			return new Response(log, {
				headers: { "Content-Type": "text/plain; charset=utf-8" },
			});
		}
		const orbAction = /^orbs\/([^/]+)\/(pause|resume)$/.exec(p);
		if (orbAction) {
			const orb = this.orbs.get(orbAction[1] ?? "");
			if (!orb) return json({ code: "not_found", message: "orb" }, 404);
			const next = {
				...orb,
				state:
					orbAction[2] === "pause" ? ("frozen" as const) : ("ready" as const),
				stateChangedAt: Date.now(),
			};
			this.orbs.set(orb.id, next);
			this.broadcast({ type: "orb", orb: next });
			return json(next, 202);
		}
		const svcRes = await this.handleServiceFetch(req, p, m, url);
		if (svcRes) return svcRes;
		if (p === "stats/usage") {
			const groups = [...this.threads.values()].map((t) => ({
				key: t.id,
				label: t.title,
				requests: 12,
				inputTokens: 120_000,
				outputTokens: 9_000,
				cacheReadTokens: 300_000,
				cacheWriteTokens: 0,
				costUsd: t.id === MAIN_THREAD ? 0.2381 : 0.05,
				subagentCostUsd: 0,
				tokensPerSecond: 64,
				toolCalls: 3,
				toolErrors: 0,
			}));
			return json({
				groupBy: url.searchParams.get("groupBy") ?? "thread",
				from: 0,
				to: Date.now(),
				groups,
			});
		}
		if (p === "threads" && m === "GET") {
			const archived = url.searchParams.get("archived") === "true";
			return json({
				threads: [...this.threads.values()].filter(
					(t) => Boolean(t.archivedAt) === archived,
				),
			});
		}
		const attRes = await this.handleAttachmentFetch(req, p, m, url);
		if (attRes) return attRes;
		if (p === "threads" && m === "POST") {
			const b = await body();
			const ids = Array.isArray(b.attachments)
				? (b.attachments as string[])
				: [];
			if (ids.some((a) => this.attachments.get(a)?.meta.threadId))
				return json(
					{ code: "attachment_not_found", message: "attachment" },
					404,
				);
			// 挂到已有 orb 时沿用那台 orb 的 Profile，不解析项目默认（与 internal/threads 一致），不做冲突预检；
			// mock 照旧另起一台 orb 演示，给它一张不带 Profile 的桌面
			const existing = (b.executor as Json | undefined)?.kind === "orb";
			const desktop = this.desktop.forNewOrb(
				existing ? { browserProfileId: "" } : b,
			);
			if (desktop instanceof Response) return desktop;
			return json({ thread: this.createThread(b, desktop) }, 201);
		}
		const threadMatch = /^threads\/([^/]+)(?:\/(fork|wake))?$/.exec(p);
		if (threadMatch) {
			const id = threadMatch[1] ?? "";
			const t = this.threads.get(id);
			if (!t) return json({ code: "not_found", message: "thread" }, 404);
			if (threadMatch[2] === "fork") {
				const b = await body();
				const copy = this.createThread({
					prompt: "",
					title: `fork：${t.title}`,
					parent: id,
					env: b.env,
				});
				return json({ thread: copy }, 201);
			}
			if (threadMatch[2] === "wake") {
				const orb = t.orbId ? this.orbs.get(t.orbId) : undefined;
				if (orb?.state === "failed") void this.recoverOrb(orb);
				else if (orb?.state === "frozen" || orb?.state === "stopped")
					void this.wakeOrb(orb);
				return new Response(null, { status: 202 });
			}
			if (m === "PATCH") {
				const b = await body();
				const next: Thread = { ...t, updatedAt: Date.now() };
				if (typeof b.title === "string")
					Object.assign(next, { title: b.title, titleSource: "user" });
				if (typeof b.pinned === "boolean") next.pinned = b.pinned;
				if (typeof b.color === "string") next.color = b.color || undefined;
				if (Array.isArray(b.labels)) next.labels = b.labels as string[];
				if (typeof b.archived === "boolean")
					next.archivedAt = b.archived ? Date.now() : undefined;
				if (typeof b.snoozedUntil === "number")
					next.snoozedUntil = b.snoozedUntil || undefined;
				if (typeof b.spaceId === "string") {
					if (t.kind && t.kind !== "work")
						return json(
							{ code: "coordinator_restricted", message: "space chat" },
							400,
						);
					if (
						b.spaceId &&
						!this.automation.spaces.some((s) => s.id === b.spaceId)
					)
						return json({ code: "space_not_found", message: "space" }, 404);
					next.spaceId = b.spaceId || undefined;
				}
				if (b.shipped === true) {
					next.shippedAt = Date.now();
					next.archivedAt ??= Date.now();
				} else if (b.shipped === false) next.shippedAt = undefined;
				this.upsertThread(next);
				return json(next);
			}
			if (m === "DELETE") {
				this.threads.delete(id);
				// 线程删除即撤销其全部分享（同 orynd 的级联删除）
				for (const s of [...this.shares.values()])
					if (s.threadId === id) this.revokeShare(s);
				for (const [aid, a] of this.attachments)
					if (a.meta.threadId === id) this.attachments.delete(aid);
				this.broadcast({
					type: "index.patch",
					upsert: [],
					remove: [id],
					rev: ++this.rev,
				});
				return new Response(null, { status: 204 });
			}
			return json(t);
		}
		return json({ code: "not_found", message: `mock: ${m} ${p}` }, 404);
	}

	/** 受管服务（列表、启停、重启、日志）与 portal 公开 / 终止（计划 M4 1.1）。 */
	private async handleServiceFetch(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		const svc =
			/^orbs\/([^/]+)\/services(?:\/([^/]+)\/(start|stop|restart|log))?$/.exec(
				p,
			);
		const share = /^orbs\/([^/]+)\/portals\/(\d+)\/share$/.exec(p);
		const orb = this.orbs.get((svc ?? share)?.[1] ?? "");
		if (!svc && !share) return null;
		if (!orb) return json({ code: "not_found", message: "orb" }, 404);
		if (svc) {
			const [, , name, op] = svc;
			if (!name)
				return json({
					services: orb.services ?? [],
					portals: orb.portals ?? [],
				});
			const cur = orb.services?.find((s) => s.name === name);
			if (!cur)
				return json(
					{ code: "service_not_found", message: `service ${name}` },
					404,
				);
			if (op === "log") {
				if (url.searchParams.has("bg") && orb.state !== "ready")
					return json({ code: "orb_not_ready", message: orb.state }, 409);
				return new Response(this.serviceLogs.get(name) ?? "", {
					headers: { "Content-Type": "text/plain; charset=utf-8" },
				});
			}
			if (m !== "POST") return null;
			const running = op !== "stop";
			const { pid: _, error: __, exitCode: ___, ...rest } = cur;
			const next: Schemas["OrbService"] = running
				? {
						...rest,
						status: "running",
						pid: 5000 + this.idSeq++,
						restarts: op === "restart" ? cur.restarts : 0,
						startedAt: Date.now(),
					}
				: { ...rest, status: "stopped" };
			const log = this.serviceLogs.get(name) ?? "";
			this.serviceLogs.set(
				name,
				`${log}${running ? `[orbd] starting ${name} (port ${cur.port ?? 0})` : "[orbd] exited code=143 (signal: terminated)"}\n`,
			);
			this.setOrb({
				...orb,
				services: orb.services?.map((s) => (s.name === name ? next : s)),
			});
			return json(next);
		}
		const port = Number(share?.[2]);
		const portal = orb.portals?.find((x) => x.port === port);
		if (!portal)
			return json({ code: "portal_not_found", message: `port ${port}` }, 404);
		let next: Schemas["OrbPortal"];
		if (m === "PUT") {
			const b = (await req.json()) as Schemas["PortalShareRequest"];
			const sec = { "1h": 3600, "3h": 10_800, "1d": 86_400, "7d": 604_800 }[
				b.duration
			];
			if (!sec)
				return json({ code: "invalid_request", message: "duration" }, 400);
			next = { ...portal, public: { expiresAt: Date.now() + sec * 1000 } };
		} else if (m === "DELETE") {
			const { public: _, ...rest } = portal;
			next = rest;
		} else return null;
		this.setOrb({
			...orb,
			portals: orb.portals?.map((x) => (x.port === port ? next : x)),
		});
		return m === "PUT" ? json(next) : new Response(null, { status: 204 });
	}

	private setOrb(orb: Orb) {
		this.orbs.set(orb.id, orb);
		this.broadcast({ type: "orb", orb });
	}

	/** failed orb 重试：waking → connecting → resuming → ready，其线程回到 idle。 */
	private async recoverOrb(orb: Orb) {
		const { failureReason: _, ...rest } = orb;
		for (const state of [
			"waking",
			"connecting",
			"resuming",
			"ready",
		] as const) {
			await sleep(this.delay * 8);
			this.setOrb({ ...rest, state, stateChangedAt: Date.now() });
			if (state === "waking")
				for (const tid of orb.threadIds) {
					const t = this.threads.get(tid);
					if (!t) continue;
					const { error: __, ...clean } = t;
					this.upsertThread({ ...clean, state: "provisioning" });
				}
		}
		for (const tid of orb.threadIds) {
			const t = this.threads.get(tid);
			if (t) this.upsertThread({ ...t, state: "idle" });
		}
	}

	/** 冻结 / 停机的 orb 唤醒：waking → connecting → resuming → ready（终端随 omp 进程保留）。 */
	private async wakeOrb(orb: Orb) {
		for (const state of [
			"waking",
			"connecting",
			"resuming",
			"ready",
		] as const) {
			await sleep(this.delay * 6);
			const cur = this.orbs.get(orb.id) ?? orb;
			this.setOrb({ ...cur, state, stateChangedAt: Date.now() });
		}
	}

	/** 记一条手动重跑成功的 setup 运行并推送 orb 更新。 */
	private recordManualSetup(orbId: string) {
		const orb = this.orbs.get(orbId);
		if (!orb) return;
		const now = Date.now();
		const run: Schemas["OrbSetupRun"] = {
			id: `osr_mock${(this.idSeq++).toString(16)}`,
			orbId,
			phase: "setup",
			trigger: "manual",
			startedAt: now - 38_000,
			finishedAt: now,
			ok: true,
			setupRan: true,
			durationMs: 38_000,
			hasRepoScript: true,
			hasProjectScript: orb.lastSetup?.hasProjectScript ?? false,
		};
		this.setupRuns.set(orbId, [run, ...(this.setupRuns.get(orbId) ?? [])]);
		this.setupLogs.set(run.id, "[setup] + oryn orb setup run\n[setup] ok\n");
		this.setOrb({ ...orb, lastSetup: run, stateChangedAt: now });
	}

	/** 登记一个新线程（空转录、默认会话状态）并推送 index.patch。 */
	private addThread(t: Thread) {
		this.entries.set(t.id, []);
		this.states.set(t.id, {
			...mainState(),
			todoPhases: [],
			contextUsage: { tokens: 0, contextWindow: 200_000, percent: 0 },
		});
		this.upsertThread(t);
	}

	private upsertThread(input: Thread) {
		// 同 orynd：离开 working 即清空活动
		const t = settleActivity(input);
		this.threads.set(t.id, t);
		this.broadcast({
			type: "index.patch",
			upsert: [t],
			remove: [],
			rev: ++this.rev,
		});
		// 匿名分享连接只收标题 / 状态的变化
		for (const [s, sub] of this.publicSockets) {
			if (sub.threadId !== t.id) continue;
			if (sub.state === t.state && sub.title === t.title) continue;
			sub.state = t.state;
			sub.title = t.title;
			s.deliver({ type: "share.state", state: t.state, title: t.title });
		}
	}

	// ------------------------------------------------------------ 附件

	private async handleAttachmentFetch(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		if (p === "attachments" && m === "POST") {
			const data = await req.blob();
			if (data.size > 100 * 1024 * 1024)
				return json({ code: "too_large", message: "attachment" }, 413);
			const name = (url.searchParams.get("name") ?? "file")
				.split("/")
				.pop() as string;
			const threadId = url.searchParams.get("threadId") ?? undefined;
			const ext = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
			const mimeByExt: Record<string, string> = {
				png: "image/png",
				jpg: "image/jpeg",
				jpeg: "image/jpeg",
				gif: "image/gif",
				webp: "image/webp",
				pdf: "application/pdf",
				txt: "text/plain; charset=utf-8",
				md: "text/markdown; charset=utf-8",
			};
			const meta: Schemas["Attachment"] = {
				id: `att_${(this.idSeq++).toString(36).toUpperCase().padStart(26, "0")}`,
				name,
				mime: mimeByExt[ext] ?? "application/octet-stream",
				size: data.size,
				sha256: "0".repeat(64),
				createdAt: Date.now(),
				...(threadId ? { threadId } : {}),
			};
			this.attachments.set(meta.id, { meta, data });
			return json(meta, 201);
		}
		const listMatch = /^threads\/([^/]+)\/attachments$/.exec(p);
		if (listMatch && m === "GET") {
			if (!this.threads.has(listMatch[1] ?? ""))
				return json({ code: "not_found", message: "thread" }, 404);
			return json({
				attachments: [...this.attachments.values()]
					.map((a) => a.meta)
					.filter((a) => a.threadId === listMatch[1]),
			});
		}
		const attMatch = /^attachments\/([^/]+)(\/content)?$/.exec(p);
		if (!attMatch) return null;
		const a = this.attachments.get(attMatch[1] ?? "");
		if (!a) return json({ code: "not_found", message: "attachment" }, 404);
		if (attMatch[2] && m === "GET")
			return new Response(a.data, {
				headers: { "Content-Type": "application/octet-stream" },
			});
		if (!attMatch[2] && m === "DELETE") {
			this.attachments.delete(a.meta.id);
			return new Response(null, { status: 204 });
		}
		return null;
	}

	/**
	 * 模拟控制面投递：绑定到线程并在消息末尾追加 `<oryn-attachments>` 块（路径形如 orb 内的投递位置）。
	 * 有附件属于别的线程时返回 null（投递失败）。
	 */
	private withAttachments(
		threadId: string,
		message: string,
		ids: unknown,
	): string | null {
		if (!Array.isArray(ids) || ids.length === 0) return message;
		const lines: string[] = [];
		for (const id of ids) {
			const a = this.attachments.get(String(id));
			if (!a || (a.meta.threadId && a.meta.threadId !== threadId)) return null;
			a.meta = { ...a.meta, threadId };
			lines.push(
				`- /home/user/.oryn/attachments/${threadId}/${a.meta.id}/${a.meta.name} (${a.meta.mime}, ${a.meta.size} bytes)`,
			);
		}
		const block = `<oryn-attachments>\nThe user attached these files. They are stored outside the repository; read them when relevant and do not copy them into the repository unless asked.\n${lines.join("\n")}\n</oryn-attachments>`;
		return message ? `${message}\n\n${block}` : block;
	}

	private createThread(
		b: Json,
		desktop?: (orb: Orb) => Schemas["OrbDesktop"],
	): Thread {
		const id = `T-mock-${(this.idSeq++).toString(16)}`;
		const orbId = `orb-mock${this.idSeq.toString().padStart(22, "0")}`;
		const prompt =
			this.withAttachments(
				id,
				typeof b.prompt === "string" ? b.prompt : "",
				b.attachments,
			) ?? "";
		const t: Thread = {
			id,
			title:
				typeof b.title === "string" && b.title
					? b.title
					: prompt.slice(0, 40) || "新线程",
			titleSource: "auto",
			state: "provisioning",
			stateChangedAt: Date.now(),
			pinned: false,
			labels: [],
			unread: false,
			createdAt: Date.now(),
			updatedAt: Date.now(),
			orbId,
			projectId: typeof b.projectId === "string" ? b.projectId : undefined,
			parentThreadId: typeof b.parent === "string" ? b.parent : undefined,
			portalUrlTemplate: MOCK_PORTAL_TEMPLATE,
		};
		this.entries.set(id, []);
		this.states.set(id, {
			...mainState(),
			todoPhases: [],
			contextUsage: { tokens: 0, contextWindow: 200_000, percent: 0 },
		});
		const orb = makeOrb(orbId, [id], "spawning");
		this.orbs.set(orbId, orb);
		this.upsertThread(t);
		// 新 orb 都带桌面；Profile 在线程登记之后再定（持有者要带线程标题）
		if (desktop) orb.desktop = desktop(orb);
		void (async () => {
			for (const state of ["connecting", "setting_up", "ready"] as const) {
				await sleep(this.delay * 8);
				const next = { ...orb, state, stateChangedAt: Date.now() };
				this.orbs.set(orbId, next);
				this.broadcast({ type: "orb", orb: next });
			}
			this.upsertThread({ ...t, state: "idle" });
			if (prompt) await this.runPrompt(id, prompt);
		})();
		return t;
	}

	// ------------------------------------------------------------ WS

	connect(socket: MockSocket) {
		if (socket.url?.includes("/api/v1/public/shares/")) {
			this.connectPublic(socket, socket.url);
			return;
		}
		this.sockets.add(socket);
		this.subs.set(socket, new Set());
		socket.deliver({
			type: "index",
			threads: [...this.threads.values()].filter((t) => !t.archivedAt),
			rev: this.rev,
		});
	}

	/**
	 * 匿名分享 WS（同 orynd `ServePublic`）：只接受未撤销的实时分享；先发 `share.snapshot`，
	 * 再按 200 条一批补发 `after` 之后的公开条目，之后只转发白名单内的消息。
	 */
	private connectPublic(socket: MockSocket, raw: string) {
		const u = new URL(raw, this.origin());
		const slug = /^\/api\/v1\/public\/shares\/([0-9a-f]{32})\/ws$/.exec(
			u.pathname,
		)?.[1];
		const share = slug ? this.activeShareBySlug(slug) : undefined;
		const t = share && this.threads.get(share.threadId);
		if (!share || !t || share.mode !== "live") {
			setTimeout(() => socket.close?.(), 0);
			return;
		}
		this.publicSockets.set(socket, {
			shareId: share.id,
			threadId: t.id,
			state: t.state,
			title: t.title,
		});
		const scope = this.shareScope(share);
		socket.deliver({
			type: "share.snapshot",
			title: t.title,
			state: t.state,
			leafEntryId: scope.leaf,
			lastSeq: scope.until,
			transcriptRev: 0,
			fullReplay: false,
			live: (this.liveFrames.get(t.id) ?? []).filter(
				(f) => PUBLIC_FRAMES[String(f.type)],
			),
			subagents: [],
		});
		const after = Number(u.searchParams.get("after") ?? 0);
		const rest = scope.entries.filter((e) => e.seq > after);
		for (let i = 0; i < rest.length; i += 200) {
			const batch = rest.slice(i, i + 200);
			const end = i + 200 >= rest.length;
			socket.deliver({
				type: "entries",
				entries: batch,
				leafEntryId: scope.leaf,
				lastSeq: end ? scope.until : (batch[batch.length - 1]?.seq ?? 0),
			});
		}
	}

	/** 把线程消息转成匿名连接能收的形态（不带 threadId；帧只放白名单内的类型）。 */
	private toPublic(threadId: string, msg: Json) {
		for (const [s, sub] of this.publicSockets) {
			if (sub.threadId !== threadId) continue;
			switch (msg.type) {
				case "entries":
					s.deliver({
						type: "entries",
						entries: msg.entries,
						leafEntryId: msg.leafEntryId,
						lastSeq: msg.lastSeq,
					});
					break;
				case "frame":
					if (PUBLIC_FRAMES[String((msg.frame as Json | undefined)?.type)])
						s.deliver({ type: "frame", frame: msg.frame });
					break;
				case "live.reset":
					s.deliver({ type: "live.reset" });
					break;
				case "transcript.reset":
					s.deliver({
						type: "transcript.reset",
						transcriptRev: msg.transcriptRev,
					});
					break;
			}
		}
	}

	disconnect(socket: MockSocket) {
		this.sockets.delete(socket);
		this.subs.delete(socket);
		this.termWatch.delete(socket);
		this.termWindow.delete(socket);
		this.publicSockets.delete(socket);
	}

	/** 调试：断开全部连接（客户端会重连并补拉）。 */
	dropConnections() {
		for (const s of [...this.sockets]) s.close?.();
	}

	/** 调试：模拟 omp 进程重启——其终端全部销毁，线程收到 `live.reset`。 */
	restartAgent(threadId: string) {
		this.terms.delete(threadId);
		this.broadcast({ type: "live.reset", threadId }, threadId);
	}

	/** 调试：某线程的终端列表。 */
	terminalInfos(threadId: string): RpcTerminalInfo[] {
		return [...(this.terms.get(threadId)?.values() ?? [])].map((t) => t.info);
	}

	// ------------------------------------------------------------ 终端替身

	private termsOf(threadId: string): Map<string, MockTerm> {
		let m = this.terms.get(threadId);
		if (!m) {
			m = new Map();
			this.terms.set(threadId, m);
		}
		return m;
	}

	private openTerm(
		threadId: string,
		kind: RpcTerminalInfo["kind"],
		title: string,
		size: { cols?: unknown; rows?: unknown },
		reveal?: boolean,
	): MockTerm {
		const terminalId = `term-fake-${++this.termSeq}`;
		const term: MockTerm = {
			info: {
				terminalId,
				kind,
				title,
				cwd: "/home/user/workspace",
				running: true,
				pid: 4000 + this.termSeq,
				cols: typeof size.cols === "number" ? size.cols : 100,
				rows: typeof size.rows === "number" ? size.rows : 30,
			},
			data: "",
			seq: 0,
			line: "",
			aborted: false,
		};
		this.termsOf(threadId).set(terminalId, term);
		this.frame(threadId, {
			type: "terminal_opened",
			terminal: term.info,
			...(reveal ? { reveal: true } : {}),
		});
		return term;
	}

	private termOut(threadId: string, term: MockTerm, data: string) {
		if (!this.terms.get(threadId)?.has(term.info.terminalId)) return;
		term.seq += data.length;
		term.data = (term.data + data).slice(-SNAPSHOT_CHARS);
		const frame = {
			type: "terminal_output",
			terminalId: term.info.terminalId,
			data,
			seq: term.seq,
		};
		const now = Date.now();
		for (const s of this.sockets) {
			if (!this.subs.get(s)?.has(threadId)) continue;
			const watching = this.termWatch.get(s);
			if (!watching?.has(threadId)) continue;
			let w = this.termWindow.get(s);
			if (!w || now - w.start > 1000) {
				w = { start: now, chars: 0 };
				this.termWindow.set(s, w);
			}
			w.chars += data.length;
			if (w.chars > this.termLag) {
				watching.delete(threadId);
				s.deliver({ type: "term.lagged", threadId });
				continue;
			}
			s.deliver({ type: "frame", threadId, frame });
		}
	}

	private termExit(threadId: string, term: MockTerm, exitCode: number) {
		term.info = { ...term.info, running: false, exitCode };
		this.frame(threadId, {
			type: "terminal_exit",
			terminalId: term.info.terminalId,
			exitCode,
			cancelled: false,
			timedOut: false,
		});
	}

	private termInput(threadId: string, term: MockTerm, data: string) {
		if (!term.info.running) return;
		if (term.onInput) {
			term.onInput(data);
			return;
		}
		// 方向键等转义序列不回显
		const text = data.replace(ESCAPE_SEQ, "");
		for (const ch of text) {
			if (ch === "\r") {
				const line = term.line;
				term.line = "";
				this.termOut(threadId, term, "\r\n");
				void this.runLine(threadId, term, line.trim());
			} else if (ch === "\x7f") {
				if (term.line) {
					term.line = term.line.slice(0, -1);
					this.termOut(threadId, term, "\b \b");
				}
			} else if (ch === "\x03") {
				term.line = "";
				term.aborted = true;
				this.termOut(threadId, term, `^C\r\n${PROMPT}`);
			} else if (ch >= " ") {
				term.line += ch;
				this.termOut(threadId, term, ch);
			}
		}
	}

	private async runLine(threadId: string, term: MockTerm, line: string) {
		term.aborted = false;
		const flood = /^flood (\d+)$/.exec(line);
		const count = /^count (\d+)$/.exec(line);
		if (line === "exit") {
			this.termExit(threadId, term, 0);
			return;
		}
		if (flood) {
			let left = Number(flood[1]);
			let frames = 0;
			while (left > 0 && !term.aborted) {
				const n = Math.min(FLOOD_FRAME, left);
				this.termOut(threadId, term, "x".repeat(n));
				left -= n;
				if (++frames % 8 === 0) await sleep(0);
			}
			this.termOut(threadId, term, `\r\n${PROMPT}`);
			return;
		}
		if (count) {
			const n = Number(count[1]);
			for (let i = 1; i <= n && !term.aborted; i++) {
				this.termOut(threadId, term, `line-${i}\r\n`);
				await sleep(20);
			}
			if (!term.aborted) this.termOut(threadId, term, PROMPT);
			return;
		}
		const echo = /^echo (.*)$/.exec(line);
		const out = echo
			? `${(echo[1] ?? "")
					.replace(/^(['"])(.*)\1$/, "$2")
					.replace(/\$TERM\b/g, "xterm-256color")
					.replace(/\$\(\((\d+)\*(\d+)\)\)/g, (_, a, b) =>
						String(Number(a) * Number(b)),
					)}\r\n`
			: "";
		this.termOut(threadId, term, `${out}${PROMPT}`);
	}

	/** 终端命令的替身；不是终端命令时返回 false。 */
	private termCmd(
		socket: MockSocket,
		threadId: string,
		id: string,
		type: string,
		frame: Json,
	): boolean {
		const terms = this.termsOf(threadId);
		const term =
			typeof frame.terminalId === "string"
				? terms.get(frame.terminalId)
				: undefined;
		const fail = () =>
			this.respond(
				socket,
				threadId,
				id,
				type,
				undefined,
				`unknown terminal ${String(frame.terminalId)}`,
			);
		switch (type) {
			case "terminal_configure":
				this.respond(socket, threadId, id, type, {
					interactiveCommands: frame.interactiveCommands === true,
					agentTools: frame.agentTools === true,
				});
				return true;
			case "terminal_open": {
				const t = this.openTerm(threadId, "shell", "bash", frame);
				this.respond(socket, threadId, id, type, t.info);
				this.termOut(threadId, t, PROMPT);
				return true;
			}
			case "terminal_input":
				if (!term) fail();
				else {
					this.respond(socket, threadId, id, type);
					this.termInput(threadId, term, String(frame.data ?? ""));
				}
				return true;
			case "terminal_resize":
				if (!term) {
					fail();
					return true;
				}
				term.info = {
					...term.info,
					cols: Number(frame.cols),
					rows: Number(frame.rows),
				};
				this.respond(socket, threadId, id, type);
				this.broadcast(
					{
						type: "term.resized",
						threadId,
						terminalId: term.info.terminalId,
						cols: term.info.cols,
						rows: term.info.rows,
					},
					threadId,
				);
				return true;
			case "terminal_close":
				if (!term) {
					fail();
					return true;
				}
				terms.delete(term.info.terminalId);
				term.aborted = true;
				this.frame(threadId, {
					type: "terminal_closed",
					terminalId: term.info.terminalId,
				});
				this.respond(socket, threadId, id, type);
				return true;
			case "terminal_list":
				this.respond(socket, threadId, id, type, {
					terminals: [...terms.values()].map((t) => t.info),
				});
				return true;
			case "terminal_snapshot":
				if (!term) fail();
				else
					this.respond(socket, threadId, id, type, {
						terminal: term.info,
						data: term.data,
						seq: term.seq,
					});
				return true;
			default:
				return false;
		}
	}

	private broadcast(msg: Json, threadId?: string) {
		for (const s of this.sockets) {
			if (threadId && !this.subs.get(s)?.has(threadId)) continue;
			s.deliver(msg);
		}
		if (threadId && this.publicSockets.size > 0) this.toPublic(threadId, msg);
	}

	private frame(threadId: string, frame: Json) {
		this.broadcast({ type: "frame", threadId, frame }, threadId);
		// 同 orynd：working 时最近一次工具调用的意图即线程活动，变化时推 index.patch
		const activity = frameActivity(frame);
		const t = this.threads.get(threadId);
		if (activity && t?.state === "working" && t.activity !== activity)
			this.upsertThread({ ...t, activity });
	}

	private appendEntry(threadId: string, message: Json) {
		const list = this.entries.get(threadId) ?? [];
		const last = [...list].reverse().find((e) => !e.entryId.startsWith("__"));
		const id = (this.idSeq++).toString(16).padStart(8, "0");
		const ts =
			typeof message.timestamp === "number" ? message.timestamp : Date.now();
		const e: Entry = {
			seq: (list[list.length - 1]?.seq ?? 0) + 1,
			file: SESSION_FILE,
			entryId: id,
			parentId: last?.entryId,
			type: "message",
			ts,
			payload: {
				type: "message",
				id,
				parentId: last?.entryId ?? null,
				timestamp: new Date(ts).toISOString(),
				message,
			},
		};
		list.push(e);
		this.entries.set(threadId, list);
		this.broadcast(
			{
				type: "entries",
				threadId,
				entries: [e],
				leafEntryId: id,
				lastSeq: e.seq,
			},
			threadId,
		);
	}

	onClient(socket: MockSocket, msg: Json) {
		const threadId = typeof msg.threadId === "string" ? msg.threadId : "";
		switch (msg.type) {
			case "ping":
				socket.deliver({ type: "pong" });
				return;
			case "focus":
				return;
			case "sub": {
				this.subs.get(socket)?.add(threadId);
				const t = this.threads.get(threadId);
				if (!t) {
					socket.deliver({
						type: "error",
						code: "not_found",
						message: "thread",
						threadId,
					});
					return;
				}
				const list = this.entries.get(threadId) ?? [];
				const after = typeof msg.afterSeq === "number" ? msg.afterSeq : 0;
				const orb = t.orbId ? this.orbs.get(t.orbId) : undefined;
				const last = list[list.length - 1];
				socket.deliver({
					type: "thread.snapshot",
					threadId,
					thread: t,
					orb,
					agent: {
						status: orb?.state === "ready" ? "ready" : "starting",
						state: this.states.get(threadId),
						pendingUi: this.pendingUi.get(threadId) ?? [],
						planProposal: this.planProposals.get(threadId),
						subagents: [],
						live: this.liveFrames.get(threadId) ?? [],
					},
					leafEntryId: last?.entryId,
					lastSeq: last?.seq ?? 0,
					transcriptRev: 0,
					fullReplay: false,
				});
				const rest = list.filter((e) => e.seq > after);
				// 重放帧只在首次订阅时投递，续传（afterSeq>0）不重复
				const frames =
					after === 0 ? (this.replayFrames.get(threadId) ?? []) : [];
				let fi = 0;
				let batch: Entry[] = [];
				const flush = () => {
					if (batch.length === 0) return;
					socket.deliver({
						type: "entries",
						threadId,
						entries: batch,
						leafEntryId: last?.entryId,
						// 本批覆盖到的最大 seq（可续传游标）
						lastSeq: batch[batch.length - 1]?.seq ?? 0,
					});
					batch = [];
				};
				const replayUpTo = (seq: number) => {
					for (
						;
						fi < frames.length && (frames[fi]?.afterSeq ?? 0) < seq;
						fi++
					) {
						flush();
						socket.deliver({
							type: "frame",
							threadId,
							frame: frames[fi]?.frame,
						});
					}
				};
				for (const e of rest) {
					replayUpTo(e.seq);
					batch.push(e);
					if (batch.length === 200) flush();
				}
				flush();
				replayUpTo(Number.POSITIVE_INFINITY);
				return;
			}
			case "unsub":
				this.subs.get(socket)?.delete(threadId);
				this.termWatch.get(socket)?.delete(threadId);
				return;
			case "ui.response": {
				const frame = msg.frame as Json;
				if (frame?.type !== "extension_ui_response") {
					socket.deliver({
						type: "error",
						code: "invalid_frame",
						message: "ui.response only accepts extension_ui_response",
						threadId,
					});
					return;
				}
				const reqId = String(frame.id ?? "");
				const list = this.pendingUi.get(threadId) ?? [];
				if (!list.some((r) => r.id === reqId)) return; // 已被别人先答
				this.pendingUi.set(
					threadId,
					list.filter((r) => r.id !== reqId),
				);
				this.broadcast(
					{ type: "ui.resolved", threadId, requestId: reqId },
					threadId,
				);
				this.waiters.get(reqId)?.(frame);
				this.waiters.delete(reqId);
				this.refreshThreadState(threadId);
				return;
			}
			case "term.watch": {
				if (!this.subs.get(socket)?.has(threadId)) {
					socket.deliver({
						type: "error",
						code: "not_subscribed",
						message: "term.watch before sub",
						threadId,
					});
					return;
				}
				let w = this.termWatch.get(socket);
				if (!w) {
					w = new Set();
					this.termWatch.set(socket, w);
				}
				if (msg.on === true) {
					w.add(threadId);
					this.termWindow.delete(socket);
				} else w.delete(threadId);
				return;
			}
			case "cmd":
				void this.handleCmd(
					socket,
					threadId,
					String(msg.id),
					msg.frame as Json,
				);
				return;
		}
	}

	private refreshThreadState(threadId: string) {
		const t = this.threads.get(threadId);
		if (!t) return;
		const waiting =
			(this.pendingUi.get(threadId)?.length ?? 0) > 0 ||
			this.planProposals.has(threadId);
		if (waiting && t.state !== "awaiting_input")
			this.upsertThread({ ...t, state: "awaiting_input" });
		if (!waiting && t.state === "awaiting_input")
			this.upsertThread({ ...t, state: "working" });
	}

	private respond(
		socket: MockSocket,
		threadId: string,
		id: string,
		command: string,
		data?: unknown,
		error?: string,
	) {
		socket.deliver({
			type: "res",
			threadId,
			id,
			response: error
				? { type: "response", command, success: false, error }
				: { type: "response", command, success: true, data },
		});
	}

	private async handleCmd(
		socket: MockSocket,
		threadId: string,
		id: string,
		frame: Json,
	) {
		const type = String(frame.type);
		if (this.termCmd(socket, threadId, id, type, frame)) return;
		const state = this.states.get(threadId) ?? mainState();
		const list = this.entries.get(threadId) ?? [];
		switch (type) {
			case "get_state":
				return this.respond(socket, threadId, id, type, state);
			case "get_available_commands":
				return this.respond(socket, threadId, id, type, {
					commands: [
						{
							name: "compact",
							description: "Manually compact the session context",
							source: "builtin",
							allowArgs: true,
							subcommands: [
								{ name: "snap", description: "Snapshot compaction" },
								{ name: "remote", description: "Remote compaction" },
							],
						},
						{
							name: "review",
							description: "Review the current changes",
							source: "skill",
							allowArgs: true,
							hasDynamicArgCompletion: true,
						},
						{
							name: "settings",
							description: "Open settings",
							source: "builtin",
						},
					],
				});
			case "get_command_arg_completions":
				return this.respond(socket, threadId, id, type, {
					items: ["HEAD~1", "main", "origin/main"]
						.filter((v) => v.startsWith(String(frame.prefix ?? "")))
						.map((value) => ({ value, description: "git ref" })),
				});
			case "get_queue": {
				const q = this.queues.get(threadId) ?? { steering: [], followUp: [] };
				const items = (lane: string[], p: string) =>
					lane.map((text, i) => ({
						id: `${p}${i}`,
						text,
						editable: true,
						timestamp: Date.now(),
					}));
				return this.respond(socket, threadId, id, type, {
					steering: items(q.steering, "s"),
					followUp: items(q.followUp, "f"),
				});
			}
			case "steer":
			case "follow_up": {
				const q = this.queues.get(threadId) ?? { steering: [], followUp: [] };
				(type === "steer" ? q.steering : q.followUp).push(
					String(frame.message ?? ""),
				);
				this.queues.set(threadId, q);
				this.frame(threadId, {
					type: "queue_update",
					steering: q.steering,
					followUp: q.followUp,
					liveSteered: 0,
				});
				return this.respond(socket, threadId, id, type);
			}
			case "queue_clear":
				this.queues.set(threadId, { steering: [], followUp: [] });
				this.frame(threadId, {
					type: "queue_update",
					steering: [],
					followUp: [],
					liveSteered: 0,
				});
				return this.respond(socket, threadId, id, type, { cleared: 0 });
			case "get_vibe_mode":
				return this.respond(socket, threadId, id, type, { enabled: false });
			case "get_loop_mode":
				return this.respond(socket, threadId, id, type, {
					enabled: false,
					state: "off",
				});
			case "get_subagents":
				return this.respond(socket, threadId, id, type, { subagents: [] });
			case "get_goal":
				return this.respond(socket, threadId, id, type, { enabled: false });
			case "get_btw_history":
				return this.respond(socket, threadId, id, type, { records: [] });
			// 角色模型（Ctrl+P 轮换、模型对话框的角色列表）：照主人的真实配置，default 用 @ 引用 Cyrene，writer 是裸 id（无 selected）
			case "get_settings":
				return this.respond(socket, threadId, id, type, {
					values: {
						cycleOrder: ["Hyacinthia", "Nahida", "Mare", "Cyrene", "Arianna"],
					},
					advisorEnabled: false,
				});
			case "get_model_roles": {
				const role = (
					rid: string,
					name: string,
					model: string | undefined,
					extra: Json = {},
				) => {
					const m = model?.match(/^([^/]+)\/(.+?)(?::(\w+))?$/);
					return {
						id: rid,
						name,
						section: "chat",
						model,
						selected:
							m && !model?.startsWith("@")
								? { provider: m[1], id: m[2], thinkingLevel: m[3] }
								: undefined,
						source: model ? "global" : "default",
						candidates: MOCK_ROLE_MODELS,
						...extra,
					};
				};
				return this.respond(socket, threadId, id, type, {
					roles: [
						role("default", "Default", "@Cyrene", { tag: "DEFAULT" }),
						role("smol", "Fast", "anthropic/claude-sonnet-5:medium", {
							tag: "SMOL",
						}),
						role("slow", "Thinking", "openai/gpt-6.1-sol:high", {
							tag: "SLOW",
						}),
						role("commit", "Commit", "openai/gpt-6.1-sol:medium", {
							tag: "COMMIT",
						}),
						role("web", "Web search", "openai-codex/gpt-6-astra", {
							tag: "WEB",
							section: "kind",
						}),
						role("Hyacinthia", "Hyacinthia", "anthropic/claude-fable-5-2:high"),
						role("Nahida", "Nahida", "google/gemini-4-carbon:high"),
						role("Mare", "Mare", "openai/gpt-6.1-sol:high"),
						role("Cyrene", "Cyrene", "anthropic/claude-opus-5-5:high"),
						role("Arianna", "Arianna", "deepseek/deepseek-flash:max"),
						// 裸 id 带级别：omp 不给 selected，由网页端照 resolveModelRoleValue 解析
						role("writer", "writer", "gemini-4-carbon:high"),
					],
				});
			}
			case "set_model": {
				const m = MOCK_ROLE_MODELS.find(
					(x) => x.provider === frame.provider && x.id === frame.modelId,
				);
				if (!m)
					return this.respond(
						socket,
						threadId,
						id,
						type,
						undefined,
						`Model not found: ${String(frame.provider)}/${String(frame.modelId)}`,
					);
				this.states.set(threadId, {
					...state,
					model: { ...m, api: "mock" } as unknown as SessionState["model"],
					...mockSpeedCaps(m.provider),
				});
				return this.respond(socket, threadId, id, type, m);
			}
			case "set_thinking_level": {
				const level = String(frame.level);
				// auto 时实际生效的级别不变（真实 omp 由模型自己挑），只改配置值
				this.states.set(threadId, {
					...state,
					...(level === "auto"
						? {}
						: { thinkingLevel: level as SessionState["thinkingLevel"] }),
					thinkingConfigured: level as SessionState["thinkingConfigured"],
				});
				return this.respond(socket, threadId, id, type, {
					thinkingLevel: level,
					thinkingConfigured: level,
				});
			}
			// 速度档位：与 omp 一致，fast / ultrafast 共用 fastModeEnabled，关 fast 同时清掉 ultrafast
			case "set_fast_mode":
			case "set_ultrafast_mode": {
				const on = frame.enabled === true;
				const ultra = type === "set_ultrafast_mode";
				if (on && !(ultra ? state.ultrafastSupported : state.fastModeSupported))
					return this.respond(
						socket,
						threadId,
						id,
						type,
						undefined,
						`${ultra ? "Ultrafast" : "Fast mode"} is unavailable for the current model.`,
					);
				const fast = ultra
					? on || (state.fastModeEnabled && !state.ultrafastEnabled)
					: on;
				const next = {
					...state,
					fastModeEnabled: fast,
					fastModeActive: fast,
					ultrafastEnabled: ultra ? on : false,
				};
				this.states.set(threadId, next);
				return this.respond(socket, threadId, id, type, {
					enabled: ultra ? next.ultrafastEnabled : next.fastModeEnabled,
					active: next.fastModeActive,
				});
			}
			case "set_slow_mode": {
				const on = frame.enabled === true;
				if (on && !state.slowModeSupported)
					return this.respond(
						socket,
						threadId,
						id,
						type,
						undefined,
						"Slow mode is unavailable for the current model.",
					);
				this.states.set(threadId, { ...state, slowModeEnabled: on });
				return this.respond(socket, threadId, id, type, { enabled: on });
			}
			case "get_available_models":
				return this.respond(socket, threadId, id, type, {
					models: [
						{
							id: "claude-sonnet-5",
							name: "Claude Sonnet 5",
							provider: "anthropic",
						},
						// 多厂商，方便核对 ModelLogo；ids 取自主人实际在用的模型
						{
							id: "claude-opus-5-5",
							name: "Claude Opus 5.5",
							provider: "anthropic",
						},
						{
							id: "claude-fable-5-2",
							name: "Claude Fable 5.2",
							provider: "anthropic",
						},
						{ id: "gpt-6.1-sol", name: "GPT-6.1 Sol", provider: "openai" },
						{
							id: "gpt-6-astra",
							name: "GPT-6 Astra",
							provider: "openai-codex",
						},
						{
							id: "gemini-4-carbon",
							name: "Gemini 4 Carbon",
							provider: "google",
						},
						{
							id: "deepseek-flash",
							name: "DeepSeek Flash",
							provider: "deepseek",
						},
						{
							id: "qwen/qwen3-max",
							name: "Qwen3 Max",
							provider: "openrouter",
						},
						{ id: "grok-5", name: "Grok 5", provider: "xai" },
						{ id: "jev-latest", name: "Jev", provider: "typesafe" },
						{ id: "exa", name: "Exa", provider: "web" },
					],
					discoveryStates: [],
					refreshPending: false,
					generation: 1,
				});
			case "get_session_tree": {
				const tree = list
					.filter((e) => e.type === "message" && !e.entryId.startsWith("__"))
					.map((e) => {
						const m = (e.payload as Json).message as Json;
						const content = m.content;
						const text =
							typeof content === "string"
								? content
								: Array.isArray(content)
									? content
											.map((b) =>
												b && typeof b === "object" && "text" in b
													? String(b.text)
													: "",
											)
											.join(" ")
									: "";
						return {
							entryId: e.entryId,
							parentId: e.parentId ?? null,
							role:
								m.role === "user"
									? "user"
									: m.role === "assistant"
										? "assistant"
										: "system",
							textPreview: text.slice(0, 80),
							timestamp: e.ts ?? 0,
							onActiveBranch: true,
							isLeaf: e === list[list.length - 1],
						};
					});
				return this.respond(socket, threadId, id, type, {
					tree,
					activeLeafId: list[list.length - 1]?.entryId ?? null,
				});
			}
			case "get_context_report":
				return this.respond(socket, threadId, id, type, {
					model: "claude-sonnet-5",
					contextWindow: 200_000,
					breakdown: {
						contextWindow: 200_000,
						anchored: true,
						usedTokens: 48_200,
						systemPromptTokens: 6_100,
						systemToolsTokens: 9_800,
						systemContextTokens: 2_300,
						instructionFilesTokens: 1_400,
						skillsTokens: 900,
						messagesTokens: 27_700,
					},
				});
			case "get_session_stats":
				return this.respond(socket, threadId, id, type, {
					sessionId: "s1",
					userMessages: 1,
					assistantMessages: 5,
					toolCalls: 3,
					toolResults: 3,
					totalMessages: 9,
					tokens: {
						input: 98_000,
						output: 4_200,
						reasoning: 800,
						cacheRead: 310_000,
						cacheWrite: 0,
						total: 413_000,
					},
					premiumRequests: 0,
					cost: 0.2381,
				});
			case "plan_approval":
				this.planProposals.delete(threadId);
				this.waiters.get(`plan:${threadId}`)?.(frame);
				this.waiters.delete(`plan:${threadId}`);
				this.broadcast({ type: "plan.resolved", threadId }, threadId);
				this.refreshThreadState(threadId);
				return this.respond(socket, threadId, id, type, {
					approved: frame.approved === true,
					dispatched: true,
				});
			case "set_plan_mode":
				this.states.set(threadId, {
					...state,
					planModeEnabled: frame.enabled === true,
				});
				return this.respond(socket, threadId, id, type, {
					enabled: frame.enabled === true,
				});
			case "set_agents_paused":
				this.states.set(threadId, {
					...state,
					agentsPaused: frame.enabled === true,
				});
				return this.respond(socket, threadId, id, type);
			case "set_todos":
				this.states.set(threadId, {
					...state,
					todoPhases: frame.phases as SessionState["todoPhases"],
				});
				return this.respond(socket, threadId, id, type, {
					todoPhases: frame.phases,
				});
			case "prompt":
			case "abort_and_prompt": {
				const message = this.withAttachments(
					threadId,
					String(frame.message ?? ""),
					frame.orynAttachments,
				);
				if (message === null)
					return this.respond(
						socket,
						threadId,
						id,
						type,
						undefined,
						"attachment delivery failed: attachment not found",
					);
				this.respond(socket, threadId, id, type, { agentInvoked: true });
				await this.runPrompt(threadId, message);
				return;
			}
			case "bash": {
				this.appendEntry(threadId, {
					role: "bashExecution",
					command: String(frame.command),
					output: "Linux orb-mock 6.12.0 #1 SMP x86_64 GNU/Linux\n",
					exitCode: 0,
					cancelled: false,
					truncated: false,
					excludeFromContext: frame.excludeFromContext === true,
					timestamp: Date.now(),
				});
				return this.respond(socket, threadId, id, type, { exitCode: 0 });
			}
			case "fork":
			case "fork_from":
				return this.respond(
					socket,
					threadId,
					id,
					type,
					undefined,
					"use_oryn_fork",
				);
			default:
				return this.respond(socket, threadId, id, type, {});
		}
	}

	private waitFor(key: string): Promise<Json> {
		return new Promise((resolve) => this.waiters.set(key, resolve));
	}

	/** fakeomp 风格的一轮：流式帧 + 条目复制。 */
	async runPrompt(threadId: string, text: string) {
		const d = this.delay;
		const t = this.threads.get(threadId);
		if (!t) return;
		this.upsertThread({ ...t, state: "working", lastMessageAt: Date.now() });
		this.frame(threadId, { type: "agent_start" });
		const userMsg = { role: "user", content: text, timestamp: Date.now() };
		this.frame(threadId, {
			type: "message_start",
			messageId: `m${this.idSeq++}`,
			message: userMsg,
		});
		this.frame(threadId, {
			type: "message_end",
			messageId: `m${this.idSeq++}`,
			message: userMsg,
		});
		await sleep(d);
		this.appendEntry(threadId, userMsg);

		const ts1 = Date.now();
		const mid1 = `m${this.idSeq++}`;
		const thinking =
			"用户要我在 orb 里执行命令并汇报结果。先跑 uname -a 看看内核与架构。";
		const intro = "我先在 orb 里执行一下命令。";
		const pty = /\bpty\b/i.test(text);
		const ptyCommand = "read -p 'Continue? [y/N] ' a; echo $a";
		const callArgs = JSON.stringify(
			pty
				? { command: ptyCommand, pty: true, i: "Asking in terminal" }
				: { command: "uname -a", i: "Checking kernel" },
		);
		const toolCallId = `toolu_${this.idSeq++}`;
		this.frame(threadId, {
			type: "message_start",
			messageId: mid1,
			message: assistant([], ts1, { stopReason: undefined }),
		});
		const upd = (ev: Json) =>
			this.frame(threadId, {
				type: "message_update",
				messageId: mid1,
				message: { role: "assistant" },
				assistantMessageEvent: ev,
			});
		upd({
			type: "thinking_start",
			contentIndex: 0,
			block: { type: "thinking", thinking: "" },
		});
		for (const chunk of thinking.match(/.{1,6}/gu) ?? []) {
			upd({ type: "thinking_delta", contentIndex: 0, delta: chunk });
			await sleep(d);
		}
		upd({ type: "thinking_end", contentIndex: 0, content: thinking });
		upd({
			type: "text_start",
			contentIndex: 1,
			block: { type: "text", text: "" },
		});
		for (const chunk of intro.match(/.{1,3}/gu) ?? []) {
			upd({ type: "text_delta", contentIndex: 1, delta: chunk });
			await sleep(d);
		}
		upd({ type: "text_end", contentIndex: 1, content: intro });
		upd({
			type: "toolcall_start",
			contentIndex: 2,
			block: { type: "toolCall", id: toolCallId, name: "bash", arguments: {} },
		});
		for (const chunk of callArgs.match(/.{1,8}/gu) ?? []) {
			upd({ type: "toolcall_delta", contentIndex: 2, delta: chunk });
			await sleep(d);
		}
		const call = {
			type: "toolCall",
			id: toolCallId,
			name: "bash",
			arguments: JSON.parse(callArgs) as Json,
		};
		upd({ type: "toolcall_end", contentIndex: 2, toolCall: call });
		const msg1 = assistant(
			[{ type: "thinking", thinking }, { type: "text", text: intro }, call],
			ts1,
		);
		this.frame(threadId, {
			type: "message_end",
			messageId: mid1,
			message: msg1,
		});
		this.appendEntry(threadId, msg1);

		this.frame(threadId, {
			type: "tool_execution_start",
			toolCallId,
			toolName: "bash",
			args: call.arguments,
			intent: "Checking kernel",
		});
		let out = "";
		if (pty) {
			// pty:true：在 orb 的终端里运行，等用户作答
			const term = this.openTerm(threadId, "command", ptyCommand, {}, true);
			const ask = "Continue? [y/N] ";
			this.termOut(threadId, term, ask);
			const answer = await new Promise<string>((resolve) => {
				let typed = "";
				term.onInput = (data) => {
					typed += data;
					this.termOut(threadId, term, data.replace(/\r/g, "\r\n"));
					if (/y/i.test(data)) resolve(typed.replace(/\r/g, ""));
				};
			});
			term.onInput = undefined;
			this.termOut(threadId, term, `\r\n${answer}\r\n`);
			this.termExit(threadId, term, 0);
			out = `${ask}${answer}\n${answer}`;
		}
		for (const line of pty
			? []
			: [
					"Linux orb-01jcz8m3 6.12.0-oryn #1 SMP PREEMPT_DYNAMIC",
					" x86_64 GNU/Linux",
				]) {
			await sleep(d * 4);
			out += line;
			this.frame(threadId, {
				type: "tool_execution_update",
				toolCallId,
				toolName: "bash",
				partialResult: { content: [{ type: "text", text: out }] },
			});
		}
		const result = {
			content: [{ type: "text", text: out }],
			details: { exitCode: 0, wallTimeMs: 210 },
		};
		this.frame(threadId, {
			type: "tool_execution_end",
			toolCallId,
			toolName: "bash",
			result,
			isError: false,
		});
		await sleep(d);
		this.appendEntry(threadId, {
			role: "toolResult",
			toolCallId,
			toolName: "bash",
			...result,
			isError: false,
			timestamp: Date.now(),
		});

		if (/ask/i.test(text)) {
			const reqId = `ui-${this.idSeq++}`;
			const req = {
				type: "extension_ui_request",
				id: reqId,
				method: "ask",
				questions: [
					{
						id: "q1",
						question: "输出要包含主机名吗？",
						options: [
							{ label: "包含", description: "原样输出 uname -a" },
							{ label: "去掉主机名" },
						],
						recommended: 0,
					},
				],
				timeout: 120_000,
			};
			this.pendingUi.set(threadId, [
				...(this.pendingUi.get(threadId) ?? []),
				req,
			]);
			this.frame(threadId, req);
			this.refreshThreadState(threadId);
			await this.waitFor(reqId);
		}
		if (/plan|计划/i.test(text)) {
			const proposal = {
				type: "plan_proposal",
				planFilePath: "/home/user/.omp/plans/reconnect.md",
				title: "重连改造",
				suggestedFileName: "reconnect-plan.md",
				planContent:
					"## 步骤\n\n1. 指数退避\n2. afterSeq 补拉\n3. 端到端测试\n",
				options: ["execute", "compact", "keep_context", "save", "refine"],
			};
			this.planProposals.set(threadId, proposal);
			this.frame(threadId, proposal);
			this.refreshThreadState(threadId);
			await this.waitFor(`plan:${threadId}`);
		}

		const ts2 = Date.now();
		const mid2 = `m${this.idSeq++}`;
		const answer = `命令输出：\n\n\`\`\`text\n${out}\n\`\`\`\n\n内核是 **6.12**，架构 \`x86_64\`。`;
		this.frame(threadId, {
			type: "message_start",
			messageId: mid2,
			message: assistant([], ts2, { stopReason: undefined }),
		});
		const upd2 = (ev: Json) =>
			this.frame(threadId, {
				type: "message_update",
				messageId: mid2,
				message: { role: "assistant" },
				assistantMessageEvent: ev,
			});
		upd2({
			type: "text_start",
			contentIndex: 0,
			block: { type: "text", text: "" },
		});
		for (const chunk of answer.match(/[\s\S]{1,4}/gu) ?? []) {
			upd2({ type: "text_delta", contentIndex: 0, delta: chunk });
			await sleep(d);
		}
		upd2({ type: "text_end", contentIndex: 0, content: answer });
		const msg2 = assistant([{ type: "text", text: answer }], ts2, {
			stopReason: "stop",
		});
		this.frame(threadId, {
			type: "message_end",
			messageId: mid2,
			message: msg2,
		});
		this.appendEntry(threadId, msg2);
		this.frame(threadId, {
			type: "agent_end",
			messages: [],
			isTerminal: true,
			yielded: true,
		});
		this.frame(threadId, {
			type: "prompt_result",
			agentInvoked: true,
			status: "completed",
			sessionSettled: true,
		});
		this.frame(threadId, { type: "session_settled" });
		const cur = this.threads.get(threadId);
		if (cur)
			this.upsertThread({ ...cur, state: "idle", lastMessageAt: Date.now() });
		if (cur?.orbId && text.includes("oryn-setup"))
			this.recordManualSetup(cur.orbId);
	}
}
