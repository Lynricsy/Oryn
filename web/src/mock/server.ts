/**
 * 浏览器内 mock 服务端：实现足够的 REST 与 WS（计划 C3）让界面无需后端即可体验。
 * 发送提示词时按 fakeomp 的帧序列回放：agent_start → 用户消息 → 流式 assistant（思考 / 文本 / 工具调用）→
 * bash 工具生命周期 → 最终回答 → agent_end → prompt_result → session_settled，期间按序复制条目；
 * 回合结束稍后再推一帧 `oryn_composer_prediction`（下一条消息预测）。
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
import { AGENT_DEFAULT_GLYPHS } from "../lib/avatar-defaults";
import {
	DESKTOP_ASK_DONE,
	DESKTOP_ASK_SKIP,
	desktopAskTitle,
} from "../lib/desktop-ask";
import { readConfigDoc } from "../lib/omp-config";
import { parseSideQuestion } from "../lib/side-ask";
import { parseModelSource } from "../lib/turn-role";
import type {
	RpcBtwHistoryRecord,
	RpcTerminalInfo,
	SessionState,
} from "../omp/types";
import { activeBranch, isMainFile } from "../state/transcript";
import { MockArtifacts } from "./artifacts";
import { MockAssistant, xdevMountNotice } from "./assistant";
import { MockAutomation } from "./automation";
import { MockBoxes } from "./boxes";
import { MockDesktop } from "./desktop";
import {
	assistant,
	buildM9Transcripts,
	buildM11bTranscripts,
	buildM96Transcripts,
	buildMainTranscript,
	buildToolZooTranscript,
	projects as fixtureProjects,
	runners as fixtureRunners,
	threads as fixtureThreads,
	M96_IDS,
	M96_ROLE_THREAD_MODEL,
	MAIN_THREAD,
	MOCK_PORTAL_TEMPLATE,
	MOCK_ROLE_AVATARS,
	MODEL_SOURCE_TYPE,
	mainState,
	makeOrb,
	mockSpeedCaps,
	NOW,
	orbEnv,
	poolOrbs,
	projectSecrets,
	projectSnapshots,
	providers,
	runnerOrb,
	SESSION_FILE,
	serviceLogs,
	setupLogs,
	TOOL_ZOO_THREAD,
	toolZooLiveFrames,
	toolZooReplayFrames,
} from "./fixtures";
import { MockGitHub } from "./github";
import { MockLibrary } from "./library";
import { MockMemory } from "./memory";
import { handleNotifyFetch } from "./notify";
import { handleOrbFetch } from "./orbfs";
import { MockOwnerMemory } from "./owner-memory";
import { MockRepo } from "./repo";
import { handleSearchFetch } from "./search";
import { redact, SHARE_RULES } from "./share-scan";
import { MockSsh } from "./ssh";
import { handleStatsFetch } from "./stats";

type Json = Record<string, unknown>;

export interface MockSocket {
	/** FakeWebSocket 的连接地址：匿名分享 WS 据此分流。 */
	readonly url?: string;
	deliver(msg: Json): void;
	close?(): void;
}

/** 固定 slug 的分享（截图脚本直接打开 `/s/<slug>`）：主线程两条，M9.6 的 role 线程两条（回合头身份）。 */
export const MOCK_SHARE_SLUGS = {
	snapshot: "5a0c1e7f3b9d42a8c6e1f0b2d4a69c3e",
	live: "c3e9a14b7d2f4e6a8b0c5d1e9f2a7b64",
	roleSnapshot: "9d6e2b17c4a84f30b5e1d8c27a9f0e61",
	roleLive: "4b8f1c3e9a2d47e6b0c5a1d8e7f29c03",
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

type MockShare = Schemas["ThreadShare"] & {
	leafEntryId?: string;
	/** 快照分享创建时固化的身份（同 `thread_share_identities`）；实时分享不存。 */
	identity?: Schemas["PublicShareIdentity"];
};
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

/** 中央模型目录里的一个模型（`omp models --json` 的子集；按新版 Core 报告 thinkingDefault，空串 = 没有自带默认档位）。 */
function mockCatalogModel(
	provider: string,
	id: string,
	name: string,
	kind: string,
	thinking?: string[],
): Schemas["CatalogModel"] {
	return {
		provider,
		id,
		selector: `${provider}/${id}`,
		name,
		kind,
		reasoning: thinking !== undefined,
		...(thinking ? { thinking } : {}),
		thinkingDefault: "",
		input: ["text"],
	};
}

/** `GET /api/v1/models/catalog`：多厂商，方便核对 ModelLogo；ids 取自主人实际在用的模型。含非对话模型，核对选择器只列 chat。 */
const MOCK_CATALOG_MODELS: Schemas["CatalogModel"][] = [
	mockCatalogModel("anthropic", "claude-sonnet-5", "Claude Sonnet 5", "chat", [
		"low",
		"medium",
		"high",
		"xhigh",
	]),
	mockCatalogModel("anthropic", "claude-opus-5-5", "Claude Opus 5.5", "chat", [
		"low",
		"medium",
		"high",
		"xhigh",
		"max",
	]),
	mockCatalogModel(
		"anthropic",
		"claude-fable-5-2",
		"Claude Fable 5.2",
		"chat",
		["low", "medium", "high", "xhigh", "max"],
	),
	mockCatalogModel("openai", "gpt-6.1-sol", "GPT-6.1 Sol", "chat", [
		"minimal",
		"low",
		"medium",
		"high",
		"xhigh",
	]),
	mockCatalogModel("openai-codex", "gpt-6-astra", "GPT-6 Astra", "chat", [
		"low",
		"medium",
		"high",
		"xhigh",
	]),
	mockCatalogModel("google", "gemini-4-carbon", "Gemini 4 Carbon", "chat", [
		"low",
		"medium",
		"high",
	]),
	mockCatalogModel("deepseek", "deepseek-flash", "DeepSeek Flash", "chat", [
		"low",
		"high",
		"max",
	]),
	mockCatalogModel("openrouter", "qwen/qwen3-max", "Qwen3 Max", "chat"),
	mockCatalogModel("xai", "grok-5", "Grok 5", "chat", ["low", "high"]),
	mockCatalogModel("typesafe", "jev-latest", "Jev", "chat"),
	// 补足真实目录的规模（几十个对话模型）与长名称：选择器的筛选框、滚动、截断都在这种量下才看得出问题
	mockCatalogModel("anthropic", "claude-haiku-5", "Claude Haiku 5", "chat"),
	mockCatalogModel("openai", "gpt-6.1-mini", "GPT-6.1 Mini", "chat", [
		"low",
		"medium",
		"high",
	]),
	mockCatalogModel("openai", "o7-pro", "o7 Pro", "chat", ["high", "xhigh"]),
	mockCatalogModel(
		"google",
		"gemini-4-flash-lite",
		"Gemini 4 Flash Lite",
		"chat",
	),
	mockCatalogModel("mistral", "mistral-large-3", "Mistral Large 3", "chat"),
	mockCatalogModel("mistral", "codestral-2611", "Codestral 2611", "chat"),
	mockCatalogModel("moonshot", "kimi-k3-thinking", "Kimi K3 Thinking", "chat", [
		"low",
		"high",
	]),
	mockCatalogModel("zhipu", "glm-5.1-air", "GLM-5.1 Air", "chat"),
	mockCatalogModel(
		"openrouter",
		"meta-llama/llama-5-405b-instruct-preview-2611",
		"Llama 5 405B Instruct Preview (November 2026 snapshot)",
		"chat",
	),
	mockCatalogModel("openrouter", "minimax/minimax-m3", "MiniMax M3", "chat"),
	mockCatalogModel(
		"bedrock",
		"us.anthropic.claude-opus-5-5-v1:0",
		"Claude Opus 5.5 (Bedrock us-east-1)",
		"chat",
	),
	mockCatalogModel(
		"vertex",
		"gemini-4-carbon-001",
		"Gemini 4 Carbon (Vertex)",
		"chat",
	),
	mockCatalogModel("ollama", "qwen3-coder:32b", "Qwen3 Coder 32B", "chat"),
	mockCatalogModel(
		"lmstudio",
		"deepseek-r3-distill-qwen-14b",
		"DeepSeek R3 Distill Qwen 14B",
		"chat",
	),
	mockCatalogModel("web", "exa", "Exa", "search"),
	mockCatalogModel(
		"openai",
		"text-embedding-3-small",
		"Text Embedding 3 Small",
		"embedding",
	),
];

/** mock 的 cycleOrder（同 `get_settings`）。 */
const MOCK_CYCLE = ["Hyacinthia", "Nahida", "Mare", "Cyrene", "Arianna"];

/** 各 role 的配置值（`modelRoles.<role>`，同 `get_model_roles` 的 `model`）：default 用 @ 引用 Cyrene，writer 是裸 id。 */
const MOCK_ROLE_SELECTORS: Record<string, string> = {
	default: "@Cyrene",
	smol: "anthropic/claude-sonnet-5:medium",
	slow: "openai/gpt-6.1-sol:high",
	commit: "openai/gpt-6.1-sol:medium",
	web: "openai-codex/gpt-6-astra",
	Hyacinthia: "anthropic/claude-fable-5-2:high",
	Nahida: "google/gemini-4-carbon:high",
	Mare: "openai/gpt-6.1-sol:high",
	Cyrene: "anthropic/claude-opus-5-5:high",
	Arianna: "deepseek/deepseek-flash:max",
	writer: "gemini-4-carbon:high",
};

/** 一次按 role 或手动的模型选择：模型、role 预设的思考级别、别名目标。 */
interface MockRolePick {
	model: (typeof MOCK_ROLE_MODELS)[number];
	level?: string;
	alias?: string;
}

/**
 * 按 role 解析模型（`oryn_select_model{role}` 用，同 Core 的 `resolveRoleModelWithThinking` 的静态子集）：
 * `@<role>` 只跟一跳并记为别名；`provider/id:level` 或裸 id 在 MOCK_ROLE_MODELS 里查。
 */
function resolveMockRole(role: string): MockRolePick | undefined {
	const raw = MOCK_ROLE_SELECTORS[role];
	if (!raw) return undefined;
	const target = raw.startsWith("@") ? raw.slice(1).split(":")[0] : undefined;
	const value = target ? MOCK_ROLE_SELECTORS[target] : raw;
	const m = value?.match(/^(?:([^/]+)\/)?(.+?)(?::(\w+))?$/);
	const model = m
		? MOCK_ROLE_MODELS.find(
				(x) => x.id === m[2] && (!m[1] || x.provider === m[1]),
			)
		: undefined;
	if (!model) return undefined;
	return { model, level: m?.[3], ...(target ? { alias: target } : {}) };
}

/**
 * 分享里 role 的不透明键（同 orynd `roleKey` 的用途：每个分享各不相同、不可反推）。mock 没有同步 sha256，
 * 用两路 32 位 FNV 拼成 16 位十六进制；只需「同分享同 role 稳定、不同分享不同」。
 */
function roleKey(shareId: string, role: string): string {
	let a = 0x811c9dc5;
	let b = 0x9747b28c;
	for (const ch of `${shareId}\u0000${role}`) {
		const c = ch.codePointAt(0) ?? 0;
		a = Math.imul(a ^ c, 0x01000193) >>> 0;
		b = Math.imul(b ^ c, 0x5bd1e995) >>> 0;
	}
	return a.toString(16).padStart(8, "0") + b.toString(16).padStart(8, "0");
}

/** omp 的保留 role（`model_change.role` 里不是 role id 的取值）：匿名面上原样保留。 */
const RESERVED_ROLES: Record<string, true> = {
	default: true,
	fallback: true,
	prewalk: true,
	temporary: true,
};

/** 名字与 role id 里不得出现的控制字符（同 orynd 的 `unicode.IsControl` 校验的 ASCII 部分）。 */
// biome-ignore lint/suspicious/noControlCharactersInRegex: 正是要匹配控制字符
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/;

/** Core 打包的内置 agent（固定顺序，同 orynd `agentcfg.BundledAgents`）；默认字形表正好按这个顺序列出。 */
const BUNDLED_AGENTS = Object.keys(AGENT_DEFAULT_GLYPHS);

/** 名字：去首尾空白，空 = 未设置；超过 40 个字符或含控制字符 → 错误文案。 */
function identityName(raw: unknown): { name?: string; error?: string } {
	const name = typeof raw === "string" ? raw.trim() : "";
	if (!name) return {};
	if ([...name].length > 40 || CONTROL_CHARS.test(name))
		return { error: "name 须是 1–40 个字符且不含控制字符" };
	return { name };
}

/** 头像两字段的校验（同 orynd `validAvatar`）；通过时返回清洗后的值。 */
function avatarFields(
	b: Schemas["AvatarIdentity"],
): { avatar: Schemas["AvatarIdentity"] } | { error: Response } {
	const avatarIcon = b.avatarIcon?.trim() || undefined;
	const avatarImage = b.avatarImage || undefined;
	if (avatarIcon && avatarImage)
		return {
			error: json(
				{
					code: "invalid_request",
					message: "avatarIcon 与 avatarImage 至多给一个",
				},
				400,
			),
		};
	if (avatarImage && !/^data:image\/(png|webp|jpeg);base64,/.test(avatarImage))
		return {
			error: json(
				{ code: "invalid_request", message: "avatarImage 格式无效" },
				400,
			),
		};
	if (avatarImage && avatarImage.length > 128 << 10)
		return {
			error: json(
				{ code: "too_large", message: "avatarImage 超过 128KiB" },
				413,
			),
		};
	return { avatar: { avatarIcon, avatarImage } };
}

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

/** 托管仓库的克隆地址（同 orbs.HostedCloneURL；mock 的 public_url 是 https://oryn.example.com）。 */
function hostedCloneUrl(projectId: string): string {
	return `https://oryn.example.com/git/${projectId}.git`;
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

/** 预热 orb 数越界（0–3，M11a；与服务端一致回 400）。 */
function badWarmOrbs(n?: number): Response | null {
	if (n === undefined || (Number.isInteger(n) && n >= 0 && n <= 3)) return null;
	return json(
		{ code: "invalid_request", message: "warmOrbs 须在 0–3 之间" },
		400,
	);
}

/** 自定义选区快捷动作的校验与规整（同 internal/api/selection_actions.go）：不合法时返回错误说明。 */
function normalizeSelectionActions(
	input: unknown,
): Schemas["SelectionActions"] | string {
	const list = (input as { actions?: unknown } | null)?.actions;
	if (!Array.isArray(list)) return "actions 须为数组";
	if (list.length > 12) return "actions 至多 12 条";
	const seen = new Set<string>();
	const actions: Schemas["SelectionAction"][] = [];
	for (const [i, raw] of list.entries()) {
		const a = (raw ?? {}) as Partial<Schemas["SelectionAction"]>;
		const id = String(a.id ?? "");
		if (!/^[a-z0-9]{8}$/.test(id))
			return `actions[${i}].id 须为 8 位小写字母或数字`;
		if (seen.has(id)) return `actions[${i}].id 重复`;
		seen.add(id);
		const name = String(a.name ?? "").trim();
		if (name === "" || [...name].length > 24)
			return `actions[${i}].name 须为 1-24 个字符`;
		const prompt = String(a.prompt ?? "").trim();
		if (prompt === "" || [...prompt].length > 2000)
			return `actions[${i}].prompt 须为 1-2000 个字符`;
		actions.push({ id, name, prompt });
	}
	return { actions };
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
	/** runner（M11b）：列表、新建、删除；它们的常驻 orb 在 `orbs` 里（`kind: "runner"`）。 */
	readonly runners: Schemas["Runner"][] = fixtureRunners.map((r) => ({ ...r }));
	readonly pendingUi = new Map<string, Json[]>();
	/** setup / resume 运行记录（按 orb，新的在前）与日志。 */
	readonly setupRuns = new Map<string, Schemas["OrbSetupRun"][]>();
	readonly setupLogs = new Map<string, string>(Object.entries(setupLogs));
	/** 受管服务日志（服务名 → 文本）。 */
	readonly serviceLogs = new Map<string, string>(Object.entries(serviceLogs));
	readonly planProposals = new Map<string, Json>();
	/** 最近一次非空的输入框预测（同控制面缓存，进 `thread.snapshot` 的 `agent.composerPrediction`）。 */
	readonly predictions = new Map<string, string[]>();
	/** 快照 `agent.live` 里的在途帧（按线程）。 */
	readonly liveFrames = new Map<string, Json[]>();
	/** 首次订阅（afterSeq=0）时按位置插在条目之间投递的帧（按线程）。 */
	readonly replayFrames = new Map<
		string,
		{ afterSeq: number; frame: Json }[]
	>();
	readonly projects: Project[] = fixtureProjects.map((p) => ({ ...p }));
	/** 项目快照（M11a），按项目 id。 */
	readonly snapshots = new Map<string, Schemas["ProjectSnapshot"][]>(
		Object.entries(projectSnapshots).map(([k, v]) => [
			k,
			v.map((s) => ({ ...s })),
		]),
	);
	/** 项目级密钥与环境变量（M24 阶段 ④），按项目 id；保密条目不存值。 */
	readonly projectSecrets = new Map<string, Schemas["ProjectSecret"][]>(
		Object.entries(projectSecrets).map(([k, v]) => [
			k,
			v.map((s) => ({ ...s })),
		]),
	);
	/** 浏览器 Profile 与 orb 桌面（M8）。 */
	readonly desktop = new MockDesktop({
		orbs: this.orbs,
		setOrb: (orb) => this.setOrb(orb),
		projects: () => this.projects,
		threadTitle: (id) => this.threads.get(id)?.title,
	});
	/** 全局助手文件库（M13）。 */
	readonly library = new MockLibrary(this.threads);
	/** Artifacts（M14）：列表、详情、版本、帧与观看者。 */
	readonly artifacts = new MockArtifacts(this.threads, {
		changed: () => this.broadcast({ type: "artifacts.changed" }),
	});
	/** 全局助手设置、MCP 列表与助手的电脑（M19）。 */
	readonly assistant = new MockAssistant({
		orbs: this.orbs,
		setOrb: (orb) => this.setOrb(orb),
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
	/** SSH 主机与网关（M18）。 */
	readonly ssh = new MockSsh({
		changed: () => this.broadcast({ type: "ssh.changed" }),
		threads: this.threads,
		orbs: this.orbs,
	});
	/** 项目仓库镜像（M24）。 */
	readonly repo = new MockRepo({
		changed: (projectId) =>
			this.broadcast({ type: "project_repo.changed", projectId }),
		projects: () => this.projects,
		threads: this.threads,
	});
	/** Boxes（M23）：box 的常驻 orb 由它放进 `orbs`。 */
	readonly boxes = new MockBoxes({
		changed: () => this.broadcast({ type: "boxes.changed" }),
		threads: this.threads,
		orbs: this.orbs,
		orbChanged: (orb) => this.broadcast({ type: "orb", orb }),
		serviceLogs: this.serviceLogs,
	});
	/** GitHub 集成（M14）。 */
	readonly github = new MockGitHub({
		changed: () => this.broadcast({ type: "github.changed" }),
		threads: this.threads,
		upsertThread: (t) => this.upsertThread(t),
		projects: () => this.projects,
		addAttachment: (name, mime, text) => {
			const data = new Blob([text], { type: mime });
			const meta: Schemas["Attachment"] = {
				id: `att_${(this.idSeq++).toString(36).toUpperCase().padStart(26, "0")}`,
				name,
				mime,
				size: data.size,
				sha256: "0".repeat(64),
				createdAt: Date.now(),
			};
			this.attachments.set(meta.id, { meta, data });
			return meta;
		},
		startThread: (t, prompt) => {
			this.addThread(t);
			this.appendEntry(t.id, {
				role: "user",
				content: prompt,
				timestamp: Date.now(),
			});
		},
	});
	/** 内置 Hindsight 记忆（M16）。 */
	readonly memory = new MockMemory({
		restart: (reason) => this.noteRestartReason(reason),
		threadTitle: (id) => this.threads.get(id)?.title,
	});
	/** 全局助手的主人记忆（M17）：以工程记忆已启用并配置为前提。 */
	readonly ownerMemory = new MockOwnerMemory({
		engineeringReady: () => this.memory.ready(),
		thread: (id) => this.threads.get(id),
		projectName: (id) => this.projects.find((x) => x.id === id)?.name,
	});
	gitIdentity: Schemas["GitIdentity"] | undefined = {
		name: "Oryn Owner",
		email: "owner@oryn.example",
	};
	/** 全局 agent 身份（名字 + 头像，内存态）；都缺省 = 默认品牌标、不显示名字。 */
	agentIdentity: Schemas["AgentIdentity"] = {};
	/** 全局助手身份（原始值；缺省时客户端显示「全局助手」与 sparkle，不跟随 agent）。 */
	assistantIdentity: Schemas["AgentIdentity"] = {};
	/** 用户头像（内存态）；默认给一个 emoji，截图里能看到效果。 */
	userIdentity: Schemas["AvatarIdentity"] = { avatarIcon: "🦊" };
	/** 模型目录的读取时间；`?refresh=true` 时更新。 */
	catalogFetchedAt = Date.now();
	/** role 头像（`role_avatars`）：行存在 = 设了头像。 */
	roleAvatars: Record<string, Schemas["AvatarIdentity"]> = {
		...MOCK_ROLE_AVATARS,
	};
	/** 子 agent 头像（`agent_avatars`）：行存在 = 设了头像。 */
	agentAvatars: Record<string, Schemas["AvatarIdentity"]> = {};
	/** 新对话输入框偏好（`owner.new_thread_target`）。 */
	composerPrefs: Schemas["ComposerPrefs"] = { defaultTarget: "role" };
	/** 自定义选区快捷动作（`owner.selection_actions`，M22）。 */
	selectionActions: Schemas["SelectionActions"] = { actions: [] };
	/** config.yml 解析失败的原因（模拟 `configError`）。 */
	roleConfigError: string | undefined;
	/** 旧 Core 的线程：不认识 `oryn_*` 命令（应答 `Unknown command`），也不写来源记录。 */
	readonly legacyThreads = new Set<string>([M96_IDS.legacyThread]);
	readonly queues = new Map<
		string,
		{ steering: string[]; followUp: string[] }
	>();
	private readonly sockets = new Set<MockSocket>();
	private readonly subs = new Map<MockSocket, Set<string>>();
	private readonly waiters = new Map<string, (value: Json) => void>();
	private rev = 1;
	private idSeq = 100;
	/** btw 旁路提问的话题记录（按线程）；验收脚本经 `__orynMock.btwRecords` 读发出去的问题。 */
	readonly btwRecords = new Map<string, RpcBtwHistoryRecord[]>();
	/** 每线程至多一个在答的 btw：逐段推送回答的计时器 */
	private readonly btwTimers = new Map<
		string,
		{ recordId: string; timer: ReturnType<typeof setInterval> }
	>();
	private btwSeq = 0;
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
	/**
	 * omp 配置的会话级生效（M13 apply-status）：需重启的线程 → 原因与安排重启的时间；等回合结束才能重新加载的会话数与截止时间。
	 * 模拟里「agent 在跑」的会话取最近更新的 3 个未归档工作线程；安排重启后 5 秒视为已重启，回合中的会话 8 秒后视为已重新加载。
	 */
	readonly applyRestarts = new Map<
		string,
		{ reasons: Set<string>; scheduledAt?: number }
	>();
	applyReloads = { count: 0, until: 0 };
	/** 全局 omp-home 的模拟下发：写入后 1 秒（服务端的防抖时长）完成。 */
	applyPushAt?: number;

	/** 非 omp-home 的设置改动（如记忆开关）要求重启：同样记到「agent 在跑」的那几个线程上。 */
	private noteRestartReason(reason: string): void {
		const live = [...this.threads.values()]
			.filter(
				(t) =>
					!t.archivedAt &&
					(t.kind ?? "work") === "work" &&
					t.state !== "error" &&
					t.state !== "provisioning",
			)
			.sort((a, b) => b.updatedAt - a.updatedAt)
			.slice(0, 3);
		for (const t of live) {
			const entry = this.applyRestarts.get(t.id) ?? { reasons: new Set() };
			entry.reasons.add(reason);
			entry.scheduledAt = undefined;
			this.applyRestarts.set(t.id, entry);
		}
	}

	/** 全局 omp-home 的一次写入或删除：按 docs/protocol.md「会话级生效与懒重启」的分类记到线程上（`config.yml` 只比较 `disabledExtensions`）。 */
	private noteOmpHomeChange(path: string, before: string | undefined): void {
		this.applyPushAt = Date.now() + 1_000;
		const restart: string[] = [];
		let reload = false;
		if (path === "config.yml") {
			const ids = (text: string | undefined): Set<string> => {
				const list: unknown = readConfigDoc(text ?? "").toJS()
					?.disabledExtensions;
				return new Set(
					Array.isArray(list)
						? list.filter((v): v is string => typeof v === "string")
						: [],
				);
			};
			const prev = ids(before);
			const next = ids(this.files[path]);
			for (const id of [...prev, ...next]) {
				if (prev.has(id) && next.has(id)) continue;
				const prefix = id.split(":")[0] ?? "";
				if (
					/^(skill|slash-command|rule|prompt|instruction|context-file|mcp)$/.test(
						prefix,
					)
				)
					reload = true;
				else restart.push(id);
			}
		} else if (
			/^(skills|commands|rules|prompts|instructions)\/|^(AGENTS\.md|RULES\.md|mcp\.json)$/.test(
				path,
			)
		)
			reload = true;
		else if (!path.startsWith("agents/")) restart.push(path);
		const live = [...this.threads.values()]
			.filter(
				(t) =>
					!t.archivedAt &&
					(t.kind ?? "work") === "work" &&
					t.state !== "error" &&
					t.state !== "provisioning",
			)
			.sort((a, b) => b.updatedAt - a.updatedAt)
			.slice(0, 3);
		for (const t of live) {
			if (restart.length === 0) break;
			const entry = this.applyRestarts.get(t.id) ?? { reasons: new Set() };
			for (const r of restart) entry.reasons.add(r);
			this.applyRestarts.set(t.id, entry);
		}
		const working = live.filter((t) => t.state === "working").length;
		if (reload && working > 0)
			this.applyReloads = { count: working, until: Date.now() + 8_000 };
	}

	private applyStatus(): Schemas["AgentConfigApplyStatus"] {
		const now = Date.now();
		const threads: Schemas["AgentConfigRestartThread"][] = [];
		for (const [id, entry] of [...this.applyRestarts].sort(([a], [b]) =>
			a.localeCompare(b),
		)) {
			const t = this.threads.get(id);
			if (
				!t ||
				(entry.scheduledAt !== undefined && now - entry.scheduledAt > 5_000)
			) {
				this.applyRestarts.delete(id);
				continue;
			}
			threads.push({
				threadId: id,
				kind: t.kind ?? "work",
				title: t.title,
				reasons: [...entry.reasons],
				scheduled: entry.scheduledAt !== undefined,
			});
		}
		const pushAt = this.applyPushAt;
		return {
			threads,
			pendingReloads:
				now < this.applyReloads.until ? this.applyReloads.count : 0,
			push: {
				pending: pushAt !== undefined && now < pushAt,
				...(pushAt !== undefined && now >= pushAt ? { appliedAt: pushAt } : {}),
				failed: [],
			},
		};
	}

	/** 项目级 Agent 配置（M12.2）：项目 id → 相对项目配置根的路径 → 内容。 */
	projectFiles: Record<string, Record<string, string>> = {
		[fixtureProjects[0]?.id ?? ""]: {
			"AGENTS.md": "# 本项目\n\n- 前端用 bun，不用 npm\n",
			"mcp.json": `{\n  "mcpServers": {\n    "linear": {\n      "url": "https://mcp.linear.app/sse",\n      "headers": { "Authorization": "Bearer \${LINEAR_TOKEN}" }\n    }\n  }\n}\n`,
			"rules/no-npm.md":
				'---\nname: no-npm\ndescription: 用 bun\ncondition: ["\\\\bnpm install\\\\b"]\nscope: text\n---\n\n用 `bun install`。\n',
			"skills/release/SKILL.md": "---\nname: release\n---\n按发版清单走。\n",
		},
	};

	constructor() {
		// runner 的常驻 orb（M11b）：先放进去，下面按线程补 orb 时跳过
		for (const r of this.runners)
			this.orbs.set(
				r.orbId,
				runnerOrb(
					r,
					fixtureThreads.filter((x) => x.orbId === r.orbId).map((x) => x.id),
				),
			);
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
		// 预热池 orb（M11a）没有线程，单独加入
		for (const o of poolOrbs) this.orbs.set(o.id, { ...o });
		// 任务栏（M25）：有桌面的 orb 带上应用与初始窗口
		this.desktop.seed();
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
		for (const [id, list] of Object.entries({
			...buildM9Transcripts(),
			...buildM96Transcripts(),
			...buildM11bTranscripts(),
		}))
			this.entries.set(id, list);
		this.liveFrames.set(TOOL_ZOO_THREAD, toolZooLiveFrames());
		this.replayFrames.set(TOOL_ZOO_THREAD, toolZooReplayFrames(zoo));
		for (const t of fixtureThreads)
			this.states.set(t.id, {
				...mainState(),
				todoPhases: t.id === MAIN_THREAD ? mainState().todoPhases : [],
			});
		const roleState = this.states.get(M96_IDS.roleThread);
		if (roleState)
			this.states.set(M96_IDS.roleThread, {
				...roleState,
				model: {
					...M96_ROLE_THREAD_MODEL,
					api: "mock",
				} as unknown as SessionState["model"],
				...mockSpeedCaps(M96_ROLE_THREAD_MODEL.provider),
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

	/**
	 * 主线程：一条快照分享（2 小时前）、一条实时分享（20 分钟前）与一个已发布的附件；
	 * M9.6 的 role 线程：一条快照分享与一条实时分享（分享页的回合头身份）。
	 */
	private seedShares() {
		const roleMain = this.entries.get(M96_IDS.roleThread) ?? [];
		const roleLast = roleMain[roleMain.length - 1];
		this.addShare(M96_IDS.roleThread, "snapshot", NOW - 30 * 60_000, {
			slug: MOCK_SHARE_SLUGS.roleSnapshot,
			untilSeq: roleLast?.seq ?? 0,
			leafEntryId: roleLast?.entryId,
		});
		this.addShare(M96_IDS.roleThread, "live", NOW - 25 * 60_000, {
			slug: MOCK_SHARE_SLUGS.roleLive,
		});
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
		// 附件对话框的压力数据：超长文件名、图片、大文件、未发布（ui-audit thread.dlg.attachments）
		for (const [i, name, mime, size, ago] of [
			[
				1,
				"screenshot-2026-11-20-reconnect-backoff-timeline-full-page@2x.png",
				"image/png",
				1_482_331,
				2,
			],
			[2, "afterSeq-replay-fixture.json", "application/json", 48_211, 26],
			[
				3,
				"WebSocket 断线重连设计评审纪要（第二版，含时序图与回退方案）.pdf",
				"application/pdf",
				12_604_118,
				50,
			],
		] as const) {
			const extra: Schemas["Attachment"] = {
				id: `att_01JCZ8MOCKRECONNECTEXTRA0${i}`,
				threadId: MAIN_THREAD,
				name,
				mime,
				size,
				sha256: "0".repeat(64),
				createdAt: NOW - ago * 3_600_000,
			};
			this.attachments.set(extra.id, { meta: extra, data: new Blob(["x"]) });
		}
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
		// 快照的身份在创建时固化（之后改名字、换头像都不影响）；实时分享每次现算
		if (mode === "snapshot") share.identity = this.shareIdentity(share);
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

	/** role 头像列表里的一项（同 orynd `role_identity.go` 的组装）。 */
	private roleIdentity(role: string): Schemas["RoleIdentity"] {
		const inCycle = MOCK_CYCLE.includes(role);
		const selector = inCycle ? MOCK_ROLE_SELECTORS[role] : undefined;
		return {
			role,
			inCycle,
			...(selector ? { selector } : {}),
			...this.roleAvatars[role],
		};
	}

	/** 先是 cycleOrder 里的 role（按其顺序），再是只剩头像的遗留项（按 role 升序）。 */
	roleIdentities(): Schemas["RoleIdentityList"] {
		const orphans = Object.keys(this.roleAvatars)
			.filter((r) => !MOCK_CYCLE.includes(r))
			.sort();
		const level: unknown = readConfigDoc(this.files["config.yml"] ?? "").toJS()
			?.defaultThinkingLevel;
		return {
			roles: [...MOCK_CYCLE, ...orphans].map((r) => this.roleIdentity(r)),
			...(this.roleConfigError ? { configError: this.roleConfigError } : {}),
			defaultThinkingLevel: typeof level === "string" ? level : "high",
		};
	}

	/** omp-home `agents/*.md` 里的自定义 agent（frontmatter 的 name / description；没有 name 的跳过），按名字排序。 */
	private userAgents(): { name: string; description?: string }[] {
		const out: { name: string; description?: string }[] = [];
		for (const [path, text] of Object.entries(this.files)) {
			if (!/^agents\/[^/]+\.md$/.test(path)) continue;
			const fm = /^---\n([\s\S]*?)\n---/.exec(text)?.[1] ?? "";
			const name = /^name:\s*(.+)$/m.exec(fm)?.[1]?.trim();
			if (!name) continue;
			const description = /^description:\s*(.+)$/m.exec(fm)?.[1]?.trim();
			out.push(description ? { name, description } : { name });
		}
		return out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
	}

	/** 子 agent 头像列表（同 orynd `listAgentAvatars`）：内置（固定顺序）→ 自定义（按名字）→ 只剩头像的遗留项（按名字）。 */
	agentAvatarList(): Schemas["AgentAvatarList"] {
		const user = this.userAgents();
		const known = new Set([...BUNDLED_AGENTS, ...user.map((a) => a.name)]);
		const orphans = Object.keys(this.agentAvatars)
			.filter((n) => !known.has(n))
			.sort();
		return {
			agents: [
				...BUNDLED_AGENTS.map((name) => ({
					name,
					source: "bundled" as const,
					...this.agentAvatars[name],
				})),
				...user.map((a) => ({
					...a,
					source: "user" as const,
					...this.agentAvatars[a.name],
				})),
				...orphans.map((name) => ({
					name,
					source: "orphan" as const,
					...this.agentAvatars[name],
				})),
			],
		};
	}

	/**
	 * 分享页的身份（同 orynd `buildIdentity`）：全局 agent 身份 + 这段对话里「用到」的 role——
	 * `role` / `startup` 记录生效期间（到下一条记录或下一条非 fallback / prewalk 的 model_change）出现过消息条目，
	 * 取候选 `[role, alias]` 里第一个设了头像的；没用到的 role 不出现。
	 */
	private shareIdentity(share: MockShare): Schemas["PublicShareIdentity"] {
		const a = this.agentIdentity;
		const roles: Schemas["PublicRoleIdentity"][] = [];
		let open: string[] | undefined;
		let used = false;
		const flush = () => {
			const role = used ? open?.find((r) => this.roleAvatars[r]) : undefined;
			if (role && !roles.some((x) => x.role === role))
				roles.push({
					key: roleKey(share.id, role),
					role,
					...this.roleAvatars[role],
				});
			open = undefined;
			used = false;
		};
		for (const e of this.shareScope(share).entries) {
			const r = parseModelSource(e);
			if (r) {
				flush();
				if (r.source !== "manual")
					open = [r.role, r.alias].filter((x): x is string => !!x);
			} else if (e.type === "model_change") {
				const role = (e.payload as Json | undefined)?.role;
				if (role !== "fallback" && role !== "prewalk") flush();
			} else if (e.type === "message") used = true;
		}
		flush();
		return a.name || a.avatarIcon || a.avatarImage
			? { agent: { ...a }, roles }
			: { roles };
	}

	/**
	 * 匿名面上的条目：来源记录的 role / alias 换成该分享的不透明键、只留白名单字段；`model_change` 的 role 名
	 * 同样换成键（`fallback` / `prewalk` / `temporary` / `default` 这些保留值原样，判定要用）；其余原样。
	 */
	private publicEntry(share: MockShare, e: Entry): Entry {
		const r = parseModelSource(e);
		if (!r) {
			const p = e.payload as Json | undefined;
			const role = e.type === "model_change" ? p?.role : undefined;
			if (typeof role !== "string" || Object.hasOwn(RESERVED_ROLES, role))
				return e;
			return { ...e, payload: { ...p, role: roleKey(share.id, role) } };
		}
		return {
			...e,
			payload: {
				type: "custom",
				customType: MODEL_SOURCE_TYPE,
				data: {
					v: 1,
					source: r.source,
					model: r.model,
					...(r.role ? { role: roleKey(share.id, r.role) } : {}),
					...(r.alias ? { alias: roleKey(share.id, r.alias) } : {}),
				},
			},
		};
	}

	/** 分享、文件发布与匿名接口（计划 M5 1.1）。 */
	private async handleShareFetch(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		const pub =
			/^public\/shares\/([0-9a-f]{32})(?:\/(entries|identity|blobs\/[0-9a-f]{64}))?$/.exec(
				p,
			);
		if (pub) {
			const share = this.activeShareBySlug(pub[1] ?? "");
			const t = share && this.threads.get(share.threadId);
			if (!share || !t || m !== "GET")
				return publicJson({ code: "not_found", message: "share" }, 404);
			const scope = this.shareScope(share);
			if (pub[2] === "identity")
				return publicJson(
					share.mode === "snapshot"
						? (share.identity ?? { roles: [] })
						: this.shareIdentity(share),
				);
			if (pub[2] === "entries") {
				const after = Number(url.searchParams.get("after") ?? 0);
				const limit = Math.min(
					500,
					Math.max(1, Number(url.searchParams.get("limit") ?? 200)),
				);
				const rest = scope.entries.filter((e) => e.seq > after);
				return publicJson({
					entries: rest.slice(0, limit).map((e) => this.publicEntry(share, e)),
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
		const notifyRes = await handleNotifyFetch(req, {
			// 通知按钮作答 = 线程收到回复：等你处理 → 工作中
			answered: (id) => {
				const t = this.threads.get(id);
				if (t?.state === "awaiting_input")
					this.upsertThread({
						...t,
						state: "working",
						stateChangedAt: Date.now(),
						activity: "按你的选择继续",
					});
			},
		});
		if (notifyRes) return notifyRes;
		const shareRes = await this.handleShareFetch(
			req,
			path.slice("/api/v1/".length),
			req.method.toUpperCase(),
			url,
		);
		if (shareRes) return shareRes;
		const orbRes = await handleOrbFetch(req, this.runnerScope);
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
		const libRes = await this.library.handle(req, p, m, url);
		if (libRes) return libRes;
		const artRes = await this.artifacts.handle(req, p, m, url);
		if (artRes) return artRes;
		const deskRes = await this.desktop.handle(req, p, m);
		if (deskRes) return deskRes;
		const statsRes = await handleStatsFetch(req, p, m, url, {
			threads: () => [...this.threads.values()],
			projects: () => this.projects,
			spaces: () => this.automation.spaces,
			orbs: () => [...this.orbs.values()],
		});
		if (statsRes) return statsRes;
		const searchRes = handleSearchFetch(p, m, url, {
			threads: () => [...this.threads.values()],
			entries: (id) => this.entries.get(id) ?? [],
		});
		if (searchRes) return searchRes;
		const asstRes = await this.assistant.handle(req, p, m);
		if (asstRes) return asstRes;
		if (p === "me")
			return json({
				displayName: "owner",
				createdAt: NOW - 90 * 86_400_000,
				auth: { kind: "web" },
				passkeyCount: 1,
			});
		if (p === "ws-ticket")
			return json({ ticket: "mock-ticket", expiresIn: 30 });
		if (p === "projects" && m === "GET") {
			// 与 ListProjects 一致：archived=true 只列已归档，否则只列未归档
			const archived = url.searchParams.get("archived") === "true";
			return json({
				projects: this.projects.filter(
					(x) => Boolean(x.archivedAt) === archived,
				),
			});
		}
		if (p === "projects" && m === "POST") {
			const b = (await body()) as Schemas["ProjectCreate"];
			const bad =
				badGitPair(b.gitAuthorName, b.gitAuthorEmail) ??
				badWarmOrbs(b.warmOrbs);
			if (bad) return bad;
			if (b.hosted && b.repoUrl)
				return json(
					{
						code: "invalid_request",
						message: "hosted 与 repoUrl 只能二选一",
					},
					400,
				);
			const { emptyRepo: _emptyRepo, ...rest } = b;
			const id = `prj_mock${this.idSeq++}`;
			const created: Project = {
				// 新建时空串等同缺省
				...(Object.fromEntries(
					Object.entries(rest).filter(([, v]) => v !== ""),
				) as Schemas["ProjectCreate"]),
				id,
				shallowClone: Boolean(b.shallowClone),
				snapshots: b.snapshots ?? true,
				warmOrbs: b.warmOrbs ?? 0,
				createdAt: Date.now(),
				updatedAt: Date.now(),
			};
			// M24 阶段 ③：托管仓库，克隆地址指向控制面，基线缺省 main
			if (b.hosted) {
				created.hosted = true;
				created.cloneUrl = hostedCloneUrl(id);
				created.baseBranch = b.baseBranch || "main";
			} else delete created.hosted;
			// M14：仓库地址是 App 可访问的 mock 仓库时自动绑定
			const gh = this.github.projectGitHub(created.repoUrl);
			if (gh) created.github = gh;
			this.projects.push(created);
			return json(created, 201);
		}
		const snapMatch = /^projects\/([^/]+)\/snapshots$/.exec(p);
		if (snapMatch) {
			const pid = snapMatch[1] ?? "";
			if (!this.projects.some((x) => x.id === pid))
				return json({ code: "not_found", message: "project" }, 404);
			const list = this.snapshots.get(pid) ?? [];
			if (m === "DELETE") {
				// 与服务端一致：把匹配的 ready 快照退役（原因 manual），镜像由回收器异步删除
				const type = url.searchParams.get("type");
				this.snapshots.set(
					pid,
					list.map((s) =>
						s.state === "ready" && (!type || s.type === type)
							? { ...s, state: "retired", retireReason: "manual" }
							: s,
					),
				);
				return new Response(null, { status: 204 });
			}
			return json({ snapshots: list });
		}
		const prjCfg = /^projects\/([^/]+)\/agent-config\/files(?:\/(.+))?$/.exec(
			p,
		);
		if (prjCfg) {
			const pid = prjCfg[1] ?? "";
			if (!this.projects.some((x) => x.id === pid))
				return json({ code: "not_found", message: "项目不存在" }, 404);
			const files = this.projectFiles[pid] ?? {};
			this.projectFiles[pid] = files;
			if (prjCfg[2] === undefined)
				return json({
					files: Object.entries(files).map(([path, content]) => ({
						path,
						size: content.length,
						mode: 420,
						sha256: "0".repeat(64),
						updatedAt: NOW - 3_600_000,
					})),
				});
			const fp = decodeURIComponent(prjCfg[2]);
			if (m === "GET")
				return files[fp] === undefined
					? json({ code: "not_found", message: fp }, 404)
					: new Response(files[fp]);
			if (m === "PUT") {
				const text = await req.text();
				files[fp] = text;
				// 与服务端一致：项目层不抽取密钥，形似令牌的明文只给 possible_secret 警告
				const line = text.split("\n").findIndex((l) => /Bearer [^$\s]/.test(l));
				return json({
					file: {
						path: fp,
						size: text.length,
						mode: 420,
						sha256: "0".repeat(64),
						updatedAt: Date.now(),
					},
					extractedSecrets: [],
					warnings:
						line < 0
							? []
							: [
									{
										path: fp,
										line: line + 1,
										kind: "possible_secret",
										message: "looks like a bearer token",
									},
								],
				});
			}
			if (m === "DELETE") {
				delete files[fp];
				return new Response(null, { status: 204 });
			}
		}
		const secMatch = /^projects\/([^/]+)\/secrets(?:\/(.+))?$/.exec(p);
		if (secMatch) {
			const pid = secMatch[1] ?? "";
			if (!this.projects.some((x) => x.id === pid))
				return json({ code: "not_found", message: "项目不存在" }, 404);
			const list = this.projectSecrets.get(pid) ?? [];
			if (secMatch[2] === undefined) return json({ secrets: list });
			const name = decodeURIComponent(secMatch[2]);
			const bad = (message: string) =>
				json({ code: "invalid_request", message }, 400);
			if (m === "PUT") {
				const b = (await req.json()) as Schemas["ProjectSecretPut"];
				const secret = b.secret ?? true;
				// 与服务端 secrets.ValidProjectEntry 同顺序、同文案
				if (b.mode !== "env" && b.mode !== "proxy")
					return bad("mode 必须是 proxy 或 env");
				if (b.mode === "env" && !/^[A-Z_][A-Z0-9_]*$/.test(name))
					return bad(
						`env 模式的密钥名必须匹配 [A-Z_][A-Z0-9_]*：${JSON.stringify(name)}`,
					);
				if (b.mode === "proxy" && name.startsWith("omp/models/"))
					return bad(
						`模型供应商密钥只能在全局密钥里设置：${JSON.stringify(name)}`,
					);
				if (
					b.mode === "proxy" &&
					!/^git\/[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?\/token$/.test(name)
				)
					return bad(
						`项目级 proxy 条目只能是 git/<主机>/token（主机名小写）：${JSON.stringify(name)}`,
					);
				if (b.mode === "proxy" && !secret) return bad("Git 令牌只能保密保存");
				if (!b.value) return bad("值不能为空");
				const prev = list.find((s) => s.name === name);
				const now = Date.now();
				const next: Schemas["ProjectSecret"] = {
					name,
					mode: b.mode,
					secret,
					...(secret ? {} : { value: b.value }),
					createdAt: prev?.createdAt ?? now,
					updatedAt: now,
				};
				this.projectSecrets.set(
					pid,
					[...list.filter((s) => s.name !== name), next].sort((a, b) =>
						a.name < b.name ? -1 : a.name > b.name ? 1 : 0,
					),
				);
				return json(next);
			}
			if (m === "DELETE") {
				if (!list.some((s) => s.name === name))
					return json({ code: "not_found", message: "条目不存在" }, 404);
				this.projectSecrets.set(
					pid,
					list.filter((s) => s.name !== name),
				);
				return new Response(null, { status: 204 });
			}
		}
		const hostedMatch = /^projects\/([^/]+)\/repo\/hosted$/.exec(p);
		if (hostedMatch && m === "POST") {
			const i = this.projects.findIndex((x) => x.id === hostedMatch[1]);
			const cur = this.projects[i];
			if (!cur) return json({ code: "not_found", message: "项目不存在" }, 404);
			if (cur.archivedAt)
				return json({ code: "project_archived", message: "项目已归档" }, 409);
			if (cur.repoUrl || cur.hosted)
				return json({ code: "repo_exists", message: "项目已有仓库" }, 409);
			const next: Project = {
				...cur,
				hosted: true,
				cloneUrl: hostedCloneUrl(cur.id),
				baseBranch: cur.baseBranch || "main",
				updatedAt: Date.now(),
			};
			this.projects[i] = next;
			return json(next, 201);
		}
		// 推送后 Webhook（M24 阶段 ③）：校验同服务端；密钥省略 = 不改、空串 = 清除；DELETE 幂等
		const hookMatch = /^projects\/([^/]+)\/webhook$/.exec(p);
		if (hookMatch && (m === "PUT" || m === "DELETE")) {
			const i = this.projects.findIndex((x) => x.id === hookMatch[1]);
			const cur = this.projects[i];
			if (!cur) return json({ code: "not_found", message: "项目不存在" }, 404);
			if (m === "DELETE") {
				const { webhook: _gone, ...rest } = cur;
				this.projects[i] = rest;
				return new Response(null, { status: 204 });
			}
			const b = (await body()) as Schemas["ProjectWebhookInput"];
			const u = URL.canParse(b.url) ? new URL(b.url) : null;
			if (
				!u ||
				(u.protocol !== "http:" && u.protocol !== "https:") ||
				!u.host ||
				u.username ||
				u.password ||
				b.url.length > 2048
			)
				return json(
					{
						code: "invalid_request",
						message: "url 必须是不带用户名密码的 http(s) 地址",
					},
					400,
				);
			if (!cur.hosted)
				return json(
					{
						code: "repo_not_hosted",
						message: "只有托管仓库的项目能设置 Webhook",
					},
					409,
				);
			const next: Project = {
				...cur,
				webhook: {
					...cur.webhook,
					url: b.url,
					hasSecret:
						b.secret === undefined
							? (cur.webhook?.hasSecret ?? false)
							: b.secret !== "",
				},
			};
			this.projects[i] = next;
			return json(next);
		}
		const projectMatch = /^projects\/([^/]+)$/.exec(p);
		if (projectMatch) {
			const i = this.projects.findIndex((x) => x.id === projectMatch[1]);
			const cur = this.projects[i];
			if (!cur) return json({ code: "not_found", message: "project" }, 404);
			if (m === "PATCH") {
				const { archived, githubListen, ...b } =
					(await body()) as Schemas["ProjectPatch"];
				const bad =
					badGitPair(b.gitAuthorName, b.gitAuthorEmail) ??
					badWarmOrbs(b.warmOrbs);
				if (bad) return bad;
				// M24 阶段 ③：托管项目不能改用外部仓库地址，基线分支不能清空
				if (cur.hosted && b.repoUrl)
					return json(
						{
							code: "repo_hosted",
							message: "托管仓库的项目不能改用外部仓库地址",
						},
						409,
					);
				if (cur.hosted && b.baseBranch === "")
					return json(
						{
							code: "invalid_request",
							message: "托管仓库的基线分支不能清空",
						},
						400,
					);
				const next: Project = { ...cur, updatedAt: Date.now() };
				for (const [k, v] of Object.entries(b)) {
					const key = k as keyof Project;
					if (v === "") delete next[key];
					else Object.assign(next, { [key]: v });
				}
				if (archived !== undefined)
					next.archivedAt = archived ? Date.now() : undefined;
				// M14：仓库地址变了就重新绑定；回流开关合并进绑定
				if (b.repoUrl !== undefined && b.repoUrl !== cur.repoUrl) {
					const gh = this.github.projectGitHub(next.repoUrl);
					if (gh) next.github = gh;
					else delete next.github;
				}
				if (githubListen && next.github)
					next.github = { ...next.github, listen: githubListen };
				this.projects[i] = next;
				return json(next);
			}
			if (m === "DELETE") {
				this.projects.splice(i, 1);
				return new Response(null, { status: 204 });
			}
			return json(cur);
		}
		if (
			p === "settings/agent-identity" ||
			p === "settings/assistant-identity"
		) {
			const key =
				p === "settings/agent-identity" ? "agentIdentity" : "assistantIdentity";
			if (m === "PUT") {
				const b = (await body()) as Schemas["AgentIdentity"];
				const name = identityName(b.name);
				if (name.error)
					return json({ code: "invalid_request", message: name.error }, 400);
				const av = avatarFields(b);
				if ("error" in av) return av.error;
				this[key] = { ...(name.name ? { name: name.name } : {}), ...av.avatar };
			}
			return json(this[key]);
		}
		if (p === "models/catalog") {
			if (url.searchParams.get("refresh") === "true")
				this.catalogFetchedAt = Date.now();
			return json({
				models: MOCK_CATALOG_MODELS,
				fetchedAt: this.catalogFetchedAt,
				stale: false,
			} satisfies Schemas["ModelCatalog"]);
		}
		if (p === "settings/user-identity") {
			if (m === "PUT") {
				const av = avatarFields((await body()) as Schemas["AvatarIdentity"]);
				if ("error" in av) return av.error;
				this.userIdentity = av.avatar;
			}
			return json(this.userIdentity);
		}
		if (p === "settings/role-identities") {
			if (m === "PUT") {
				const b = (await body()) as Schemas["RoleIdentityInput"];
				const role = typeof b.role === "string" ? b.role : "";
				if (!role.trim() || role.length > 128 || CONTROL_CHARS.test(role))
					return json({ code: "invalid_request", message: "role 无效" }, 400);
				const av = avatarFields(b);
				if ("error" in av) return av.error;
				if (!av.avatar.avatarIcon && !av.avatar.avatarImage)
					delete this.roleAvatars[role];
				else if (!MOCK_CYCLE.includes(role))
					return json(
						{ code: "invalid_request", message: "role 不在 cycleOrder 里" },
						400,
					);
				else this.roleAvatars[role] = av.avatar;
				return json(this.roleIdentity(role));
			}
			return json(this.roleIdentities());
		}
		if (p === "settings/agent-avatars") {
			if (m === "PUT") {
				const b = (await body()) as Schemas["AgentAvatarInput"];
				const name = typeof b.name === "string" ? b.name : "";
				if (
					!name.trim() ||
					new TextEncoder().encode(name).length > 128 ||
					CONTROL_CHARS.test(name)
				)
					return json({ code: "invalid_request", message: "name 无效" }, 400);
				const av = avatarFields(b);
				if ("error" in av) return av.error;
				if (!av.avatar.avatarIcon && !av.avatar.avatarImage)
					delete this.agentAvatars[name];
				else if (
					!this.agentAvatarList().agents.some(
						(a) => a.name === name && a.source !== "orphan",
					)
				)
					return json(
						{ code: "invalid_request", message: "没有这个 agent" },
						400,
					);
				else this.agentAvatars[name] = av.avatar;
				return json(
					this.agentAvatarList().agents.find((a) => a.name === name) ?? {
						name,
						source: "orphan",
					},
				);
			}
			return json(this.agentAvatarList());
		}
		if (p === "settings/composer") {
			if (m === "PUT") {
				const b = (await body()) as Schemas["ComposerPrefs"];
				if (b.defaultTarget !== "role" && b.defaultTarget !== "assistant")
					return json(
						{ code: "invalid_request", message: "defaultTarget 无效" },
						400,
					);
				this.composerPrefs = { defaultTarget: b.defaultTarget };
			}
			return json(this.composerPrefs);
		}
		if (p === "settings/selection-actions") {
			if (m === "PUT") {
				const next = normalizeSelectionActions(await body());
				if (typeof next === "string")
					return json({ code: "invalid_request", message: next }, 400);
				this.selectionActions = next;
			}
			return json(this.selectionActions);
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
		const repoRes = await this.repo.handle(req, p, m, url);
		if (repoRes) return repoRes;
		const githubRes = await this.github.handle(p, m, body, url);
		if (githubRes) return githubRes;
		const sshRes = await this.ssh.handle(req, p, m, url);
		if (sshRes) return sshRes;
		const boxRes = await this.boxes.handle(req, p, m, url);
		if (boxRes) return boxRes;
		const memoryRes = await this.memory.handle(p, m, body, url);
		if (memoryRes) return memoryRes;
		const ownerMemoryRes = await this.ownerMemory.handle(p, m, body, url);
		if (ownerMemoryRes) return ownerMemoryRes;
		const runnerRes = await this.handleRunnerFetch(p, m, body);
		if (runnerRes) return runnerRes;
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
				const before = this.files[fp];
				this.files[fp] = await req.text();
				this.noteOmpHomeChange(fp, before);
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
				const before = this.files[fp];
				delete this.files[fp];
				this.noteOmpHomeChange(fp, before);
				return new Response(null, { status: 204 });
			}
		}
		if (p === "agent-config/apply-status" && m === "GET")
			return json(this.applyStatus());
		if (p === "agent-config/restart-sessions" && m === "POST") {
			const now = Date.now();
			for (const entry of this.applyRestarts.values())
				entry.scheduledAt ??= now;
			return json({
				threadIds: [...this.applyRestarts.keys()].sort(),
			});
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
			if (orb.kind === "runner")
				return json({ code: "runner_unsupported", message: "runner" }, 409);
			if (orb.box)
				return json(
					{
						code: "orb_is_box",
						message: "this orb is a box; use /api/v1/boxes",
					},
					409,
				);
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
		if (p === "threads" && m === "GET") {
			const archived = url.searchParams.get("archived") === "true";
			// project=none 只留无项目线程；其它值按项目 id 过滤；不给则不过滤
			const project = url.searchParams.get("project");
			return json({
				threads: [...this.threads.values()].filter(
					(t) =>
						Boolean(t.archivedAt) === archived &&
						(project === null ||
							(project === "none" ? !t.projectId : t.projectId === project)),
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
			const executor = b.executor as Json | undefined;
			if (executor?.kind === "runner")
				return this.createRunnerThread(b, executor);
			if (executor?.kind === "box") {
				const target = this.boxes.threadTarget(
					String(executor.boxId ?? ""),
					typeof executor.app === "string" ? executor.app : undefined,
				);
				if (target instanceof Response) return target;
				const orb = this.orbs.get(target.orbId);
				if (!orb) return json({ code: "box_not_found", message: "box" }, 404);
				const t = this.attachThread(b, orb, { cwd: target.cwd });
				this.boxes.threadsChanged(orb.id);
				return json({ thread: t }, 201);
			}
			const existing = executor?.kind === "orb";
			const desktop = this.desktop.forNewOrb(
				existing ? { browserProfileId: "" } : b,
			);
			if (desktop instanceof Response) return desktop;
			return json({ thread: this.createThread(b, desktop) }, 201);
		}
		const entriesMatch = /^threads\/([^/]+)\/entries$/.exec(p);
		if (entriesMatch && m === "GET") {
			const list = this.entries.get(entriesMatch[1] ?? "");
			if (!list) return json({ code: "not_found", message: "thread" }, 404);
			const q = url.searchParams;
			if (q.has("after") && q.has("before"))
				return json(
					{ code: "invalid_request", message: "after and before" },
					400,
				);
			const limit = Math.min(500, Math.max(1, Number(q.get("limit") ?? 200)));
			const last = list[list.length - 1];
			let page: Entry[];
			let hasMore: boolean;
			if (q.has("before")) {
				// M19 向前翻页：seq < before 的最近 limit 条，升序
				const earlier = list.filter((e) => e.seq < Number(q.get("before")));
				page = earlier.slice(-limit);
				hasMore = earlier.length > limit;
			} else {
				const rest = list.filter((e) => e.seq > Number(q.get("after") ?? 0));
				page = rest.slice(0, limit);
				hasMore = rest.length > limit;
			}
			return json({
				entries: page,
				hasMore,
				lastSeq: last?.seq ?? 0,
				leafEntryId: last?.entryId,
			});
		}
		const threadMatch = /^threads\/([^/]+)(?:\/(fork|wake))?$/.exec(p);
		if (threadMatch) {
			const id = threadMatch[1] ?? "";
			const t = this.threads.get(id);
			if (!t) return json({ code: "not_found", message: "thread" }, 404);
			if (threadMatch[2] === "fork") {
				const b = await body();
				// box 上的线程（M23）：只能共用环境，子线程留在同一台 orb、沿用 cwd
				if (this.boxes.isBoxOrb(t.orbId)) {
					const orb = this.orbs.get(t.orbId ?? "");
					if (b.env === "copy" || !orb)
						return json(
							{
								code: "box_unsupported",
								message:
									"threads on a box cannot be forked with a copied environment; fork with env=share",
							},
							409,
						);
					const child = this.attachThread({ prompt: "" }, orb, {
						title: `fork：${t.title}`,
						parentThreadId: id,
						cwd: t.cwd,
					});
					this.boxes.threadsChanged(orb.id);
					return json({ thread: child }, 201);
				}
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
				if (this.boxes.isStopped(t.orbId) && orb?.state !== "ready")
					return json(
						{
							code: "box_stopped",
							message: "this box is stopped; start it from the Boxes page",
						},
						409,
					);
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
				if (typeof b.unread === "boolean") next.unread = b.unread;
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
				this.boxes.threadsChanged(t.orbId);
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
	 * 模拟控制面投递：绑定到线程并在消息末尾追加 `<oryn-attachments>` 块（路径形如 orb 内的投递位置）；
	 * `extra` 是文件库文件的行（`orynFiles`，见 `MockLibrary.deliver`）。有附件属于别的线程时返回 null（投递失败）。
	 */
	private withAttachments(
		threadId: string,
		message: string,
		ids: unknown,
		extra: string[] = [],
	): string | null {
		const lines: string[] = [];
		for (const id of Array.isArray(ids) ? ids : []) {
			const a = this.attachments.get(String(id));
			if (!a || (a.meta.threadId && a.meta.threadId !== threadId)) return null;
			a.meta = { ...a.meta, threadId };
			lines.push(
				`- /home/user/.oryn/attachments/${threadId}/${a.meta.id}/${a.meta.name} (${a.meta.mime}, ${a.meta.size} bytes)`,
			);
		}
		lines.push(...extra);
		if (lines.length === 0) return message;
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

	// ------------------------------------------------------------ runner（M11b）

	/**
	 * orb 文件 / 改动接口的范围：runner 的 orb 必须带 `thread`（同 orynd 的 orb 代理），范围是线程的工作目录；
	 * box 线程（M23）带 `thread` 时工作目录换成线程的 cwd（`$HOME` 不变）。
	 */
	private runnerScope = (
		orbId: string,
		threadId: string | null,
	): { cwd: string } | { workdir: string } | Response | undefined => {
		const orb = this.orbs.get(orbId);
		if (orb?.box) {
			const t = threadId ? this.threads.get(threadId) : undefined;
			return t?.orbId === orbId && t.cwd ? { workdir: t.cwd } : undefined;
		}
		if (orb?.kind !== "runner") return undefined;
		if (!threadId)
			return json({ code: "thread_required", message: "thread" }, 400);
		const t = this.threads.get(threadId);
		if (!t || t.orbId !== orbId || !t.cwd)
			return json({ code: "invalid_request", message: "thread" }, 400);
		return { cwd: t.cwd };
	};

	/** `runners*` 与 `threads/{id}/worktree/remove`；其它请求返回 null。 */
	private async handleRunnerFetch(
		p: string,
		m: string,
		body: () => Promise<Json>,
	): Promise<Response | null> {
		if (p === "runners" && m === "GET")
			return json({
				runners: this.runners.map((r) => ({
					...r,
					online: this.orbs.get(r.orbId)?.state === "ready",
				})),
			});
		if (p === "runners" && m === "POST") {
			const name = String((await body()).name ?? "").trim();
			if (!name) return json({ code: "invalid_request", message: "name" }, 400);
			if (
				this.runners.some((r) => r.name === name) ||
				providers.some((x) => x.name === name)
			)
				return json(
					{ code: "conflict", message: `名称 ${name} 已被占用` },
					409,
				);
			const seq = (this.idSeq++).toString().padStart(14, "0");
			const runner: Schemas["Runner"] = {
				id: `prov_01JD2M11BMOCK${seq}`,
				name,
				online: false,
				orbId: `orb-01jd2m11bmock${seq}`,
				dirs: [],
				features: [],
				createdAt: Date.now(),
			};
			this.runners.push(runner);
			const orb = runnerOrb(runner, []);
			this.orbs.set(orb.id, orb);
			this.broadcast({ type: "orb", orb });
			return json({ runner, token: "ort_mock_ONLY_SHOWN_ONCE" }, 201);
		}
		const del = /^runners\/([^/]+)$/.exec(p);
		if (del && m === "DELETE") {
			const i = this.runners.findIndex((r) => r.id === del[1]);
			const runner = this.runners[i];
			if (!runner) return json({ code: "not_found", message: "runner" }, 404);
			this.runners.splice(i, 1);
			const orb = this.orbs.get(runner.orbId);
			if (orb) {
				const next: Orb = {
					...orb,
					state: "deleted",
					stateChangedAt: Date.now(),
					deletedAt: Date.now(),
				};
				this.orbs.set(orb.id, next);
				this.broadcast({ type: "orb", orb: next });
			}
			// 线程保留转录，`orbId` 置空（只读）
			for (const t of [...this.threads.values()])
				if (t.orbId === runner.orbId)
					this.upsertThread({ ...t, orbId: undefined, updatedAt: Date.now() });
			return new Response(null, { status: 204 });
		}
		const wt = /^threads\/([^/]+)\/worktree\/remove$/.exec(p);
		if (wt && m === "POST") {
			const t = this.threads.get(wt[1] ?? "");
			if (!t) return json({ code: "not_found", message: "thread" }, 404);
			if (!t.worktree)
				return json({ code: "conflict", message: "no worktree" }, 409);
			if (this.orbs.get(t.orbId ?? "")?.state !== "ready")
				return json({ code: "runner_offline", message: "runner" }, 409);
			this.upsertThread({ ...t, worktree: undefined, updatedAt: Date.now() });
			return new Response(null, { status: 204 });
		}
		return null;
	}

	/**
	 * 在 runner 上新建线程（同 internal/threads 的 runner 分支）：runner 须在线、`dir` 须在声明的目录之内；
	 * 没要 worktree、没给 `shareDir` 而目录已被未归档线程占用 → 409 `dir_in_use`；要 worktree 时目录取 `<repo>-<name>`（重名加 `-2`…）。
	 */
	private createRunnerThread(b: Json, executor: Json): Response {
		const runner = this.runners.find((r) => r.id === executor.runnerId);
		if (!runner) return json({ code: "not_found", message: "runner" }, 404);
		const orb = this.orbs.get(runner.orbId);
		if (orb?.state !== "ready")
			return json({ code: "runner_offline", message: runner.name }, 409);
		const dir = String(executor.dir ?? "");
		const served = runner.dirs.find(
			(d) => dir === d.path || dir.startsWith(`${d.path}/`),
		);
		if (!dir.startsWith("/") || !served || dir.split("/").includes(".."))
			return json({ code: "dir_not_served", message: dir }, 400);
		const open = [...this.threads.values()].filter(
			(t) => !t.archivedAt && t.orbId === orb.id,
		);
		let cwd = dir;
		let worktree: Thread["worktree"];
		const wt = executor.worktree as Json | undefined;
		if (wt) {
			const name = String(wt.name ?? "");
			if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$/.test(name))
				return json(
					{
						code: "worktree_failed",
						message: `fatal: '${name}' is not a valid branch name`,
					},
					400,
				);
			if (!(served.git && served.path === dir))
				return json(
					{
						code: "worktree_failed",
						message:
							"fatal: not a git repository (or any of the parent directories): .git",
					},
					400,
				);
			let n = 1;
			const taken = (d: string) => open.some((t) => t.cwd === d);
			while (taken(n === 1 ? `${dir}-${name}` : `${dir}-${name}-${n}`)) n++;
			const branch = n === 1 ? name : `${name}-${n}`;
			cwd = `${dir}-${branch}`;
			worktree = { repo: dir, dir: cwd, branch };
		} else if (executor.shareDir !== true) {
			const users = open.filter((t) => t.cwd === dir).map((t) => t.id);
			if (users.length > 0)
				return json(
					{
						code: "dir_in_use",
						message: `${dir} 已是其它线程的工作目录`,
						threadIds: users,
					},
					409,
				);
		}
		return json({ thread: this.attachThread(b, orb, { cwd, worktree }) }, 201);
	}

	/** 在已有的常驻 orb（runner、box）上建线程：不建 orb、不跑 setup，agent 直接启动。 */
	private attachThread(b: Json, orb: Orb, extra: Partial<Thread>): Thread {
		const id = `T-mock-${(this.idSeq++).toString(16)}`;
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
			orbId: orb.id,
			projectId: typeof b.projectId === "string" ? b.projectId : undefined,
			...extra,
		};
		this.entries.set(id, []);
		this.states.set(id, {
			...mainState(),
			todoPhases: [],
			contextUsage: { tokens: 0, contextWindow: 200_000, percent: 0 },
		});
		const next = { ...orb, threadIds: [...orb.threadIds, id] };
		this.orbs.set(orb.id, next);
		this.broadcast({ type: "orb", orb: next });
		this.upsertThread(t);
		void (async () => {
			await sleep(this.delay * 4);
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
				entries: batch.map((e) => this.publicEntry(share, e)),
				leafEntryId: scope.leaf,
				lastSeq: end ? scope.until : (batch[batch.length - 1]?.seq ?? 0),
			});
		}
	}

	/** 把线程消息转成匿名连接能收的形态（不带 threadId；帧只放白名单内的类型）。 */
	private toPublic(threadId: string, msg: Json) {
		for (const [s, sub] of this.publicSockets) {
			if (sub.threadId !== threadId) continue;
			const share = this.shares.get(sub.shareId);
			if (!share) continue;
			switch (msg.type) {
				case "entries":
					s.deliver({
						type: "entries",
						entries: (msg.entries as Entry[]).map((e) =>
							this.publicEntry(share, e),
						),
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
		this.artifacts.drop(socket);
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

	/** 追加一条主会话条目（任意类型）并推送；叶子移到它。 */
	private appendRaw(
		threadId: string,
		type: string,
		body: Json,
		ts = Date.now(),
	) {
		const list = this.entries.get(threadId) ?? [];
		const last = [...list].reverse().find((e) => !e.entryId.startsWith("__"));
		const id = (this.idSeq++).toString(16).padStart(8, "0");
		const e: Entry = {
			seq: (list[list.length - 1]?.seq ?? 0) + 1,
			file: SESSION_FILE,
			entryId: id,
			parentId: last?.entryId,
			type,
			ts,
			payload: {
				type,
				id,
				parentId: last?.entryId ?? null,
				timestamp: new Date(ts).toISOString(),
				...body,
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

	private appendEntry(threadId: string, message: Json) {
		this.appendRaw(
			threadId,
			"message",
			{ message },
			typeof message.timestamp === "number" ? message.timestamp : Date.now(),
		);
	}

	/** 回合里助手消息的模型字段：取线程当前的模型（切换后的新回合跑在新模型上）。 */
	private turnModel(threadId: string): Json {
		const m = this.states.get(threadId)?.model;
		return m ? { provider: m.provider, model: m.id } : {};
	}

	onClient(socket: MockSocket, msg: Json) {
		const threadId = typeof msg.threadId === "string" ? msg.threadId : "";
		switch (msg.type) {
			case "ping":
				socket.deliver({ type: "pong" });
				return;
			case "focus":
				return;
			case "artifact.watch":
				this.artifacts.watch(socket, msg.id);
				return;
			case "artifact.unwatch":
				this.artifacts.unwatch(socket, msg.id);
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
				let after = typeof msg.afterSeq === "number" ? msg.afterSeq : 0;
				// M19 sub.tail：从 seq 0 补发时只补最近 tail 条（夹到 50–2000）
				let windowStart = 0;
				if (after === 0 && typeof msg.tail === "number") {
					const tail = Math.min(2000, Math.max(50, msg.tail));
					if (list.length >= tail)
						windowStart = (list[list.length - tail]?.seq ?? 1) - 1;
					after = windowStart;
				}
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
						composerPrediction: this.predictions.get(threadId),
					},
					leafEntryId: last?.entryId,
					lastSeq: last?.seq ?? 0,
					transcriptRev: 0,
					fullReplay: false,
					...(windowStart > 0 ? { windowStart } : {}),
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
			case "cmd": {
				const t = this.threads.get(threadId);
				const frame = msg.frame as Json;
				// 停着的 box 上的线程（M23）：消息不受理，同 orynd 的 boxStoppedLocked
				if (
					frame?.type === "prompt" &&
					this.boxes.isStopped(t?.orbId) &&
					this.orbs.get(t?.orbId ?? "")?.state !== "ready"
				) {
					socket.deliver({
						type: "error",
						code: "box_stopped",
						message: "this box is stopped; start it from the Boxes page",
						threadId,
						id: String(msg.id),
					});
					return;
				}
				void this.handleCmd(socket, threadId, String(msg.id), frame);
				return;
			}
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

	/** 改 btw 记录最新一轮（有追问时是 followUps 末尾），存回并推 `btw_record`。 */
	private patchBtw(
		threadId: string,
		recordId: string,
		patch: Partial<Pick<RpcBtwHistoryRecord, "answer" | "status">>,
		push = true,
	): void {
		const list = this.btwRecords.get(threadId) ?? [];
		const i = list.findIndex((r) => r.id === recordId);
		const prev = list[i];
		if (!prev) return;
		const updatedAt = Date.now();
		const last = prev.followUps?.at(-1);
		const next: RpcBtwHistoryRecord =
			prev.followUps && last
				? {
						...prev,
						updatedAt,
						followUps: [
							...prev.followUps.slice(0, -1),
							{ ...last, ...patch, updatedAt },
						],
					}
				: { ...prev, ...patch, updatedAt };
		list[i] = next;
		if (push) this.frame(threadId, { type: "btw_record", record: next });
	}

	/** `btw`：同一线程同时只答一个；回答切成 4 段，每隔 `oryn.mock.btw-delay` 毫秒（缺省 120）推一段。 */
	private btwAsk(
		socket: MockSocket,
		threadId: string,
		id: string,
		frame: Json,
	) {
		if (this.btwTimers.has(threadId))
			return this.respond(
				socket,
				threadId,
				id,
				"btw",
				undefined,
				"A /btw question is still running; cancel it first",
			);
		const question = String(frame.question ?? "").trim();
		const list = this.btwRecords.get(threadId) ?? [];
		this.btwRecords.set(threadId, list);
		const now = Date.now();
		const turn = {
			question,
			answer: "",
			status: "running" as const,
			createdAt: now,
			updatedAt: now,
		};
		const prev =
			typeof frame.recordId === "string"
				? list.find((r) => r.id === frame.recordId)
				: undefined;
		let record: RpcBtwHistoryRecord;
		if (prev) {
			record = {
				...prev,
				updatedAt: now,
				followUps: [...(prev.followUps ?? []), turn],
			};
			list[list.indexOf(prev)] = record;
		} else {
			this.btwSeq += 1;
			record = { id: `mock-btw-${this.btwSeq}`, leafId: null, ...turn };
			list.push(record);
		}
		this.frame(threadId, { type: "btw_record", record });
		this.respond(socket, threadId, id, "btw", { record });
		const parsed = parseSideQuestion(question);
		const answer = parsed
			? `模拟回答（${parsed.action}）：${Array.from(parsed.selected).slice(0, 40).join("")}`
			: `模拟回答：${question}`;
		const chars = Array.from(answer);
		const size = Math.ceil(chars.length / 4);
		const chunks = [0, 1, 2, 3]
			.map((k) => chars.slice(k * size, (k + 1) * size).join(""))
			.filter(Boolean);
		const delay = Number(localStorage.getItem("oryn.mock.btw-delay") ?? 120);
		const recordId = record.id;
		let sent = "";
		const timer = setInterval(() => {
			const delta = chunks.shift();
			if (delta !== undefined) {
				sent += delta;
				this.patchBtw(threadId, recordId, { answer: sent }, false);
				this.frame(threadId, { type: "btw_delta", recordId, delta });
				return;
			}
			clearInterval(timer);
			this.btwTimers.delete(threadId);
			this.patchBtw(threadId, recordId, { status: "complete" });
		}, delay);
		this.btwTimers.set(threadId, { recordId, timer });
	}

	private btwCancel(socket: MockSocket, threadId: string, id: string) {
		const running = this.btwTimers.get(threadId);
		if (running) {
			clearInterval(running.timer);
			this.btwTimers.delete(threadId);
			this.patchBtw(threadId, running.recordId, { status: "cancelled" });
		}
		this.respond(socket, threadId, id, "btw_cancel", {
			cancelled: running !== undefined,
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
		// 旧 Core：不认识 Oryn 的命令（同 omp 的 `Unknown command: <type>`，没有 code）
		if (type.startsWith("oryn_") && this.legacyThreads.has(threadId))
			return this.respond(
				socket,
				threadId,
				id,
				type,
				undefined,
				`Unknown command: ${type}`,
			);
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
				return this.respond(socket, threadId, id, type, {
					records: [...(this.btwRecords.get(threadId) ?? [])].sort(
						(a, b) => b.createdAt - a.createdAt,
					),
				});
			case "btw":
				return this.btwAsk(socket, threadId, id, frame);
			case "btw_cancel":
				return this.btwCancel(socket, threadId, id);
			case "btw_inject":
				return this.respond(socket, threadId, id, type, {
					sent: false,
					reason: "mock",
				});
			// 角色模型（Ctrl+P 轮换、模型对话框的角色列表）：照主人的真实配置，default 用 @ 引用 Cyrene，writer 是裸 id（无 selected）
			case "get_settings":
				return this.respond(socket, threadId, id, type, {
					values: {
						cycleOrder: MOCK_CYCLE,
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
						role("default", "Default", MOCK_ROLE_SELECTORS.default, {
							tag: "DEFAULT",
						}),
						role("smol", "Fast", MOCK_ROLE_SELECTORS.smol, { tag: "SMOL" }),
						role("slow", "Thinking", MOCK_ROLE_SELECTORS.slow, {
							tag: "SLOW",
						}),
						role("commit", "Commit", MOCK_ROLE_SELECTORS.commit, {
							tag: "COMMIT",
						}),
						role("web", "Web search", MOCK_ROLE_SELECTORS.web, {
							tag: "WEB",
							section: "kind",
						}),
						...MOCK_CYCLE.map((r) => role(r, r, MOCK_ROLE_SELECTORS[r])),
						// 裸 id 带级别：omp 不给 selected，由网页端照 resolveModelRoleValue 解析
						role("writer", "writer", MOCK_ROLE_SELECTORS.writer),
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
				// 与真 Core 一致：RPC set_model 记成 default role，且没有来源记录
				this.appendRaw(threadId, "model_change", {
					model: `${m.provider}/${m.id}`,
					role: "default",
				});
				return this.respond(socket, threadId, id, type, m);
			}
			// 新 Core 的选择命令（M9.6）：切模型的同时写 model_change 与来源记录
			case "oryn_select_model": {
				const role = typeof frame.role === "string" ? frame.role : undefined;
				const provider =
					typeof frame.provider === "string" ? frame.provider : "";
				const modelId = typeof frame.modelId === "string" ? frame.modelId : "";
				const fail = (error: string) =>
					this.respond(socket, threadId, id, type, undefined, error);
				let pick: MockRolePick | undefined;
				if (role && !provider && !modelId) {
					pick = resolveMockRole(role);
					if (!pick) return fail(`Role has no available model: ${role}`);
				} else if (provider && modelId && role === undefined) {
					const m = MOCK_ROLE_MODELS.find(
						(x) => x.provider === provider && x.id === modelId,
					);
					if (!m) return fail(`Model not found: ${provider}/${modelId}`);
					pick = { model: m };
				} else if ((provider || modelId) && role === undefined)
					return fail("provider and modelId must be given together");
				else
					return fail(
						"oryn_select_model requires either role or provider+modelId",
					);
				const key = `${pick.model.provider}/${pick.model.id}`;
				const level = pick.level as SessionState["thinkingLevel"] | undefined;
				this.states.set(threadId, {
					...state,
					model: {
						...pick.model,
						api: "mock",
					} as unknown as SessionState["model"],
					...mockSpeedCaps(pick.model.provider),
					...(level
						? {
								thinkingLevel: level,
								thinkingConfigured: level as SessionState["thinkingConfigured"],
							}
						: {}),
				});
				const source = role ? "role" : "manual";
				const named = {
					...(role ? { role } : {}),
					...(pick.alias ? { alias: pick.alias } : {}),
				};
				this.appendRaw(threadId, "model_change", {
					model: key,
					role: role ?? "default",
				});
				this.appendRaw(threadId, "custom", {
					customType: MODEL_SOURCE_TYPE,
					data: { v: 1, source, model: key, ...named },
				});
				return this.respond(socket, threadId, id, type, {
					model: { ...pick.model, api: "mock" },
					thinkingLevel: this.states.get(threadId)?.thinkingLevel,
					source,
					...named,
				});
			}
			// 只允许控制面自己发（同 orynd 的 managedCommands）
			case "oryn_mark_session_start":
				return this.respond(
					socket,
					threadId,
					id,
					type,
					undefined,
					"managed_by_oryn",
				);
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
				const files = this.library.deliver(threadId, frame.orynFiles);
				const message =
					files &&
					this.withAttachments(
						threadId,
						String(frame.message ?? ""),
						frame.orynAttachments,
						files,
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
			// runner 那台机器自己的 SSH 主机（M18）；orb 线程的同名命令在真实控制面里被拦或直通，mock 不区分
			case "get_ssh_hosts":
			case "ssh_manage":
			case "ssh_test":
				try {
					return this.respond(
						socket,
						threadId,
						id,
						type,
						this.ssh.runnerCmd(type, frame),
					);
				} catch (e) {
					return this.respond(
						socket,
						threadId,
						id,
						type,
						undefined,
						e instanceof Error ? e.message : String(e),
					);
				}
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
		// 同控制面：新一轮开始时作废缓存的输入框预测
		this.predictions.delete(threadId);
		this.desktop.turn(t.orbId, true);
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
		// 助手对话：omp 随这一轮注入 xd:// 设备挂载提示（display: false），模型首个 token 前它是最后一条在途消息
		if (t.kind === "assistant") {
			const notice = xdevMountNotice(Date.now());
			const nid = `m${this.idSeq++}`;
			this.frame(threadId, {
				type: "message_start",
				messageId: nid,
				message: notice,
			});
			this.frame(threadId, {
				type: "message_end",
				messageId: nid,
				message: notice,
			});
			await sleep(Math.max(d, 2500));
		}

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
			message: assistant([], ts1, {
				stopReason: undefined,
				...this.turnModel(threadId),
			}),
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
			this.turnModel(threadId),
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
		// 提示词含「桌面」：agent 先操作桌面几秒，再用 desktop_ask_user 请用户登录（同真实扩展的 select 形态）
		if (/桌面|desktop/i.test(text)) {
			await sleep(4000);
			const reqId = `ui-${this.idSeq++}`;
			const reason = "在桌面的浏览器里登录 GitHub 测试号（含二次验证）";
			const req = {
				type: "extension_ui_request",
				id: reqId,
				method: "select",
				title: desktopAskTitle("sign_in", reason),
				options: [DESKTOP_ASK_DONE, DESKTOP_ASK_SKIP],
			};
			this.pendingUi.set(threadId, [
				...(this.pendingUi.get(threadId) ?? []),
				req,
			]);
			this.frame(threadId, req);
			this.desktop.setAsk(t.orbId, {
				threadId,
				requestId: reqId,
				kind: "sign_in",
				reason,
			});
			this.refreshThreadState(threadId);
			await this.waitFor(reqId);
			this.desktop.setAsk(t.orbId, undefined);
			await sleep(2000);
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
			message: assistant([], ts2, {
				stopReason: undefined,
				...this.turnModel(threadId),
			}),
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
			...this.turnModel(threadId),
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
		this.desktop.turn(t.orbId, false);
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
		// 同 omp `composer.predictions`：一轮完成后再发一次请求预测下一条消息（这里固定给 4 个候选，
		// 首条是第二条的前缀，便于在开发模式下试前缀匹配与 Tab 轮换）
		await sleep(d * 4);
		if (this.threads.get(threadId)?.state !== "idle") return;
		const choices = [
			"再看一下磁盘占用",
			"再看一下磁盘占用和内存",
			"把结果整理成表格",
			"接着跑一遍测试",
		];
		this.predictions.set(threadId, choices);
		this.frame(threadId, { type: "oryn_composer_prediction", choices });
	}
}
