/**
 * mock 后端的项目仓库镜像（M24 阶段 ②）：状态与同步、分支 / 标签、提交与提交详情、diff、目录树与文件内容，
 * 响应形状与错误码同 orynd（`internal/repos`、`internal/api/projectrepo.go`）。`MockServer.handleFetch` 调 `handle`，
 * 不归这里的请求返回 null；状态变化后 `deps.changed(projectId)` 推送 WS `project_repo.changed`。
 * 预置：第一个项目（Oryn）镜像就绪，带一个 8 个提交的假仓库（`main` 7 个提交、`feature/repo-browser` 多 1 个，
 * 附注标签 `v0.1.0`，其中 2 个提交关联到 Oryn 的线程）；oryn-core 克隆失败（凭据被拒）；其余有仓库地址的项目未克隆，
 * 没有仓库地址的是 `kind:"none"`。`POST repo/sync`：就绪的进入同步、600ms 后完成；未克隆的克隆成空仓库；失败的再失败一次。
 * Git LFS（阶段 ② 补充）：`docs/media/` 下三个 LFS 指针文件——能预览的 SVG、超过预览上限只能下载的视频、上游没有对象的
 * PSD；目录树给对象的真实大小与 `lfs.oid`，blob 返回对象内容（或同服务端的 413 / 404 `lfs_missing`）。
 * 托管项目（阶段 ③，`hosted`）：`kind:"hosted"`、就绪，读接口复用 Oryn 的假仓库数据；`POST repo/sync` 回 409 `repo_hosted`。
 */
import type { Project, Schemas, Thread } from "../api/client";
import { projects as fixtureProjects, NOW } from "./fixtures";

type Status = Schemas["ProjectRepoStatus"];
type Summary = Schemas["ProjectRepoCommitSummary"];
type TreeEntry = Schemas["ProjectRepoTreeEntry"];
type FileChange = Schemas["GitFileChange"];

export interface RepoDeps {
	/** 某个项目的镜像状态变了：推送 WS `project_repo.changed` */
	changed(projectId: string): void;
	/** 项目表（含已归档） */
	projects(): readonly Project[];
	/** 线程表（只读；提交关联的线程从这里取标题） */
	threads: ReadonlyMap<string, Thread>;
}

const MIN = 60_000;
const HOUR = 60 * MIN;
const ZERO_SHA = "0".repeat(40);
const SYNC_MS = 600;
const DATA_PROJECT = fixtureProjects[0]?.id ?? "";
/** 带假仓库数据的项目：Oryn 与托管项目（托管项目复用 Oryn 的数据）。 */
const hasData = (p: Project): boolean =>
	p.id === DATA_PROJECT || p.hosted === true;
const FAILING_PROJECT = fixtureProjects[1]?.id ?? "";
const SHA_RE = /^[0-9a-f]{7,40}$/;
const REF_RE = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$/;

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

/** 由种子算出确定的 40 位十六进制（形如真实 sha）：5 轮带盐的 FNV-1a。 */
function fakeSha(seed: string): string {
	let out = "";
	for (let round = 0; round < 5; round++) {
		let h = 0x811c9dc5 ^ (round * 0x9e3779b1);
		for (let i = 0; i < seed.length; i++) {
			h ^= seed.charCodeAt(i);
			h = Math.imul(h, 0x01000193);
		}
		out += (h >>> 0).toString(16).padStart(8, "0");
	}
	return out;
}

const README_V1 = "# webapp\n\nOryn 演示仓库。\n";
const README_V2 = `# webapp

Oryn 演示仓库：一个很小的 Go 程序，用来演示项目主页的仓库浏览。

- 使用说明见 [指南](docs/guide.md)
- 架构见下图

![架构图](docs/diagram.svg)
`;
const GUIDE = `# 指南

## 运行

\`\`\`sh
go run ./src
\`\`\`

## 目录

- \`src/main.go\`：入口
- \`src/lib/util.go\`：工具函数

返回 [README](../README.md)。
`;
const DIAGRAM = `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="60" viewBox="0 0 120 60">
  <rect x="4" y="16" width="40" height="28" rx="4" fill="gray"/>
  <rect x="76" y="16" width="40" height="28" rx="4" fill="gray"/>
  <path d="M44 30h32" stroke="gray" stroke-width="2"/>
</svg>
`;
const MAIN_V1 = `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`;
const MAIN_V2 = `package main

import (
	"fmt"
	"os"

	"example.com/webapp/src/lib"
)

func main() {
	fmt.Println(lib.Greet(os.Getenv("USER")))
}
`;
const MAIN_V3 = `package main

import (
	"fmt"
	"os"

	"example.com/webapp/src/lib"
)

func main() {
	for _, name := range lib.ListFiles(".") {
		fmt.Println(name)
	}
	fmt.Println(lib.Greet(os.Getenv("USER")))
}
`;
const UTIL_V1 = `package lib

// Greet 返回问候语。
func Greet(name string) string {
	return "hello, " + name
}
`;
const UTIL_V2 = `package lib

import "strings"

// Greet 返回问候语；名字为空时问候 world。
func Greet(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "world"
	}
	return "hello, " + name
}
`;
const UTIL_V3 = `package lib

import (
	"os"
	"strings"
)

// Greet 返回问候语；名字为空时问候 world。
func Greet(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "world"
	}
	return "hello, " + name
}

// ListFiles 列出目录下的文件名；出错时返回空。
func ListFiles(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}
`;

/** 与服务端 lfsInlineMax 一致：超过它的 LFS 对象不带 download=1 回 413。 */
const LFS_INLINE_MAX = 100 * 1024 * 1024;

const BANNER = `<svg xmlns="http://www.w3.org/2000/svg" width="480" height="120" viewBox="0 0 480 120">
  <rect x="4" y="4" width="472" height="112" rx="12" fill="none" stroke="gray" stroke-width="2"/>
  <rect x="28" y="36" width="48" height="48" rx="8" fill="gray"/>
  <rect x="96" y="44" width="200" height="12" rx="6" fill="gray"/>
  <rect x="96" y="66" width="140" height="10" rx="5" fill="gray"/>
</svg>
`;

/** Git LFS 对象；body 缺省 = 上游没有这个对象（lfs_missing）。 */
interface LfsObject {
	oid: string;
	size: number;
	body?: string;
}

/** 固定的 64 位十六进制 oid（形如 sha256）。 */
function lfsOid(seed: string): string {
	return fakeSha(`lfs\0${seed}`) + fakeSha(`lfs\0${seed}\0`).slice(0, 24);
}

/** 规范的三行 LFS 指针（仓库里存的就是它）。 */
function lfsPointer(o: LfsObject): string {
	return `version https://git-lfs.github.com/spec/v1\noid sha256:${o.oid}\nsize ${o.size}\n`;
}

const LFS_FILES: Record<string, LfsObject> = {
	"docs/media/banner.svg": {
		oid: lfsOid("banner.svg"),
		size: byteLength(BANNER),
		body: BANNER,
	},
	"docs/media/walkthrough.mp4": {
		oid: lfsOid("walkthrough.mp4"),
		size: 157_286_400,
		// 真实对象 150 MiB；mock 的 download=1 只给 16 字节
		body: "\0\0\0\x18ftypmp42\0\0\0\0",
	},
	"docs/media/missing.psd": { oid: lfsOid("missing.psd"), size: 8_388_608 },
};

/** 指针文本 → 对象。 */
const LFS_BY_POINTER = new Map(
	Object.values(LFS_FILES).map((o) => [lfsPointer(o), o]),
);

interface MockCommit {
	sha: string;
	subject: string;
	body: string;
	author: [string, string];
	ts: number;
	parents: string[];
	/** 路径 → 新内容（null = 删除） */
	changes: Record<string, string | null>;
	/** 关联到 Oryn 的一个线程（来源 push） */
	linked?: boolean;
}

const OWNER: [string, string] = ["Lynricsy", "lynricsy@oryn.example"];
const AGENT: [string, string] = ["Oryn Agent", "agent@oryn.example"];

/** 线性历史，旧到新；第 8 个只在 `feature/repo-browser` 上。 */
const COMMITS: MockCommit[] = (() => {
	const specs: Omit<MockCommit, "sha" | "parents">[] = [
		{
			subject: "initial commit",
			body: "",
			author: OWNER,
			ts: NOW - 9 * 24 * HOUR,
			changes: {
				"README.md": README_V1,
				".gitignore": "/bin/\n*.log\n",
			},
		},
		{
			subject: "Add Go entry point",
			body: "",
			author: OWNER,
			ts: NOW - 8 * 24 * HOUR,
			changes: { "src/main.go": MAIN_V1 },
		},
		{
			subject: "lib: add Greet helper",
			body: "把问候语抽到 src/lib，main 只负责输出。",
			author: OWNER,
			ts: NOW - 6 * 24 * HOUR,
			changes: { "src/lib/util.go": UTIL_V1, "src/main.go": MAIN_V2 },
		},
		{
			subject: "docs: add guide",
			body: "",
			author: AGENT,
			ts: NOW - 4 * 24 * HOUR,
			changes: { "docs/guide.md": GUIDE },
		},
		{
			subject: "docs: architecture diagram",
			body: "横幅、演示视频与设计稿放在 docs/media，用 Git LFS 存放。",
			author: AGENT,
			ts: NOW - 3 * 24 * HOUR,
			changes: {
				"docs/diagram.svg": DIAGRAM,
				...Object.fromEntries(
					Object.entries(LFS_FILES).map(([p, o]) => [p, lfsPointer(o)]),
				),
			},
			linked: true,
		},
		{
			subject: "README: link the guide and the diagram",
			body: "",
			author: OWNER,
			ts: NOW - 2 * 24 * HOUR,
			changes: { "README.md": README_V2 },
		},
		{
			subject: "lib: greet world when the name is empty",
			body: "USER 没设置时以前会输出 `hello, `。\n\n顺手 trim 掉首尾空白。\n\nCo-authored-by: Lynricsy <lynricsy@oryn.example>",
			author: AGENT,
			ts: NOW - 5 * HOUR,
			changes: { "src/lib/util.go": UTIL_V2 },
			linked: true,
		},
		{
			subject: "repo browser: list files in the working directory",
			body: "",
			author: AGENT,
			ts: NOW - 40 * MIN,
			changes: { "src/lib/util.go": UTIL_V3, "src/main.go": MAIN_V3 },
		},
	];
	const out: MockCommit[] = [];
	for (const s of specs) {
		const parent = out.at(-1)?.sha;
		out.push({
			...s,
			parents: parent ? [parent] : [],
			sha: fakeSha(`commit\0${parent ?? ""}\0${s.subject}`),
		});
	}
	return out;
})();

const BY_SHA = new Map(COMMITS.map((c) => [c.sha, c]));

function commitAt(i: number): MockCommit {
	const c = COMMITS[i];
	if (!c) throw new Error(`mock repo: no commit #${i}`);
	return c;
}

const BRANCHES: [string, MockCommit][] = [
	["main", commitAt(6)],
	["feature/repo-browser", commitAt(7)],
];
const TAGS: [string, MockCommit][] = [["v0.1.0", commitAt(5)]];

/** 基线 `main` 的最新提交 sha（提交列表第一个）：UI 审计与截图直接打开它的详情页。 */
export const MOCK_REPO_HEAD_SHA = commitAt(6).sha;

/** 从 tip 沿第一个父提交回溯，新到旧。 */
function history(tip: string): MockCommit[] {
	const out: MockCommit[] = [];
	for (let c = BY_SHA.get(tip); c; c = BY_SHA.get(c.parents[0] ?? ""))
		out.push(c);
	return out;
}

/** 提交时的全部文件（路径 → 内容）。 */
function snapshot(sha: string): Map<string, string> {
	const files = new Map<string, string>();
	for (const c of history(sha).toReversed())
		for (const [path, content] of Object.entries(c.changes)) {
			if (content === null) files.delete(path);
			else files.set(path, content);
		}
	return files;
}

function byteLength(s: string): number {
	return new TextEncoder().encode(s).length;
}

function summaryOf(c: MockCommit): Summary {
	return {
		sha: c.sha,
		shortSha: c.sha.slice(0, 7),
		subject: c.subject,
		authorName: c.author[0],
		authorEmail: c.author[1],
		ts: c.ts,
	};
}

function linesOf(s: string): string[] {
	if (s === "") return [];
	return (s.endsWith("\n") ? s.slice(0, -1) : s).split("\n");
}

/** 一个文件的改动：unified diff（掐掉共同的首尾，前后各留 3 行上下文）与行数。 */
function fileDiff(
	path: string,
	before: string | undefined,
	after: string | undefined,
) {
	const a = before === undefined ? [] : linesOf(before);
	const b = after === undefined ? [] : linesOf(after);
	let pre = 0;
	while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
	let suf = 0;
	while (
		suf < a.length - pre &&
		suf < b.length - pre &&
		a[a.length - 1 - suf] === b[b.length - 1 - suf]
	)
		suf++;
	const removed = a.slice(pre, a.length - suf);
	const added = b.slice(pre, b.length - suf);
	const ctxStart = Math.max(0, pre - 3);
	const ctxEnd = Math.min(suf, 3);
	const lead = a.slice(ctxStart, pre);
	const trail = a.slice(a.length - suf, a.length - suf + ctxEnd);
	const oldLen = lead.length + removed.length + trail.length;
	const newLen = lead.length + added.length + trail.length;
	const range = (start: number, len: number) =>
		len === 0 ? `${start},0` : `${start + 1},${len}`;
	const oldOid = before === undefined ? ZERO_SHA : fakeSha(`blob\0${before}`);
	const newOid = after === undefined ? ZERO_SHA : fakeSha(`blob\0${after}`);
	const header = [`diff --git a/${path} b/${path}`];
	if (before === undefined) header.push("new file mode 100644");
	if (after === undefined) header.push("deleted file mode 100644");
	header.push(
		`index ${oldOid.slice(0, 7)}..${newOid.slice(0, 7)}${before !== undefined && after !== undefined ? " 100644" : ""}`,
		before === undefined ? "--- /dev/null" : `--- a/${path}`,
		after === undefined ? "+++ /dev/null" : `+++ b/${path}`,
		`@@ -${range(ctxStart, oldLen)} +${range(ctxStart, newLen)} @@`,
	);
	const patch = `${[
		...header,
		...lead.map((l) => ` ${l}`),
		...removed.map((l) => `-${l}`),
		...added.map((l) => `+${l}`),
		...trail.map((l) => ` ${l}`),
	].join("\n")}\n`;
	return { patch, additions: added.length, deletions: removed.length };
}

function changesOf(c: MockCommit): FileChange[] {
	const prev = c.parents[0]
		? snapshot(c.parents[0])
		: new Map<string, string>();
	return Object.entries(c.changes)
		.toSorted(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
		.map(([path, content]) => {
			const before = prev.get(path);
			const d = fileDiff(path, before, content ?? undefined);
			return {
				path,
				status:
					content === null
						? "deleted"
						: before === undefined
							? "added"
							: "modified",
				additions: d.additions,
				deletions: d.deletions,
				binary: false,
				staged: "none",
			};
		});
}

function mimeOf(path: string): string {
	const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
	if (ext === "md") return "text/markdown; charset=utf-8";
	if (ext === "svg") return "image/svg+xml";
	if (ext === "png") return "image/png";
	if (ext === "mp4") return "video/mp4";
	if (ext === "psd") return "image/vnd.adobe.photoshop";
	return "text/plain; charset=utf-8";
}

/** blob 接口给出的内容：LFS 指针换成对象内容；对象缺失或超过预览上限（且不是 download）时为 undefined。 */
function blobContent(content: string, download: boolean): string | undefined {
	const lfs = LFS_BY_POINTER.get(content);
	if (!lfs) return content;
	if (lfs.body === undefined) return undefined;
	return lfs.size > LFS_INLINE_MAX && !download ? undefined : lfs.body;
}

/** 仓库内相对路径的校验（同服务端）：根为 ""；不能以 `/` 或 `-` 开头，不能有空段、`.`、`..`。 */
function validPath(path: string): boolean {
	if (path === "") return true;
	if (path.startsWith("-") || path.length > 4096 || path.includes("\0"))
		return false;
	return path.split("/").every((s) => s !== "" && s !== "." && s !== "..");
}

function validRef(ref: string): boolean {
	return (
		REF_RE.test(ref) &&
		!ref.includes("..") &&
		!ref.includes("//") &&
		!ref.endsWith("/") &&
		!ref.endsWith(".lock")
	);
}

export class MockRepo {
	/** 有仓库地址的项目的镜像状态（按项目 id，记下算状态时的仓库地址，改了地址就重来）。 */
	private readonly mirrors = new Map<string, { url: string; status: Status }>();

	private readonly deps: RepoDeps;

	constructor(deps: RepoDeps) {
		this.deps = deps;
	}

	private initial(p: Project): Status {
		const base = {
			kind: "mirror" as const,
			available: true,
			syncing: false,
			empty: false,
			cloneUrl: p.repoUrl,
		};
		if (p.id === DATA_PROJECT)
			return {
				...base,
				state: "ready",
				defaultBranch: "main",
				baseBranch: this.baseOf(p, "main"),
				syncedAt: NOW - 4 * MIN,
				sizeBytes: 3_145_728,
			};
		if (p.id === FAILING_PROJECT)
			return {
				...base,
				state: "error",
				lastError: {
					code: "auth_failed",
					message: `fatal: Authentication failed for '${p.repoUrl}.git/'`,
					at: NOW - HOUR,
				},
			};
		return { ...base, state: "absent" };
	}

	/** 项目 baseBranch 在镜像里存在时取它，否则默认分支。 */
	private baseOf(p: Project, defaultBranch: string): string {
		const branches = hasData(p) ? BRANCHES.map(([n]) => n) : [];
		return p.baseBranch && branches.includes(p.baseBranch)
			? p.baseBranch
			: defaultBranch;
	}

	private status(p: Project): Status {
		if (p.hosted) {
			this.mirrors.delete(p.id);
			return {
				kind: "hosted",
				state: "ready",
				available: true,
				syncing: false,
				empty: false,
				...(p.cloneUrl ? { cloneUrl: p.cloneUrl } : {}),
				defaultBranch: "main",
				baseBranch: p.baseBranch || "main",
				sizeBytes: 524_288,
			};
		}
		if (!p.repoUrl) {
			this.mirrors.delete(p.id);
			return {
				kind: "none",
				state: "absent",
				available: true,
				syncing: false,
				empty: false,
			};
		}
		let m = this.mirrors.get(p.id);
		if (!m || m.url !== p.repoUrl) {
			// 改了仓库地址：旧镜像作废，回到未克隆
			m = {
				url: p.repoUrl,
				status: m
					? {
							kind: "mirror",
							state: "absent",
							available: true,
							syncing: false,
							empty: false,
							cloneUrl: p.repoUrl,
						}
					: this.initial(p),
			};
			this.mirrors.set(p.id, m);
		}
		const s = m.status;
		return s.defaultBranch
			? { ...s, baseBranch: this.baseOf(p, s.defaultBranch) }
			: s;
	}

	private update(id: string, next: Partial<Status>) {
		const m = this.mirrors.get(id);
		if (!m) return;
		m.status = { ...m.status, ...next };
		this.deps.changed(id);
	}

	private sync(p: Project): Response {
		const s = this.status(p);
		if (s.kind === "none") return error(409, "no_repo", "项目没有仓库地址");
		if (s.kind === "hosted")
			return error(409, "repo_hosted", "托管仓库不需要同步");
		const id = p.id;
		if (s.state === "ready") {
			if (!s.syncing) {
				this.update(id, { syncing: true });
				setTimeout(
					() =>
						this.update(id, {
							syncing: false,
							syncedAt: Date.now(),
							lastError: undefined,
						}),
					SYNC_MS,
				);
			}
		} else if (s.state !== "cloning") {
			const failed = s.state === "error";
			this.update(id, { state: "cloning" });
			setTimeout(
				() =>
					failed && s.lastError
						? this.update(id, {
								state: "error",
								lastError: { ...s.lastError, at: Date.now() },
							})
						: this.update(id, {
								state: "ready",
								empty: true,
								defaultBranch: "main",
								syncedAt: Date.now(),
								sizeBytes: 24_576,
								lastError: undefined,
							}),
				SYNC_MS,
			);
		}
		return json(this.status(p), 202);
	}

	/**
	 * 关联到本项目的一条线程（push）；本项目没有线程就不关联。托管项目复用同一份提交，
	 * 不能把别的项目的线程挂到它的提交上。
	 */
	private threadOf(
		projectId: string,
		c: MockCommit,
	): Schemas["ProjectRepoThreadLink"] | undefined {
		if (!c.linked) return undefined;
		const t = [...this.deps.threads.values()].find(
			(x) => x.projectId === projectId,
		);
		return t ? { id: t.id, title: t.title ?? "", source: "push" } : undefined;
	}

	private commitOf(
		projectId: string,
		c: MockCommit,
	): Schemas["ProjectRepoCommit"] {
		const thread = this.threadOf(projectId, c);
		// 同服务端：正文里的 `Co-authored-by: 名字 <邮箱>` trailer
		const coAuthors = [
			...c.body.matchAll(/^co-authored-by:\s*(.+?)\s*<([^>]*)>\s*$/gim),
		].map(([, name = "", email = ""]) => ({ name, email }));
		return {
			...summaryOf(c),
			parents: c.parents,
			...(thread ? { thread } : {}),
			...(coAuthors.length ? { coAuthors } : {}),
		};
	}

	/** ref（空 = 基线）→ 提交 sha；不存在返回 undefined。 */
	private resolve(p: Project, s: Status, ref: string): string | undefined {
		const name = ref || s.baseBranch || "";
		if (!hasData(p) || s.empty) return undefined;
		const hit =
			BRANCHES.find(([n]) => n === name) ?? TAGS.find(([n]) => n === name);
		if (hit) return hit[1].sha;
		if (SHA_RE.test(name))
			return COMMITS.find((c) => c.sha.startsWith(name))?.sha;
		return undefined;
	}

	/**
	 * `<img src=…/repo/blob…>` 不经 fetch，mock 模式里没有服务器应答：`installMock` 拦下图片地址，同步换成这里给的
	 * `data:` 地址（内容同 blob 接口）。不是本仓库的 blob 地址或取不到内容时返回 null（原样加载）。
	 */
	blobDataUrl(raw: string): string | null {
		if (!URL.canParse(raw, location.origin)) return null;
		const url = new URL(raw, location.origin);
		const m = /^\/api\/v1\/projects\/([^/]+)\/repo\/blob$/.exec(url.pathname);
		const prj =
			m &&
			this.deps.projects().find((x) => x.id === decodeURIComponent(m[1] ?? ""));
		if (!prj) return null;
		const s = this.status(prj);
		const tip =
			s.state === "ready"
				? this.resolve(prj, s, url.searchParams.get("ref") ?? "")
				: undefined;
		const path = url.searchParams.get("path") ?? "";
		const content = tip ? snapshot(tip).get(path) : undefined;
		const data =
			content === undefined ? undefined : blobContent(content, false);
		if (data === undefined) return null;
		return `data:${mimeOf(path).replaceAll(" ", "")},${encodeURIComponent(data)}`;
	}

	async handle(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		const match = /^projects\/([^/]+)\/repo(?:\/(.+))?$/.exec(p);
		if (!match) return null;
		const id = decodeURIComponent(match[1] ?? "");
		const rest = match[2] ?? "";
		const prj = this.deps.projects().find((x) => x.id === id);
		if (!prj) return error(404, "not_found", "项目不存在");
		if (rest === "" && m === "GET") return json(this.status(prj));
		if (rest === "sync" && m === "POST") return this.sync(prj);
		if (m !== "GET") return null;
		const routes = /^(refs|commits|tree|blob|commits\/([^/]+)(\/diff)?)$/.exec(
			rest,
		);
		if (!routes) return null;
		const s = this.status(prj);
		if (s.kind === "none") return error(409, "no_repo", "项目没有仓库地址");
		if (s.state !== "ready")
			return error(409, "repo_not_ready", "仓库镜像还没有就绪");
		const q = url.searchParams;
		if (rest === "refs") return this.refs(prj, q);
		if (routes[2] !== undefined) {
			const sha = routes[2];
			if (!SHA_RE.test(sha)) return error(400, "invalid_request", "sha 无效");
			const c =
				hasData(prj) && !s.empty
					? COMMITS.find((x) => x.sha.startsWith(sha))
					: undefined;
			if (!c) return error(404, "commit_not_found", "提交不存在");
			return routes[3] ? this.diff(c, q) : this.commit(prj.id, c);
		}
		const ref = q.get("ref") ?? "";
		const path = q.get("path") ?? "";
		if (ref && !validRef(ref) && !SHA_RE.test(ref))
			return error(400, "invalid_request", "ref 无效");
		if (!validPath(path)) return error(400, "invalid_request", "path 无效");
		const tip = this.resolve(prj, s, ref);
		if (!tip) return error(404, "ref_not_found", "ref 不存在");
		const refName = ref || s.baseBranch || "";
		if (rest === "commits") return this.commits(prj.id, refName, tip, path, q);
		if (rest === "tree")
			return this.tree(refName, tip, path, q.get("lastCommits") !== "false");
		const download = q.get("download");
		return this.blob(
			req,
			ref,
			tip,
			path,
			download === "1" || download === "true",
		);
	}

	private refs(p: Project, q: URLSearchParams): Response {
		const k = q.get("kind");
		if (k !== "branch" && k !== "tag")
			return error(400, "invalid_request", "kind 必须是 branch 或 tag");
		const kind: Schemas["ProjectRepoRefKind"] = k;
		const limit = Math.min(
			Math.max(Number(q.get("limit") ?? 50) || 50, 1),
			200,
		);
		const off = Number(q.get("cursor") ?? 0) || 0;
		const s = this.status(p);
		const list =
			hasData(p) && !s.empty ? (kind === "branch" ? BRANCHES : TAGS) : [];
		const all: Schemas["ProjectRepoRef"][] = list
			.map(([name, c]) => ({
				name,
				kind,
				commit: summaryOf(c),
				default: kind === "branch" && name === s.defaultBranch,
				...(kind === "tag" ? { annotated: true } : {}),
			}))
			.toSorted(
				(a, b) =>
					Number(b.default) - Number(a.default) || b.commit.ts - a.commit.ts,
			);
		const page = all.slice(off, off + limit);
		return json({
			refs: page,
			...(off + limit < all.length ? { nextCursor: String(off + limit) } : {}),
		});
	}

	private commits(
		projectId: string,
		ref: string,
		tip: string,
		path: string,
		q: URLSearchParams,
	): Response {
		const limit = Math.min(
			Math.max(Number(q.get("limit") ?? 30) || 30, 1),
			100,
		);
		let head = tip;
		let off = 0;
		const cursor = q.get("cursor");
		if (cursor) {
			const cm = /^([0-9a-f]{40}):([0-9]{1,7})$/.exec(cursor);
			if (!cm) return error(400, "invalid_request", "cursor 无效");
			head = cm[1] ?? tip;
			off = Number(cm[2]);
		}
		const touches = (c: MockCommit) =>
			path === "" ||
			Object.keys(c.changes).some(
				(f) => f === path || f.startsWith(`${path}/`),
			);
		const all = history(head).filter(touches);
		const page = all.slice(off, off + limit);
		return json({
			ref,
			commit: head,
			commits: page.map((c) => this.commitOf(projectId, c)),
			...(off + limit < all.length
				? { nextCursor: `${head}:${off + limit}` }
				: {}),
			...(cursor ? {} : { total: all.length }),
		});
	}

	private commit(projectId: string, c: MockCommit): Response {
		const files = changesOf(c);
		const detail: Schemas["ProjectRepoCommitDetail"] = {
			...this.commitOf(projectId, c),
			body: c.body,
			committerName: c.author[0],
			committerEmail: c.author[1],
			committedAt: c.ts,
			files,
			additions: files.reduce((n, f) => n + (f.additions ?? 0), 0),
			deletions: files.reduce((n, f) => n + (f.deletions ?? 0), 0),
			truncated: false,
		};
		return json(detail);
	}

	private diff(c: MockCommit, q: URLSearchParams): Response {
		const path = q.get("path") ?? "";
		if (!validPath(path) || path === "")
			return error(400, "invalid_request", "path 无效");
		const prev = c.parents[0]
			? snapshot(c.parents[0])
			: new Map<string, string>();
		// 同服务端：这个提交没动该文件时 patch 为空
		const patch =
			path in c.changes
				? fileDiff(path, prev.get(path), c.changes[path] ?? undefined).patch
				: "";
		const body: Schemas["GitFileDiff"] = {
			path,
			kind: "text",
			patch,
			truncated: false,
		};
		return json(body);
	}

	private tree(
		ref: string,
		tip: string,
		path: string,
		lastCommits: boolean,
	): Response {
		const files = snapshot(tip);
		const hist = history(tip);
		const lastOf = (entryPath: string) => {
			const c = hist.find((x) =>
				Object.keys(x.changes).some(
					(f) => f === entryPath || f.startsWith(`${entryPath}/`),
				),
			);
			return c ? summaryOf(c) : undefined;
		};
		const nameOf = (p: string) => p.slice(p.lastIndexOf("/") + 1);
		const fileEntry = (p: string, content: string): TreeEntry => {
			const lfs = LFS_BY_POINTER.get(content);
			return {
				name: nameOf(p),
				path: p,
				type: "file",
				mode: 420,
				// LFS：size 是对象的真实大小，oid 仍是指针 blob 的
				size: lfs ? lfs.size : byteLength(content),
				oid: fakeSha(`blob\0${content}`),
				...(lfs ? { lfs: { oid: lfs.oid } } : {}),
			};
		};
		const dirEntry = (p: string): TreeEntry => ({
			name: nameOf(p),
			path: p,
			type: "dir",
			mode: 493,
			size: 0,
			oid: fakeSha(`tree\0${tip}\0${p}`),
		});
		const content = files.get(path);
		if (content !== undefined) {
			// 同服务端：LFS 对象缺失时带 error；对象超过预览上限或缺失时 mime 只按扩展名、不给 text
			const lfs = LFS_BY_POINTER.get(content);
			const missing = lfs !== undefined && lfs.body === undefined;
			const inline = !lfs || (!missing && lfs.size <= LFS_INLINE_MAX);
			const self: TreeEntry = {
				...fileEntry(path, content),
				mime: mimeOf(path),
				...(lfs && missing
					? { lfs: { oid: lfs.oid, error: "lfs_missing" as const } }
					: {}),
				...(inline ? { text: true } : {}),
			};
			const last = lastOf(path);
			const body: Schemas["ProjectRepoTree"] = {
				ref,
				commit: tip,
				path,
				self: last ? { ...self, lastCommit: last } : self,
				entries: [],
				truncated: false,
				lastCommitsComplete: true,
			};
			return json(body);
		}
		const prefix = path === "" ? "" : `${path}/`;
		const under = [...files.keys()].filter((f) => f.startsWith(prefix));
		if (path !== "" && under.length === 0)
			return error(404, "path_not_found", "路径不存在");
		const children = new Map<string, TreeEntry>();
		for (const f of under) {
			const rest = f.slice(prefix.length);
			const slash = rest.indexOf("/");
			const childPath = slash < 0 ? f : prefix + rest.slice(0, slash);
			if (children.has(childPath)) continue;
			children.set(
				childPath,
				slash < 0 ? fileEntry(f, files.get(f) ?? "") : dirEntry(childPath),
			);
		}
		const entries = [...children.values()]
			.map((e) => {
				const last = lastCommits ? lastOf(e.path) : undefined;
				return last ? { ...e, lastCommit: last } : e;
			})
			.toSorted(
				(a, b) =>
					Number(b.type === "dir") - Number(a.type === "dir") ||
					a.name.toLowerCase().localeCompare(b.name.toLowerCase()),
			);
		const body: Schemas["ProjectRepoTree"] = {
			ref,
			commit: tip,
			path,
			self: dirEntry(path),
			entries,
			truncated: false,
			lastCommitsComplete: true,
		};
		return json(body);
	}

	private blob(
		req: Request,
		ref: string,
		tip: string,
		path: string,
		download: boolean,
	): Response {
		if (path === "") return error(409, "not_a_file", "路径是目录");
		const files = snapshot(tip);
		const content = files.get(path);
		if (content === undefined) {
			const isDir = [...files.keys()].some((f) => f.startsWith(`${path}/`));
			return isDir
				? error(409, "not_a_file", "路径是目录")
				: error(404, "path_not_found", "路径不存在");
		}
		const lfs = LFS_BY_POINTER.get(content);
		if (lfs && lfs.body === undefined)
			return error(404, "lfs_missing", "找不到这个 LFS 对象");
		const data = blobContent(content, download);
		if (data === undefined)
			return error(
				413,
				"too_large",
				"Git LFS 对象超过 100 MiB，只能下载（download=1）",
			);
		const etag = `"${fakeSha(`blob\0${content}`)}"`;
		const cache = /^[0-9a-f]{40}$/.test(ref)
			? "private, max-age=31536000, immutable"
			: "private, no-cache";
		const headers = {
			ETag: etag,
			"Cache-Control": cache,
			"X-Content-Type-Options": "nosniff",
			"Content-Security-Policy": "sandbox",
		};
		if (req.headers.get("If-None-Match") === etag)
			return new Response(null, { status: 304, headers });
		return new Response(data, {
			headers: {
				...headers,
				"Content-Type":
					lfs && lfs.size > LFS_INLINE_MAX
						? "application/octet-stream"
						: mimeOf(path),
				"Content-Disposition": `attachment; filename="${path.slice(path.lastIndexOf("/") + 1)}"`,
			},
		});
	}
}
