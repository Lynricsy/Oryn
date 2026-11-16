/**
 * mock 后端的 Artifacts（M14）：`/api/v1/artifacts*` 与 `/api/v1/artifact-frames/*`，响应形状与 orynd
 * （`internal/api/artifacts.go`）一致。夹具是四种类型各一个（单文件），外加一个在「最近删除」里的页面。
 * - 帧（`POST …/frames`）回一个 `blob:` 地址：页面 HTML 里用 `<script type="module" src="<origin>/sandbox/runtime/main.ts">`
 *   引入运行时（vite 开发服务器直接提供），随后调用 `__orynStart(<配置>)`。blob 是申请帧那一刻的快照：
 *   新版本到来时帧会照常重载，但要点「重新加载」（重新申请帧）才看得到新内容。
 * - 「在浏览器中打开」回一个外壳页的 `blob:` 地址，外壳里用沙箱 iframe 装内容，不让 artifact 的脚本跑在应用的源上。
 * - 观看者（`artifact.watch`）按连接登记；`write` 产生新版本并推 `artifact.version` 与 `artifacts.changed`。
 * - 页面帧给每个起始标签加 `data-oryn-src="<mark>:<字节偏移>"`（同 `htmlsrc.MarkSources`），评论与选择拿得到源码偏移。
 * - 评论（`…/comments*`）存在内存里，变化时推 `artifact.comments`；不在新版本上重新定位锚点。
 * - 画布（`…/canvas`、`…/canvas/ops`）：操作广播 `artifact.canvas`，静默 30 秒落成用户版本；画布帧的地址是
 *   `<origin>/__mock_af/<令牌>/`，画框文件（`_oryn/v/<n>/<路径>`）由安装时拦截 iframe `src` 的 `resolveFrameUrl` 换成 blob。
 * `MockServer.handleFetch` 调 `handle`，不归这里的请求返回 null。
 */
import type { Schemas, Thread } from "../api/client";
import { BLANK_PAGE } from "../components/artifacts/canvas/frames";
import {
	applyCanvasOps,
	type CanvasDoc,
	type CanvasOp,
	parseCanvasDoc,
	parseCanvasOps,
	stringifyCanvasDoc,
} from "../lib/artifacts/canvas-doc";
import { cleanStyle, sourceTextAt } from "../lib/artifacts/editable";
import { coerceParamValue, parseParamsDecl } from "../lib/artifacts/params";
import { MockArtifactActivity } from "./artifact-activity";
import { MockArtifactPublish } from "./artifact-publish";
import {
	ARTIFACT_IDS,
	threads as fixtureThreads,
	MAIN_THREAD,
	NOW,
	projects,
	TOOL_ZOO_THREAD,
} from "./fixtures";
import type { MockSocket } from "./server";

type Artifact = Schemas["Artifact"];
type ArtifactKind = Schemas["ArtifactKind"];
type ArtifactCapability = Schemas["ArtifactCapability"];

/** 用户画布操作静默多久后落成版本（同 `artifacts.CanvasQuiet`）。 */
const CANVAS_QUIET_MS = 30_000;

/** 一个画布的实时状态：底稿版本、当前文档、待复制的画框文件（新文件 → 来源文件）。 */
interface MockCanvasLive {
	version: number;
	doc: CanvasDoc;
	copies: Map<string, string>;
	timer: ReturnType<typeof setTimeout> | undefined;
}

const MIN = 60_000;
const DAY = 86_400_000;
const MAX_FILE_BYTES = 20 << 20;
const MAX_VERSION_FILES = 256;
const MAX_TITLE_RUNES = 120;

const MIME_BY_EXT: Record<string, string> = {
	html: "text/html; charset=utf-8",
	htm: "text/html; charset=utf-8",
	css: "text/css; charset=utf-8",
	js: "text/javascript; charset=utf-8",
	mjs: "text/javascript; charset=utf-8",
	json: "application/json",
	svg: "image/svg+xml",
	png: "image/png",
	jpg: "image/jpeg",
	jpeg: "image/jpeg",
	gif: "image/gif",
	webp: "image/webp",
	md: "text/markdown; charset=utf-8",
	txt: "text/plain; charset=utf-8",
	csv: "text/csv; charset=utf-8",
};

function mimeOf(path: string): string {
	const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
	return MIME_BY_EXT[ext] ?? "application/octet-stream";
}

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

/** JSON 请求体里的一个字段；体不是 JSON 对象或没有该字段时为 undefined。 */
async function bodyField(req: Request, key: string): Promise<unknown> {
	let body: unknown;
	try {
		body = await req.json();
	} catch {
		return undefined;
	}
	return body && typeof body === "object" ? Reflect.get(body, key) : undefined;
}

const encoder = new TextEncoder();

function escapeHtml(s: string): string {
	return s
		.replace(/&/g, "&amp;")
		.replace(/</g, "&lt;")
		.replace(/>/g, "&gt;")
		.replace(/"/g, "&quot;");
}

/** 内容的伪 sha256：64 位十六进制，同内容同值（只用来比较文件是否变过）。 */
function pseudoSha(data: Uint8Array): string {
	let h1 = 0x811c9dc5;
	let h2 = 0x01000193;
	for (const b of data) {
		h1 = Math.imul(h1 ^ b, 0x01000193) >>> 0;
		h2 = Math.imul(h2 ^ b, 0x5bd1e995) >>> 0;
	}
	const hex = (h1.toString(16) + h2.toString(16)).padStart(16, "0");
	return hex.repeat(4);
}

let crcTable: Uint32Array | null = null;

function crc32(data: Uint8Array): number {
	if (!crcTable) {
		crcTable = new Uint32Array(256);
		for (let n = 0; n < 256; n++) {
			let c = n;
			for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
			crcTable[n] = c >>> 0;
		}
	}
	let crc = 0xffffffff;
	for (const b of data) crc = (crcTable[(crc ^ b) & 0xff] ?? 0) ^ (crc >>> 8);
	return (crc ^ 0xffffffff) >>> 0;
}

/** 不压缩（stored）的 zip，文件名按 UTF-8（通用位 11）：导出 `format=zip` 用。 */
function storedZip(files: [string, Uint8Array][]): Uint8Array<ArrayBuffer> {
	const parts: Uint8Array[] = [];
	const central: Uint8Array[] = [];
	let offset = 0;
	for (const [path, data] of files) {
		const name = encoder.encode(path);
		const crc = crc32(data);
		const local = new DataView(new ArrayBuffer(30));
		local.setUint32(0, 0x04034b50, true);
		local.setUint16(4, 20, true);
		local.setUint16(6, 0x0800, true);
		local.setUint32(14, crc, true);
		local.setUint32(18, data.byteLength, true);
		local.setUint32(22, data.byteLength, true);
		local.setUint16(26, name.byteLength, true);
		parts.push(new Uint8Array(local.buffer), name, data);
		const dir = new DataView(new ArrayBuffer(46));
		dir.setUint32(0, 0x02014b50, true);
		dir.setUint16(4, 20, true);
		dir.setUint16(6, 20, true);
		dir.setUint16(8, 0x0800, true);
		dir.setUint32(16, crc, true);
		dir.setUint32(20, data.byteLength, true);
		dir.setUint32(24, data.byteLength, true);
		dir.setUint16(28, name.byteLength, true);
		dir.setUint32(42, offset, true);
		central.push(new Uint8Array(dir.buffer), name);
		offset += 30 + name.byteLength + data.byteLength;
	}
	const dirSize = central.reduce((n, p) => n + p.byteLength, 0);
	const end = new DataView(new ArrayBuffer(22));
	end.setUint32(0, 0x06054b50, true);
	end.setUint16(8, files.length, true);
	end.setUint16(10, files.length, true);
	end.setUint32(12, dirSize, true);
	end.setUint32(16, offset, true);
	const all = [...parts, ...central, new Uint8Array(end.buffer)];
	const out = new Uint8Array(all.reduce((n, p) => n + p.byteLength, 0));
	let at = 0;
	for (const p of all) {
		out.set(p, at);
		at += p.byteLength;
	}
	return out;
}

/** 路径规则（同 `artifacts.PathProblem`）；返回空串表示合法。 */
function pathProblem(path: string): string {
	if (!path || encoder.encode(path).byteLength > 512) return "长度";
	if (path.startsWith("/") || /[\\:*?"<>|]/.test(path)) return "字符";
	for (let i = 0; i < path.length; i++)
		if (path.charCodeAt(i) < 0x20 || path.charCodeAt(i) === 0x7f)
			return "控制字符";
	for (const seg of path.split("/")) {
		if (!seg || seg === "." || seg === "..") return "空段";
		if (encoder.encode(seg).byteLength > 255) return "段长";
		if (/[. ]$/.test(seg)) return "段尾";
	}
	return path.startsWith("_oryn") ? "保留前缀" : "";
}

interface StoredFile {
	data: Uint8Array<ArrayBuffer>;
	sha256: string;
}

interface StoredVersion {
	meta: Schemas["ArtifactVersion"];
	files: Map<string, StoredFile>;
	capabilities: ArtifactCapability[];
}

interface Stored {
	meta: Artifact;
	versions: StoredVersion[];
}

interface Seed {
	id: string;
	kind: ArtifactKind;
	title: string;
	threadId?: string;
	projectId?: string;
	path: string;
	/** 各版本的内容（从 v1 起）与备注。 */
	versions: { text: string; note?: string; author?: string; at: number }[];
	/** 每个版本都带的其它文件（路径 → 内容）。 */
	files?: Record<string, string>;
	openedAt?: number;
	deletedAt?: number;
}

function page(title: string, body: string): string {
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${title}</title>
<style>
body { font: 15px/1.6 system-ui, sans-serif; margin: 0; padding: 32px; color: #18181b; background: #fafafa; }
h1 { font-size: 28px; margin: 0 0 8px; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); gap: 12px; margin: 24px 0; }
.card { background: #fff; border: 1px solid #e4e4e7; border-radius: 12px; padding: 16px; }
.card b { display: block; font-size: 24px; }
@media (prefers-color-scheme: dark) {
  body { color: #fafafa; background: #0f0f11; }
  .card { background: #1c1c1f; border-color: #2e2e33; }
}
</style>
</head>
<body>
${body}
</body>
</html>
`;
}

function habitPage(days: number[]): string {
	const cards = ["阅读", "跑步", "早睡", "写作"]
		.map(
			(name, i) => `<div class="card">${name}<b>${days[i] ?? 0} / 7</b></div>`,
		)
		.join("\n");
	return page(
		"本周习惯打卡",
		`<h1>本周习惯打卡</h1>
<p>11 月 10 日 – 11 月 16 日</p>
<div class="grid">
${cards}
</div>
<p>数据来源见 <a href="https://example.com/habits">example.com</a>。</p>
${Array.from({ length: 30 }, (_, i) => `<p>第 ${i + 1} 条记录：保持节奏。</p>`).join("\n")}`,
	);
}

const SEEDS: Seed[] = [
	{
		id: ARTIFACT_IDS.web,
		kind: "web",
		title: "本周习惯打卡",
		threadId: TOOL_ZOO_THREAD,
		projectId: projects[0]?.id,
		path: "index.html",
		versions: [
			{ text: habitPage([3, 2, 5, 1]), at: NOW - 40 * MIN },
			{
				text: habitPage([4, 3, 5, 2]),
				note: "补上周四的数据",
				at: NOW - 30 * MIN,
			},
			{ text: habitPage([3, 2, 5, 1]), note: "恢复到 v1", at: NOW - 20 * MIN },
		],
		openedAt: NOW - 10 * MIN,
	},
	{
		id: ARTIFACT_IDS.document,
		kind: "document",
		title: "重连机制设计说明",
		threadId: MAIN_THREAD,
		projectId: projects[0]?.id,
		path: "document.md",
		versions: [
			{
				text: "# 重连机制设计说明\n\n断线后按指数退避重连：0.5 秒起步，每次翻倍，15 秒封顶。\n\n| 次数 | 等待 |\n| --- | --- |\n| 1 | 0.5s |\n| 2 | 1s |\n| 3 | 2s |\n\n```ts\nexport function backoffMs(attempt: number): number {\n\treturn Math.min(15_000, 500 * 2 ** attempt);\n}\n```\n",
				at: NOW - 3 * 60 * MIN,
			},
		],
	},
	{
		id: ARTIFACT_IDS.slides,
		kind: "slides",
		title: "M14 进度汇报",
		threadId: MAIN_THREAD,
		projectId: projects[0]?.id,
		path: "slides.json",
		versions: [
			{
				text: `${JSON.stringify(
					{
						title: "M14 进度汇报",
						theme: "light",
						aspect: "16:9",
						slides: [
							{ layout: "title", title: "M14 Artifacts", subtitle: "进度汇报" },
							{
								layout: "bullets",
								title: "已完成",
								bullets: ["存储与版本", "agent 工具", "沙箱帧"],
							},
							{ layout: "stats", stats: [{ value: "43", label: "能力项" }] },
						],
					},
					null,
					2,
				)}\n`,
				at: NOW - 5 * 60 * MIN,
			},
		],
	},
	{
		id: ARTIFACT_IDS.canvas,
		kind: "canvas",
		title: "首页改版草图",
		path: "canvas.json",
		versions: [
			{
				text: `${JSON.stringify(
					{
						format: 1,
						canvases: [{ id: "c1", name: "首页" }],
						pages: [
							{
								id: "hero",
								canvas: "c1",
								name: "首屏",
								file: "page-hero.html",
								x: 0,
								y: 0,
								width: 1280,
								height: 800,
							},
						],
						notes: [
							{
								id: "n1",
								canvas: "c1",
								kind: "sticky",
								x: 1340,
								y: 40,
								text: "标题再大一点",
							},
						],
					},
					null,
					2,
				)}\n`,
				at: NOW - 2 * DAY,
			},
		],
		files: {
			"page-hero.html": page(
				"首屏",
				`<script type="application/json" data-oryn-params>
{
  "accent": {"type": "color", "value": "#2563eb", "label": "强调色"},
  "title-size": {"type": "number", "value": 28, "min": 16, "max": 64, "step": 2, "unit": "px", "label": "标题字号"},
  "dark": {"type": "boolean", "value": false, "label": "深色"}
}
</script>
<style>h1 { color: var(--accent); font-size: var(--title-size); } [data-dark="true"] body { background: #18181b; color: #fafafa; }</style>
<h1>把习惯变成日常</h1><p>每天一分钟，记录你的坚持。</p>`,
			),
		},
	},
	{
		id: ARTIFACT_IDS.trashed,
		kind: "web",
		title: "旧版落地页",
		threadId: TOOL_ZOO_THREAD,
		path: "index.html",
		versions: [
			{
				text: page("旧版落地页", "<h1>旧版落地页</h1><p>已被新版替代。</p>"),
				at: NOW - 5 * DAY,
			},
		],
		deletedAt: NOW - 3 * DAY,
	},
];

/** 正文里不算元素的原文：其中的 `<` 不是起始标签。 */
const RAW_TEXT: Record<string, true> = {
	script: true,
	style: true,
	textarea: true,
	title: true,
};

/** 给每个起始标签加源码标记，偏移是该标签 `<` 在原文里的 UTF-8 字节偏移（同 `htmlsrc.MarkSources` 的口径）。 */
function markSources(html: string, mark: string): string {
	const re = /<!--[\s\S]*?-->|<![^>]*>|<\/[^>]*>|<([a-zA-Z][a-zA-Z0-9-]*)/g;
	let out = "";
	let cursor = 0;
	let byte = 0;
	let counted = 0;
	for (let m = re.exec(html); m; m = re.exec(html)) {
		const name = m[1]?.toLowerCase();
		if (!name) continue;
		byte += encoder.encode(html.slice(counted, m.index)).byteLength;
		counted = m.index;
		const end = m.index + m[0].length;
		out += `${html.slice(cursor, end)} data-oryn-src="${mark}:${byte}"`;
		cursor = end;
		if (RAW_TEXT[name]) {
			const close = html.toLowerCase().indexOf(`</${name}`, end);
			re.lastIndex = close < 0 ? html.length : close;
		}
	}
	return out + html.slice(cursor);
}

type CommentThread = Schemas["ArtifactCommentThread"];
const MAX_COMMENT_RUNES = 4000;

/** 观看者消息（`artifact.version` 等）与 `artifacts.changed` 的出口。 */
export interface ArtifactsHub {
	changed(): void;
}

export class MockArtifacts {
	private readonly items = new Map<string, Stored>();
	private readonly frames = new Map<string, string[]>();
	private readonly watchers = new Map<string, Set<MockSocket>>();
	/** artifact id → 评论串（旧 → 新）。 */
	private readonly comments = new Map<string, CommentThread[]>();
	private readonly threads: Map<string, Thread>;
	private readonly hub: ArtifactsHub;
	private seq = 0;
	/** 发布（8.1、8.4 的 mock）。 */
	private readonly publish: MockArtifactPublish;
	/** 在场（6.7 的 mock）：有人在看时演一个在改的 agent。 */
	private readonly activity = new MockArtifactActivity({
		watchers: (id) => this.watchers.get(id) ?? new Set(),
		targets: (id) => this.activityTargets(id),
	});

	constructor(threads: Map<string, Thread>, hub: ArtifactsHub) {
		this.threads = threads;
		this.hub = hub;
		for (const s of SEEDS) this.items.set(s.id, this.fromSeed(s));
		const versionOf = (id: string, n: number) =>
			this.items.get(id)?.versions[n - 1];
		this.publish = new MockArtifactPublish({
			meta: (id) => this.items.get(id)?.meta,
			setPublished: (id, published) => {
				const a = this.items.get(id);
				if (!a) return;
				const { published: _, ...meta } = a.meta;
				a.meta = published ? { ...meta, published } : meta;
			},
			texts: (id, n) =>
				[...(versionOf(id, n)?.files ?? [])]
					.filter(([path]) =>
						/\.(html?|css|m?js|json|svg|md|txt|csv|xml)$/i.test(path),
					)
					.map(([path, f]) => [path, new TextDecoder().decode(f.data)]),
			size: (id, n) => versionOf(id, n)?.meta.size ?? 0,
			changed: () => this.hub.changed(),
		});
		this.publish.seed();
	}

	/** 写入时的线程标题快照（构造时 MockServer 还没登记线程，退到夹具列表）。 */
	private label(threadId: string): string {
		return (
			this.threads.get(threadId)?.title ??
			fixtureThreads.find((t) => t.id === threadId)?.title ??
			""
		);
	}

	private fromSeed(s: Seed): Stored {
		const versions = s.versions.map((v, i): StoredVersion => {
			const data = encoder.encode(v.text);
			const authorThreadId = v.author ?? s.threadId;
			return {
				meta: {
					version: i + 1,
					createdAt: v.at,
					...(authorThreadId ? { authorThreadId } : {}),
					authorLabel: authorThreadId ? this.label(authorThreadId) : "",
					...(v.note ? { note: v.note } : {}),
					size: data.byteLength,
					pages: 1,
				},
				files: new Map([
					[s.path, { data, sha256: pseudoSha(data) }],
					...Object.entries(s.files ?? {}).map(
						([path, text]): [string, StoredFile] => {
							const bytes = encoder.encode(text);
							return [path, { data: bytes, sha256: pseudoSha(bytes) }];
						},
					),
				]),
				capabilities: [],
			};
		});
		const head = versions[versions.length - 1];
		return {
			meta: {
				id: s.id,
				...(s.projectId ? { projectId: s.projectId } : {}),
				...(s.threadId ? { threadId: s.threadId } : {}),
				origin: s.threadId ? "agent" : "import",
				...(s.kind === "document" || s.kind === "slides"
					? { type: s.kind }
					: {}),
				kind: s.kind,
				title: s.title,
				version: versions.length,
				size: head?.meta.size ?? 0,
				pages: 1,
				createdAt: s.versions[0]?.at ?? NOW,
				updatedAt: head?.meta.createdAt ?? NOW,
				...(s.openedAt ? { openedAt: s.openedAt } : {}),
				...(s.deletedAt ? { deletedAt: s.deletedAt } : {}),
			},
			versions,
		};
	}

	// ------------------------------------------------------------ 观看者

	watch(socket: MockSocket, id: unknown) {
		if (typeof id !== "string") return;
		let set = this.watchers.get(id);
		if (!set) {
			set = new Set();
			this.watchers.set(id, set);
		}
		set.add(socket);
		this.activity.changed(id);
	}

	unwatch(socket: MockSocket, id: unknown) {
		if (typeof id === "string") this.watchers.get(id)?.delete(socket);
		if (typeof id === "string") this.activity.changed(id);
	}

	/** 连接断开等于全部 unwatch。 */
	drop(socket: MockSocket) {
		for (const set of this.watchers.values()) set.delete(socket);
		for (const id of this.watchers.keys()) this.activity.changed(id);
	}

	private toWatchers(id: string, msg: Record<string, unknown>) {
		for (const s of this.watchers.get(id) ?? []) s.deliver(msg);
	}

	/** 在场演示的目标：画布是当前文档（有实时文档取实时的）里的各画框，其余是入口文件。 */
	private activityTargets(id: string): { path: string; page?: string }[] {
		const a = this.items.get(id);
		const head = a?.versions[a.versions.length - 1];
		if (!a || !head) return [];
		if (a.meta.kind !== "canvas") {
			const [path] = [...head.files.keys()];
			return path ? [{ path }] : [];
		}
		const raw = head.files.get("canvas.json");
		const doc =
			this.canvasLive.get(id)?.doc ??
			(raw ? parseCanvasDoc(new TextDecoder().decode(raw.data)) : null);
		return (doc?.pages ?? []).map((p) => ({ path: p.file, page: p.id }));
	}

	// ------------------------------------------------------------ 写入

	/**
	 * 以 `author`（线程 id；缺省 = 用户）写一个新版本：入口文件换成 `text`。调试与截图脚本用
	 * （`mockServer.artifacts.write(id, html)`）。
	 */
	write(id: string, text: string, author?: string, note?: string): boolean {
		const a = this.items.get(id);
		const prev = a?.versions[a.versions.length - 1];
		if (!a || !prev || a.meta.deletedAt !== undefined) return false;
		const [path] = [...prev.files.keys()];
		if (!path) return false;
		const data = encoder.encode(text);
		this.pushVersion(
			a,
			new Map([[path, { data, sha256: pseudoSha(data) }]]),
			prev.capabilities,
			author,
			note,
		);
		return true;
	}

	private pushVersion(
		a: Stored,
		files: Map<string, StoredFile>,
		capabilities: ArtifactCapability[],
		author?: string,
		note?: string,
	) {
		const now = Date.now();
		let size = 0;
		for (const f of files.values()) size += f.data.byteLength;
		const version = a.versions.length + 1;
		a.versions.push({
			meta: {
				version,
				createdAt: now,
				...(author ? { authorThreadId: author } : {}),
				authorLabel: author ? this.label(author) : "",
				...(note ? { note } : {}),
				size,
				pages: 1,
			},
			files,
			capabilities,
		});
		a.meta = { ...a.meta, version, size, updatedAt: now };
		this.toWatchers(a.meta.id, {
			type: "artifact.version",
			id: a.meta.id,
			version,
		});
		this.publish.onVersion(a.meta.id, version);
		this.hub.changed();
	}

	// ------------------------------------------------------------ REST

	/** 页面能力授权（步骤 7.3）：artifact id → 能力 → 授权时间。首次列出时给第一个 artifact 种一条，设置页才有内容。 */
	private readonly consents = new Map<string, Map<string, number>>();
	private consentsSeeded = false;

	private listConsents(): Response {
		if (!this.consentsSeeded) {
			this.consentsSeeded = true;
			const first = [...this.items.values()].find(
				(x) => x.meta.deletedAt === undefined,
			);
			if (first)
				this.consents.set(first.meta.id, new Map([["files", Date.now()]]));
		}
		const consents: Schemas["ArtifactConsent"][] = [];
		for (const [id, caps] of this.consents) {
			const a = this.items.get(id);
			if (!a || a.meta.deletedAt !== undefined || caps.size === 0) continue;
			consents.push({
				artifactId: id,
				title: a.meta.title,
				...(a.meta.projectId ? { projectId: a.meta.projectId } : {}),
				capabilities: [
					...caps.keys(),
				].sort() as Schemas["ArtifactConsentCapability"][],
				grantedAt: Math.max(...caps.values()),
			});
		}
		consents.sort((x, y) => y.grantedAt - x.grantedAt);
		return json({ consents } satisfies Schemas["ArtifactConsentList"]);
	}

	async handle(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		const pubRes = await this.publish.handle(req, p, m);
		if (pubRes) return pubRes;
		if (p === "artifacts" && m === "GET") return this.list(url);
		if (p === "artifacts/capabilities" && m === "GET")
			return json({
				frames: "host",
				render: false,
				renderReason: "mock 模式没有渲染器",
				sample: false,
			} satisfies Schemas["ArtifactCapabilities"]);
		if (p === "artifacts/import" && m === "POST")
			return this.importFiles(req, url);
		const frame = /^artifact-frames\/([^/]+)$/.exec(p);
		if (frame && m === "DELETE") {
			for (const u of this.frames.get(frame[1] ?? "") ?? [])
				URL.revokeObjectURL(u);
			this.frames.delete(frame[1] ?? "");
			return new Response(null, { status: 204 });
		}
		if (p === "artifact-consents" && m === "GET") return this.listConsents();
		const consent = /^artifact-consents\/([^/]+)$/.exec(p);
		if (consent && m === "DELETE") {
			this.consents.delete(consent[1] ?? "");
			return new Response(null, { status: 204 });
		}
		// mock 不会发出同意请求，所以没有打开着的请求可回答
		if (consent && m === "POST")
			return error(404, "not_found", "这个同意请求已经结束。");
		const hit = /^artifacts\/(art_[0-9A-Z]{26})(?:\/(.+))?$/.exec(p);
		if (!hit) return null;
		const a = this.items.get(hit[1] ?? "");
		if (!a) return error(404, "not_found", "artifact 不存在");
		const rest = hit[2] ?? "";
		if (rest === "") {
			if (m === "GET") return this.detail(a, url);
			if (m === "PATCH") return this.rename(a, req);
			if (m === "DELETE") return this.remove(a, url);
			return null;
		}
		if (rest === "restore" && m === "POST") {
			if (a.meta.deletedAt === undefined)
				return error(400, "invalid", "这个 artifact 不在「最近删除」里。");
			const { deletedAt: _, ...meta } = a.meta;
			a.meta = meta;
			this.hub.changed();
			return json(a.meta);
		}
		if (rest === "opened" && m === "POST") {
			a.meta = { ...a.meta, openedAt: Date.now() };
			return new Response(null, { status: 204 });
		}
		if (rest === "versions" && m === "GET")
			return json({
				versions: a.versions.map((v) => v.meta).reverse(),
			} satisfies Schemas["ArtifactVersionList"]);
		const restore = /^versions\/(\d+)\/restore$/.exec(rest);
		if (restore && m === "POST")
			return this.restoreVersion(a, Number(restore[1]), req);
		if (rest === "file" && m === "GET") return this.file(a, url);
		if (rest === "element" && m === "POST") return this.editElement(a, req);
		if (rest === "params" && m === "PUT") return this.setParams(a, req);
		if (rest === "export" && m === "GET") return this.exportFiles(a, url);
		// mock 没有渲染器：缩略图一律未命中（卡片回落到类型示意图），PNG 回 render_unavailable
		if (rest === "thumbnail" && m === "GET")
			return error(404, "no_thumbnail", "这个版本还没有缩略图");
		if (rest === "render.png" && m === "GET")
			return error(503, "render_unavailable", "mock 模式没有渲染器");
		if (rest === "comments" || rest.startsWith("comments/"))
			return this.handleComments(a, rest.slice("comments".length), m, req);
		if (rest === "canvas" && m === "GET") return this.canvasState(a);
		if (rest === "canvas/ops" && m === "POST") return this.canvasOps(a, req);
		if (rest === "frames" && m === "POST") return this.openFrame(a, req, false);
		if (rest === "open-link" && m === "POST")
			return this.openFrame(a, req, true);
		return null;
	}

	/**
	 * 元素直接编辑（同 `POST …/element`）：只改偏移处起始标签的 `style`（同名替换、其余保留、没有就加）与（可选的）
	 * 纯文本内容，落成用户版本。文件在 `baseVersion` 之后变过或偏移处不是该标签回 409。
	 */
	private async editElement(a: Stored, req: Request): Promise<Response> {
		if (a.meta.deletedAt !== undefined)
			return error(410, "deleted", "这个 artifact 已被删除");
		const body = (await req.json()) as Schemas["ArtifactElementEdit"];
		const head = a.versions[a.versions.length - 1];
		const base = a.versions[body.baseVersion - 1];
		if (!head || !base) return error(400, "invalid", "baseVersion 不存在");
		const conflict = () =>
			json(
				{
					code: "conflict",
					message: `现在是版本 ${a.meta.version}，${body.path} 被改过。`,
					state: {
						version: a.meta.version,
						deleted: false,
						changed: [body.path],
					},
				},
				409,
			);
		const file = head.files.get(body.path);
		if (!file || base.files.get(body.path)?.sha256 !== file.sha256)
			return conflict();
		const style = cleanStyle(body.style ?? {});
		if (Object.keys(style).length !== Object.keys(body.style ?? {}).length)
			return error(400, "invalid", "样式值不合法");
		const decoder = new TextDecoder();
		const src = decoder.decode(file.data);
		const at = decoder.decode(file.data.subarray(0, body.offset)).length;
		const open = /^<([a-zA-Z][a-zA-Z0-9-]*)(?:"[^"]*"|'[^']*'|[^'">])*>/.exec(
			src.slice(at),
		);
		if (!open || open[1]?.toLowerCase() !== body.tag) return conflict();
		let tag = open[0];
		if (Object.keys(style).length > 0) {
			const attr = /\sstyle\s*=\s*"([^"]*)"/i.exec(tag);
			const decls = (attr?.[1] ?? "")
				.split(";")
				.map((d) => d.trim())
				.filter(
					(d) =>
						d !== "" &&
						!Object.hasOwn(style, d.split(":")[0]?.trim().toLowerCase() ?? ""),
				);
			for (const [k, v] of Object.entries(style)) decls.push(`${k}: ${v}`);
			const next = ` style="${decls.join("; ")}"`;
			tag = attr
				? tag.replace(attr[0], next)
				: `<${open[1]}${next}${tag.slice(1 + (open[1]?.length ?? 0))}`;
		}
		let rest = src.slice(at + open[0].length);
		if (body.text !== undefined) {
			const raw = sourceTextAt(file.data, body.offset, body.tag);
			if (raw === null)
				return error(400, "invalid", "只有内容全是文字的元素能直接改文字");
			const escaped = body.text
				.replaceAll("&", "&amp;")
				.replaceAll("<", "&lt;")
				.replaceAll(">", "&gt;");
			rest = escaped + rest.slice(raw.length);
		}
		const data = encoder.encode(src.slice(0, at) + tag + rest);
		this.pushVersion(
			a,
			new Map(head.files).set(body.path, { data, sha256: pseudoSha(data) }),
			head.capabilities,
		);
		return json({
			version: a.meta.version,
		} satisfies Schemas["ArtifactVersionRef"]);
	}

	/**
	 * 写回调节参数（同 `PUT …/params`）：校验并合并值（数字夹回范围），整段声明按两空格缩进写回（`</` 写成 `<\/`），
	 * 文件其余部分不变，落成用户版本。文件在 `baseVersion` 之后变过回 409。
	 */
	private async setParams(a: Stored, req: Request): Promise<Response> {
		if (a.meta.deletedAt !== undefined)
			return error(410, "deleted", "这个 artifact 已被删除");
		const body = (await req.json()) as Schemas["ArtifactParamsSet"];
		const head = a.versions[a.versions.length - 1];
		const base = a.versions[body.baseVersion - 1];
		if (!head || !base) return error(400, "invalid", "baseVersion 不存在");
		const file = head.files.get(body.path);
		if (!file || base.files.get(body.path)?.sha256 !== file.sha256)
			return json(
				{
					code: "conflict",
					message: `现在是版本 ${a.meta.version}，${body.path} 被改过。`,
					state: {
						version: a.meta.version,
						deleted: false,
						changed: [body.path],
					},
				},
				409,
			);
		const src = new TextDecoder().decode(file.data);
		const m =
			/(<script\b[^>]*\bdata-oryn-params\b[^>]*>)(\s*)([\s\S]*?)(\s*)<\/script>/i.exec(
				src,
			);
		let decl: Record<string, Record<string, unknown>>;
		try {
			decl = JSON.parse(m?.[3] ?? "");
		} catch {
			return error(400, "invalid", `${body.path} 没有声明调节参数`);
		}
		const declared = parseParamsDecl(decl);
		for (const [name, raw] of Object.entries(body.values)) {
			const p = declared.find((d) => d.name === name);
			const v = p ? coerceParamValue(p, raw) : null;
			const def = decl[name];
			if (v === null || !def)
				return error(400, "invalid", `参数 ${name} 的值不合法`);
			def.value = v;
		}
		if (!m) return error(400, "invalid", `${body.path} 没有声明调节参数`);
		const text = JSON.stringify(decl, null, 2)
			.replaceAll("</", "<\\/")
			.replaceAll("<!--", "\\u003c!--");
		const start = m.index + (m[1]?.length ?? 0) + (m[2]?.length ?? 0);
		const data = encoder.encode(
			src.slice(0, start) + text + src.slice(start + (m[3]?.length ?? 0)),
		);
		this.pushVersion(
			a,
			new Map(head.files).set(body.path, { data, sha256: pseudoSha(data) }),
			head.capabilities,
		);
		return json({
			version: a.meta.version,
		} satisfies Schemas["ArtifactVersionRef"]);
	}

	private list(url: URL): Response {
		const deleted = url.searchParams.get("deleted") === "true";
		const threadId = url.searchParams.get("threadId") ?? "";
		const projectId = url.searchParams.get("projectId") ?? "";
		const artifacts = [...this.items.values()]
			.filter(
				(a) =>
					(a.meta.deletedAt !== undefined) === deleted &&
					(!threadId ||
						a.meta.threadId === threadId ||
						a.versions.some((v) => v.meta.authorThreadId === threadId)) &&
					(!projectId || a.meta.projectId === projectId),
			)
			.map((a) => a.meta)
			.sort((x, y) => y.updatedAt - x.updatedAt);
		return json({ artifacts } satisfies Schemas["ArtifactList"]);
	}

	private version(a: Stored, raw: string | null): StoredVersion | Response {
		const n = raw ? Number(raw) : a.versions.length;
		if (!Number.isInteger(n) || n < 1)
			return error(400, "invalid", "version 必须从 1 开始");
		return a.versions[n - 1] ?? error(404, "not_found", `没有版本 ${n}`);
	}

	private detail(a: Stored, url: URL): Response {
		const v = this.version(a, url.searchParams.get("version"));
		if (v instanceof Response) return v;
		const tids = [
			...(a.meta.threadId ? [a.meta.threadId] : []),
			...a.versions
				.map((x) => x.meta.authorThreadId)
				.filter((t): t is string => !!t)
				.reverse(),
		];
		const threads = [...new Set(tids)].flatMap((tid) => {
			const t = this.threads.get(tid);
			return t
				? [{ threadId: t.id, title: t.title, archived: !!t.archivedAt }]
				: [];
		});
		let canvas: Record<string, unknown> | undefined;
		if (a.meta.kind === "canvas") {
			const f = v.files.get("canvas.json");
			try {
				canvas = f
					? (JSON.parse(new TextDecoder().decode(f.data)) as Record<
							string,
							unknown
						>)
					: undefined;
			} catch {
				canvas = undefined;
			}
		}
		return json({
			artifact: a.meta,
			version: v.meta.version,
			head: a.meta.version,
			files: [...v.files].map(([path, f]) => ({
				path,
				size: f.data.byteLength,
				mime: mimeOf(path),
				sha256: f.sha256,
			})),
			capabilities: v.capabilities,
			...(canvas ? { canvas } : {}),
			threads,
		} satisfies Schemas["ArtifactDetail"]);
	}

	private async rename(a: Stored, req: Request): Promise<Response> {
		if (a.meta.deletedAt !== undefined)
			return error(
				410,
				"deleted",
				"这个 artifact 在「最近删除」里，恢复后才能改名。",
			);
		const raw = await bodyField(req, "title");
		const title = typeof raw === "string" ? raw.trim() : "";
		if (!title || [...title].length > MAX_TITLE_RUNES)
			return error(
				400,
				"invalid",
				`标题不能为空，且不超过 ${MAX_TITLE_RUNES} 个字。`,
			);
		a.meta = { ...a.meta, title };
		this.hub.changed();
		return json(a.meta);
	}

	private remove(a: Stored, url: URL): Response {
		if (url.searchParams.get("permanent") === "true") {
			if (a.meta.deletedAt === undefined)
				return error(400, "invalid", "只能永久删除「最近删除」里的 artifact。");
			this.publish.drop(a.meta.id);
			this.items.delete(a.meta.id);
		} else {
			if (a.meta.deletedAt !== undefined)
				return error(410, "deleted", "这个 artifact 已在「最近删除」里。");
			if (url.searchParams.get("unpublish") === "true")
				this.publish.drop(a.meta.id);
			a.meta = { ...a.meta, deletedAt: Date.now() };
		}
		this.hub.changed();
		return new Response(null, { status: 204 });
	}

	private async restoreVersion(
		a: Stored,
		n: number,
		req: Request,
	): Promise<Response> {
		if (a.meta.deletedAt !== undefined)
			return error(410, "deleted", "这个 artifact 在「最近删除」里。");
		const src = a.versions[n - 1];
		if (!src) return error(404, "not_found", `没有版本 ${n}`);
		const raw = await bodyField(req, "baseVersion");
		const base = typeof raw === "number" ? raw : 0;
		const head = a.versions[a.versions.length - 1];
		const baseV = a.versions[base - 1];
		if (!head || !baseV)
			return error(400, "invalid", "baseVersion 不在 1 到最新版本之间。");
		// 同服务端：只比较「两版之间内容不同的路径」在 baseVersion 之后是否又被改过
		const paths = new Set([...src.files.keys(), ...head.files.keys()]);
		const changed = [...paths]
			.filter(
				(path) => src.files.get(path)?.sha256 !== head.files.get(path)?.sha256,
			)
			.filter(
				(path) =>
					baseV.files.get(path)?.sha256 !== head.files.get(path)?.sha256,
			)
			.sort();
		if (changed.length > 0)
			return json(
				{
					code: "conflict",
					message: `${a.meta.id} 现在是版本 ${a.meta.version}，${changed.join("、")} 在你的 baseVersion 之后被改过。`,
					state: { version: a.meta.version, deleted: false, changed },
				} satisfies Schemas["ArtifactConflict"],
				409,
			);
		this.pushVersion(
			a,
			new Map(src.files),
			src.capabilities,
			undefined,
			`恢复到 v${n}`,
		);
		return json(a.meta);
	}

	private file(a: Stored, url: URL): Response {
		const v = this.version(a, url.searchParams.get("version"));
		if (v instanceof Response) return v;
		const path = url.searchParams.get("path") ?? "";
		const f = v.files.get(path);
		if (!f) return error(404, "not_found", "文件不存在");
		const mime = mimeOf(path);
		const inline = /^image\/(png|jpeg|gif|webp)$/.test(mime);
		return new Response(f.data, {
			headers: {
				"Content-Type": inline ? mime : "application/octet-stream",
				"Content-Disposition": `${inline ? "inline" : "attachment"}; filename*=UTF-8''${encodeURIComponent(path.split("/").pop() ?? path)}`,
				"Content-Security-Policy": "sandbox",
				"X-Content-Type-Options": "nosniff",
			},
		});
	}

	/** 导出原始文件：`html` 只在版本恰好一个文件时可用，`zip` 打包全部文件（同 `export.go`）。 */
	private exportFiles(a: Stored, url: URL): Response {
		const v = this.version(a, url.searchParams.get("version"));
		if (v instanceof Response) return v;
		const format = url.searchParams.get("format");
		const files = [...v.files];
		const name = (ext: string) =>
			`attachment; filename*=UTF-8''${encodeURIComponent(`${a.meta.title}.${ext}`)}`;
		if (format === "html") {
			const [only] = files;
			if (files.length !== 1 || !only)
				return error(
					400,
					"invalid",
					`版本 ${v.meta.version} 有 ${files.length} 个文件，只能导出为 zip。`,
				);
			return new Response(only[1].data, {
				headers: {
					"Content-Type": "text/html; charset=utf-8",
					"Content-Disposition": name("html"),
				},
			});
		}
		if (format === "zip")
			return new Response(storedZip(files.map(([p, f]) => [p, f.data])), {
				headers: {
					"Content-Type": "application/zip",
					"Content-Disposition": name("zip"),
				},
			});
		return error(
			400,
			"invalid",
			`artifact ${a.meta.id} 不能导出为 "${format ?? ""}"（页面与画布只能导出为 html 或 zip）。`,
		);
	}

	private async openFrame(
		a: Stored,
		req: Request,
		topLevel: boolean,
	): Promise<Response> {
		if (a.meta.deletedAt !== undefined)
			return error(410, "deleted", "这个 artifact 在「最近删除」里。");
		const raw = await bodyField(req, "version");
		const v = this.version(a, typeof raw === "number" ? String(raw) : null);
		if (v instanceof Response) return v;
		// 跟随最新版本的帧带页面运行时；钉住旧版本与顶层帧不带
		const follow = raw === undefined && !topLevel;
		if (a.meta.kind === "canvas" && !topLevel) {
			const token = this.newToken();
			this.canvasFrames.set(token, { id: a.meta.id, runtime: follow });
			this.frames.set(token, []);
			const url = `${location.origin}/__mock_af/${token}/`;
			return json({ url, token } satisfies Schemas["ArtifactFrame"], 201);
		}
		const content = this.framePage(a, v, follow, topLevel);
		const token = this.newToken();
		const urls = [
			URL.createObjectURL(new Blob([content], { type: "text/html" })),
		];
		if (topLevel) {
			const shell = `<!doctype html><html><head><meta charset="utf-8"><title>${escapeHtml(a.meta.title)}</title><style>html,body,iframe{margin:0;border:0;width:100%;height:100%;display:block}</style></head><body><iframe sandbox="allow-scripts allow-forms allow-downloads allow-popups allow-popups-to-escape-sandbox allow-modals" src="${urls[0]}"></iframe></body></html>`;
			urls.push(URL.createObjectURL(new Blob([shell], { type: "text/html" })));
		}
		this.frames.set(token, urls);
		const url = urls[urls.length - 1] ?? "";
		if (topLevel) return json({ url } satisfies Schemas["ArtifactOpenLink"]);
		return json({ url, token } satisfies Schemas["ArtifactFrame"], 201);
	}

	private newToken(): string {
		this.seq++;
		const alphabet = "abcdefghijklmnopqrstuvwxyz234567";
		let s = "";
		let n = this.seq;
		for (let i = 0; i < 26; i++) {
			s += alphabet[(n + i * 7) % 32];
			n = Math.floor(n / 2);
		}
		return s;
	}

	/** 帧里的页面：plain 页面原样；其余类型（查看器在阶段 5、6 接入）先以源码显示。 */
	private framePage(
		a: Stored,
		v: StoredVersion,
		runtime: boolean,
		topLevel: boolean,
	): string {
		const [entry] = [...v.files];
		const path = entry?.[0] ?? "index.html";
		const text = entry ? new TextDecoder().decode(entry[1].data) : "";
		const html =
			a.meta.kind === "web"
				? text
				: `<!doctype html><html><head><meta charset="utf-8"><title>${escapeHtml(a.meta.title)}</title><style>body{font:13px/1.6 ui-monospace,monospace;margin:0;padding:24px;white-space:pre-wrap;color:#18181b;background:#fafafa}@media (prefers-color-scheme: dark){body{color:#fafafa;background:#0f0f11}}</style></head><body>${escapeHtml(text)}</body></html>`;
		if (topLevel) return html;
		const mark = Math.random().toString(36).slice(2, 10).padEnd(8, "0");
		// 只有页面类型标源码（旧版本的帧不带运行时，标了也无妨）
		const marked = a.meta.kind === "web" ? markSources(html, mark) : html;
		const config = JSON.stringify({ v: 1, mark, path, runtime });
		const origin = location.origin;
		const inject = `<script type="module" src="${origin}/sandbox/runtime/main.ts"></script><script type="module">window.__orynStart(${config});</script>`;
		const head = /<head[^>]*>/i.exec(marked);
		return head
			? marked.slice(0, head.index + head[0].length) +
					inject +
					marked.slice(head.index + head[0].length)
			: inject + marked;
	}

	// ------------------------------------------------------------ 评论

	private async handleComments(
		a: Stored,
		sub: string,
		m: string,
		req: Request,
	): Promise<Response | null> {
		const id = a.meta.id;
		const list = this.comments.get(id) ?? [];
		if (sub === "" && m === "GET")
			return json({
				version: a.meta.version,
				threads: list,
			} satisfies Schemas["ArtifactCommentThreadList"]);
		if (a.meta.deletedAt !== undefined)
			return error(410, "deleted", "这个 artifact 在「最近删除」里。");
		// 请求体只能读一次
		let payload: Record<string, unknown> = {};
		try {
			const raw: unknown = await req.json();
			if (raw && typeof raw === "object")
				payload = raw as Record<string, unknown>;
		} catch {
			// 没有体（或不是 JSON）：字段一律缺省
		}
		const text = typeof payload.body === "string" ? payload.body.trim() : "";
		const bodyError =
			text && [...text].length <= MAX_COMMENT_RUNES
				? null
				: error(
						400,
						"invalid",
						`评论不能为空，且不超过 ${MAX_COMMENT_RUNES} 个字。`,
					);
		const now = Date.now();
		if (sub === "" && m === "POST") {
			const anchor = payload.anchor as CommentThread["anchor"] | undefined;
			if (typeof anchor?.path !== "string")
				return error(400, "invalid", "anchor 不合法");
			if (bodyError) return bodyError;
			this.seq++;
			const t: CommentThread = {
				id: `act_01JDNACMT${String(this.seq).padStart(17, "0")}`,
				anchor: { ...anchor, missing: false },
				createdAt: now,
				comments: [{ n: 1, authorLabel: "", body: text, createdAt: now }],
			};
			this.comments.set(id, [...list, t]);
			this.commentsChanged(id);
			return json(t, 201);
		}
		if (sub === "/sent" && m === "POST") {
			const ids = payload.threadIds as string[] | undefined;
			const sentTo = String(payload.sentTo ?? "");
			this.comments.set(
				id,
				list.map((t) =>
					ids?.includes(t.id) ? { ...t, sentAt: now, sentTo } : t,
				),
			);
			this.commentsChanged(id);
			return new Response(null, { status: 204 });
		}
		const hit = /^\/(act_[0-9A-Z]{26})(\/replies)?$/.exec(sub);
		const cur = list.find((t) => t.id === hit?.[1]);
		if (!hit || !cur) return error(404, "not_found", "评论串不存在");
		let next: CommentThread;
		if (hit[2] && m === "POST") {
			if (cur.resolvedAt !== undefined)
				return error(400, "invalid", "已解决的评论不能回复，先重新打开。");
			if (bodyError) return bodyError;
			// 用户回复清掉「已发送」
			const { sentAt: _a, sentTo: _b, ...rest } = cur;
			next = {
				...rest,
				comments: [
					...cur.comments,
					{
						n: cur.comments.length + 1,
						authorLabel: "",
						body: text,
						createdAt: now,
					},
				],
			};
		} else if (!hit[2] && m === "PATCH") {
			const resolved = payload.resolved === true;
			const { resolvedAt: _, ...rest } = cur;
			next = resolved ? { ...rest, resolvedAt: now } : rest;
		} else return null;
		this.comments.set(
			id,
			list.map((t) => (t.id === cur.id ? next : t)),
		);
		this.commentsChanged(id);
		return json(next);
	}

	private commentsChanged(id: string) {
		this.toWatchers(id, { type: "artifact.comments", id });
	}

	private async importFiles(req: Request, url: URL): Promise<Response> {
		let form: FormData;
		try {
			form = await req.formData();
		} catch {
			return error(400, "invalid", "请求体不是 multipart/form-data");
		}
		const files = new Map<string, StoredFile>();
		let total = 0;
		for (const [path, value] of form) {
			if (typeof value === "string") continue;
			// 隐藏文件（任何一段以点开头）跳过
			if (path.split("/").some((seg) => seg.startsWith("."))) continue;
			if (pathProblem(path))
				return error(400, "bad_name", `文件名 ${path} 不合法`);
			const data = new Uint8Array(await value.arrayBuffer());
			if (data.byteLength > MAX_FILE_BYTES)
				return error(413, "too_large", `${path} 超过 20 MiB`);
			if (/\.html?$/i.test(path)) {
				try {
					new TextDecoder("utf-8", { fatal: true }).decode(data);
				} catch {
					return error(400, "not_utf8", `${path} 不是 UTF-8`);
				}
			}
			total += data.byteLength;
			files.set(path, { data, sha256: pseudoSha(data) });
			if (files.size > MAX_VERSION_FILES)
				return error(413, "too_many", `文件超过 ${MAX_VERSION_FILES} 个`);
		}
		if (total > 64 << 20) return error(413, "too_large", "总大小超过 64 MiB");
		const lower = new Map([...files.keys()].map((k) => [k.toLowerCase(), k]));
		if (lower.size !== files.size)
			return error(400, "bad_name", "有只差大小写的文件名");
		// 只上传了一个 HTML 文件时它就是页面本身：存成 index.html（同 import.go）
		const [lone] = files.keys();
		const loneFile = lone ? files.get(lone) : undefined;
		if (files.size === 1 && lone && loneFile && /^[^/]+\.html?$/i.test(lone)) {
			files.delete(lone);
			files.set("index.html", loneFile);
		}
		const index = files.get("index.html");
		const kind: ArtifactKind = index
			? "web"
			: files.has("canvas.json")
				? "canvas"
				: "web";
		if (!index && !files.has("canvas.json"))
			return error(400, "no_index", "缺少 index.html 或 canvas.json");
		const titleTag = index
			? /<title[^>]*>([^<]*)<\/title>/i
					.exec(new TextDecoder().decode(index.data))?.[1]
					?.trim()
			: undefined;
		const title = titleTag || url.searchParams.get("name")?.trim() || "未命名";
		const projectId = url.searchParams.get("projectId") ?? undefined;
		const now = Date.now();
		const id = `art_01JDNAIMPORT${String(++this.seq).padStart(14, "0")}`;
		const meta: Artifact = {
			id,
			...(projectId ? { projectId } : {}),
			origin: "import",
			kind,
			title: [...title].slice(0, MAX_TITLE_RUNES).join(""),
			version: 1,
			size: total,
			pages: 1,
			createdAt: now,
			updatedAt: now,
		};
		this.items.set(id, {
			meta,
			versions: [
				{
					meta: {
						version: 1,
						createdAt: now,
						authorLabel: "",
						size: total,
						pages: 1,
					},
					files,
					capabilities: [],
				},
			],
		});
		this.publish.onCreated(id);
		this.hub.changed();
		return json(meta, 201);
	}

	// ------------------------------------------------------------ 实时画布（计划 6.2 的 mock）

	/** 画布帧：令牌 → artifact 与是否带运行时。帧地址是 `<origin>/__mock_af/<令牌>/`，画框文件经 `resolveFrameUrl` 换成 blob。 */
	private readonly canvasFrames = new Map<
		string,
		{ id: string; runtime: boolean }
	>();
	private readonly canvasLive = new Map<string, MockCanvasLive>();
	private readonly canvasSeq = new Map<string, number>();
	private readonly frameBlobs = new Map<string, string>();

	private liveCanvas(a: Stored): MockCanvasLive | Response {
		const cur = this.canvasLive.get(a.meta.id);
		if (cur) return cur;
		if (a.meta.kind !== "canvas")
			return error(400, "invalid", `${a.meta.id} 不是画布 artifact。`);
		if (a.meta.deletedAt !== undefined)
			return error(410, "deleted", "这个 artifact 在「最近删除」里。");
		const head = a.versions[a.versions.length - 1];
		const raw = head?.files.get("canvas.json");
		if (!head || !raw) return error(404, "not_found", "没有 canvas.json");
		const live: MockCanvasLive = {
			version: head.meta.version,
			doc: parseCanvasDoc(new TextDecoder().decode(raw.data)),
			copies: new Map(),
			timer: undefined,
		};
		this.canvasLive.set(a.meta.id, live);
		return live;
	}

	private canvasState(a: Stored): Response {
		const live = this.liveCanvas(a);
		if (live instanceof Response) return live;
		return json({
			version: live.version,
			seq: this.canvasSeq.get(a.meta.id) ?? 0,
			doc: live.doc as unknown as Record<string, unknown>,
		} satisfies Schemas["ArtifactCanvasState"]);
	}

	/** 应用一批用户操作：广播给观看者，静默 30 秒后落成一个用户版本（同 `internal/artifacts/live.go`）。 */
	private async canvasOps(a: Stored, req: Request): Promise<Response> {
		const live = this.liveCanvas(a);
		if (live instanceof Response) return live;
		let body: Schemas["ArtifactCanvasOps"];
		try {
			body = (await req.json()) as Schemas["ArtifactCanvasOps"];
		} catch {
			return error(400, "invalid", "请求体不是 JSON");
		}
		let ops: CanvasOp[];
		let doc: CanvasDoc;
		try {
			ops = parseCanvasOps(body.ops);
			if (ops.length === 0) throw new Error("没有操作");
			for (const op of ops)
				if (op.op === "add" && op.object.type === "note")
					op.object = { ...op.object, by: null };
			doc = applyCanvasOps(live.doc, ops);
		} catch (e) {
			return error(400, "invalid", `画布操作不合法：${(e as Error).message}`);
		}
		// 新增的对象换成应用后的完整形态（补上缺省的位置、文件），各端重放得到同一个结果
		const settled = ops.map((op): CanvasOp => {
			if (op.op !== "add") return op;
			const key = op.object.id;
			const found =
				doc.canvases.find((c) => c.id === key) ??
				doc.pages.find((p) => p.id === key) ??
				doc.notes.find((n) => n.id === key);
			if (op.copyOf !== undefined && op.object.type === "page") {
				const from = live.doc.pages.find((p) => p.id === op.copyOf);
				const page = doc.pages.find((p) => p.id === key);
				if (from && page) live.copies.set(page.file, from.file);
			}
			return found ? { ...op, object: { ...found, type: op.object.type } } : op;
		});
		live.doc = doc;
		const seq = (this.canvasSeq.get(a.meta.id) ?? 0) + 1;
		this.canvasSeq.set(a.meta.id, seq);
		this.toWatchers(a.meta.id, {
			type: "artifact.canvas",
			id: a.meta.id,
			seq,
			ops: settled,
			client: body.client,
			clientSeq: body.clientSeq,
		});
		clearTimeout(live.timer);
		live.timer = setTimeout(() => this.flushCanvas(a), CANVAS_QUIET_MS);
		return json({ seq } satisfies Schemas["ArtifactCanvasSeq"]);
	}

	/** 把累计的画布写成一个用户版本：新画框没有文件时补空白页，带 copyOf 的复制来源画框的文件。 */
	private flushCanvas(a: Stored) {
		const live = this.canvasLive.get(a.meta.id);
		const prev = a.versions[a.versions.length - 1];
		if (!live || !prev || a.meta.deletedAt !== undefined) return;
		const files = new Map(prev.files);
		const doc = encoder.encode(stringifyCanvasDoc(live.doc));
		files.set("canvas.json", { data: doc, sha256: pseudoSha(doc) });
		for (const p of live.doc.pages) {
			if (files.has(p.file)) continue;
			const source = live.copies.get(p.file);
			const copied = source ? files.get(source) : undefined;
			const data = copied?.data ?? encoder.encode(BLANK_PAGE);
			files.set(p.file, { data, sha256: pseudoSha(data) });
		}
		live.copies.clear();
		this.pushVersion(a, files, prev.capabilities);
		live.version = a.meta.version;
	}

	/**
	 * 画布帧里画框文件的地址（`<origin>/__mock_af/<令牌>/_oryn/v/<n>/<路径>`）→ 该版本文件的 blob（HTML 注入页面运行时）。
	 * 不是画布帧的地址返回 null。mock 安装时拦截 iframe 的 `src` 来调它。
	 */
	resolveFrameUrl(raw: string): string | null {
		let url: URL;
		try {
			url = new URL(raw, location.href);
		} catch {
			return null;
		}
		const hit = /^\/__mock_af\/([a-z2-7]+)\/_oryn\/v\/(\d+)\/(.+)$/.exec(
			url.pathname,
		);
		const frame = hit ? this.canvasFrames.get(hit[1] ?? "") : undefined;
		if (!hit || !frame) return null;
		const key = `${hit[1]}|${hit[2]}|${hit[3]}`;
		const cached = this.frameBlobs.get(key);
		if (cached) return cached;
		const path = (hit[3] ?? "").split("/").map(decodeURIComponent).join("/");
		const file = this.items
			.get(frame.id)
			?.versions[Number(hit[2]) - 1]?.files.get(path);
		let html = file
			? new TextDecoder().decode(file.data)
			: "<!doctype html><title>404</title><p>没有这个文件</p>";
		if (file && /\.html?$/i.test(path)) {
			const mark = Math.random().toString(36).slice(2, 10).padEnd(8, "0");
			const config = JSON.stringify({
				v: 1,
				mark,
				path,
				runtime: frame.runtime,
			});
			const inject = `<script type="module" src="${location.origin}/sandbox/runtime/main.ts"></script><script type="module">window.__orynStart(${config});</script>`;
			const head = /<head[^>]*>/i.exec(html);
			html = head
				? html.slice(0, head.index + head[0].length) +
					inject +
					html.slice(head.index + head[0].length)
				: inject + html;
		}
		const blob = URL.createObjectURL(
			new Blob([html], { type: file ? mimeOf(path) : "text/html" }),
		);
		this.frameBlobs.set(key, blob);
		this.frames.get(hit[1] ?? "")?.push(blob);
		return blob;
	}
}
