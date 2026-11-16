/**
 * mock 后端的 Artifacts（M14）：`/api/v1/artifacts*` 与 `/api/v1/artifact-frames/*`，响应形状与 orynd
 * （`internal/api/artifacts.go`）一致。夹具是四种类型各一个（单文件），外加一个在「最近删除」里的页面。
 * - 帧（`POST …/frames`）回一个 `blob:` 地址：页面 HTML 里用 `<script type="module" src="<origin>/sandbox/runtime/main.ts">`
 *   引入运行时（vite 开发服务器直接提供），随后调用 `__orynStart(<配置>)`。blob 是申请帧那一刻的快照：
 *   新版本到来时帧会照常重载，但要点「重新加载」（重新申请帧）才看得到新内容。
 * - 「在浏览器中打开」回一个外壳页的 `blob:` 地址，外壳里用沙箱 iframe 装内容，不让 artifact 的脚本跑在应用的源上。
 * - 观看者（`artifact.watch`）按连接登记；`write` 产生新版本并推 `artifact.version` 与 `artifacts.changed`。
 * `MockServer.handleFetch` 调 `handle`，不归这里的请求返回 null。
 */
import type { Schemas, Thread } from "../api/client";
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
						pages: [],
						notes: [],
					},
					null,
					2,
				)}\n`,
				at: NOW - 2 * DAY,
			},
		],
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

/** 观看者消息（`artifact.version` 等）与 `artifacts.changed` 的出口。 */
export interface ArtifactsHub {
	changed(): void;
}

export class MockArtifacts {
	private readonly items = new Map<string, Stored>();
	private readonly frames = new Map<string, string[]>();
	private readonly watchers = new Map<string, Set<MockSocket>>();
	private readonly threads: Map<string, Thread>;
	private readonly hub: ArtifactsHub;
	private seq = 0;

	constructor(threads: Map<string, Thread>, hub: ArtifactsHub) {
		this.threads = threads;
		this.hub = hub;
		for (const s of SEEDS) this.items.set(s.id, this.fromSeed(s));
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
				files: new Map([[s.path, { data, sha256: pseudoSha(data) }]]),
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
	}

	unwatch(socket: MockSocket, id: unknown) {
		if (typeof id === "string") this.watchers.get(id)?.delete(socket);
	}

	/** 连接断开等于全部 unwatch。 */
	drop(socket: MockSocket) {
		for (const set of this.watchers.values()) set.delete(socket);
	}

	private toWatchers(id: string, msg: Record<string, unknown>) {
		for (const s of this.watchers.get(id) ?? []) s.deliver(msg);
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
		this.hub.changed();
	}

	// ------------------------------------------------------------ REST

	async handle(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
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
		if (rest === "frames" && m === "POST") return this.openFrame(a, req, false);
		if (rest === "open-link" && m === "POST")
			return this.openFrame(a, req, true);
		return null;
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
			this.items.delete(a.meta.id);
		} else {
			if (a.meta.deletedAt !== undefined)
				return error(410, "deleted", "这个 artifact 已在「最近删除」里。");
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
		const config = JSON.stringify({ v: 1, mark, path, runtime });
		const origin = location.origin;
		const inject = `<script type="module" src="${origin}/sandbox/runtime/main.ts"></script><script type="module">window.__orynStart(${config});</script>`;
		const head = /<head[^>]*>/i.exec(html);
		return head
			? html.slice(0, head.index + head[0].length) +
					inject +
					html.slice(head.index + head[0].length)
			: inject + html;
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
		this.hub.changed();
		return json(meta, 201);
	}
}
