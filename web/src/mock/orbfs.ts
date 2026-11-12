/**
 * mock 模式的 orb 文件系统与 git 替身：每台 orb 一份内存文件树（`$HOME=/home/user`）与一组 git 假数据。
 * `MockServer.handleFetch` 开头调用 `handleOrbFetch`；非 `orbs/{id}/fs|git/*` 的请求返回 null。
 * 特例：id 以 `e4` 结尾的 orb 模拟旧版 orbd（409 `orbd_outdated`），以 `e8` 结尾的 orb 工作目录不是仓库。
 */
import type { components } from "../api/schema";

type S = components["schemas"];

const HOME = "/home/user";
const REPO = `${HOME}/workspace/repo`;
const MAX_FILE = 512 * 1024 * 1024;

type Node =
	| { type: "dir"; mtime: number; mode: number }
	| { type: "file"; data: Uint8Array; mtime: number; mode: number };

type GitFile = S["GitFileChange"] & { patch: string };

interface OrbFs {
	nodes: Map<string, Node>;
	workdir: string;
	git: {
		files: GitFile[];
		commits: (S["GitCommit"] & { files: GitFile[] })[];
	};
}

const enc = new TextEncoder();
const NOW = Date.now();

function json(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

function fail(status: number, code: string, message = code): Response {
	return json({ code, message }, status);
}

// 1×1 PNG（橙色像素）
const PIXEL_PNG = Uint8Array.from(
	atob(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z/C/HgAGgwJ/lK3Q6wAAAABJRU5ErkJggg==",
	),
	(c) => c.charCodeAt(0),
);

const LOGO_SVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><circle cx="32" cy="32" r="28" fill="#e8763a"/><circle cx="32" cy="32" r="12" fill="#fff"/></svg>\n`;

function seedFiles(): Record<string, string | Uint8Array> {
	const bin = new Uint8Array(4096);
	for (let i = 0; i < bin.length; i++) bin[i] = (i * 37) & 0xff;
	return {
		[`${HOME}/.bashrc`]: "export PS1='\\u@orb:\\w$ '\nalias ll='ls -la'\n",
		[`${HOME}/notes.txt`]: "orb 里的随手笔记。\n",
		[`${REPO}/README.md`]:
			"# Oryn\n\n为 omp 做的高级环境系统与跨平台客户端。\n\n- orb：每线程一台隔离机器\n- 终端 / 文件 / 改动面板\n",
		[`${REPO}/src/main.ts`]:
			'import { greet } from "./util";\n\nexport function main(): void {\n\tconsole.log(greet("oryn"));\n}\n\nmain();\n',
		[`${REPO}/src/util.ts`]:
			'export function greet(name: string): string {\n\treturn "hello, " + name;\n}\n',
		[`${REPO}/docs/guide.md`]: "# 指南\n\n见 README。\n",
		[`${REPO}/assets/logo.svg`]: LOGO_SVG,
		[`${REPO}/assets/pixel.png`]: PIXEL_PNG,
		[`${REPO}/data.bin`]: bin,
		[`${REPO}/logs/big.log`]: Array.from(
			{ length: 60_000 },
			(_, i) =>
				`${new Date(NOW - (60_000 - i) * 1000).toISOString()} line ${i}`,
		).join("\n"),
		[`${REPO}/.gitignore`]: "node_modules/\ndist/\n",
	};
}

function patchOf(
	path: string,
	removed: string[],
	added: string[],
	oldPath?: string,
): string {
	const a = oldPath ?? path;
	return [
		`diff --git a/${a} b/${path}`,
		"index 3b18e51..a1c2d3e 100644",
		`--- a/${a}`,
		`+++ b/${path}`,
		`@@ -1,${removed.length + 1} +1,${added.length + 1} @@`,
		...removed.map((l) => `-${l}`),
		...added.map((l) => `+${l}`),
		" ",
		"",
	].join("\n");
}

function seedGit(): OrbFs["git"] {
	const f = (
		path: string,
		status: GitFile["status"],
		staged: GitFile["staged"],
		removed: string[],
		added: string[],
		extra: Partial<GitFile> = {},
	): GitFile => ({
		path,
		status,
		staged,
		binary: false,
		additions: added.length,
		deletions: removed.length,
		patch: patchOf(path, removed, added, extra.oldPath),
		...extra,
	});
	const files: GitFile[] = [
		f(
			"README.md",
			"modified",
			"full",
			["- orb：每线程一台机器"],
			["- orb：每线程一台隔离机器", "- 终端 / 文件 / 改动面板"],
		),
		f(
			"src/main.ts",
			"modified",
			"partial",
			["\tconsole.log(greet());"],
			['\tconsole.log(greet("oryn"));'],
		),
		f(
			"src/util.ts",
			"added",
			"none",
			[],
			[
				"export function greet(name: string): string {",
				'\treturn "hello, " + name;',
				"}",
			],
		),
		f("docs/guide.md", "renamed", "none", ["# Guide"], ["# 指南"], {
			oldPath: "docs/old-guide.md",
		}),
		f("legacy.txt", "deleted", "none", ["obsolete"], []),
		{
			path: "data.bin",
			status: "modified",
			staged: "none",
			binary: true,
			patch: "",
		},
		f("notes/todo.md", "untracked", "none", [], ["- [ ] 冻结与唤醒验收"]),
	];
	const commits = [
		{
			sha: "9f1c2b7e4a3d5c6b7a8e9f0d1c2b3a4e5f6a7b8c",
			shortSha: "9f1c2b7",
			subject: "feat: 文件面板",
			authorName: "Oryn Agent",
			authorEmail: "agent@oryn.dev",
			ts: NOW - 40 * 60_000,
			files: [
				f("src/files.ts", "added", "none", [], ["export const files = true;"]),
			],
		},
		{
			sha: "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b",
			shortSha: "1a2b3c4",
			subject: "docs: 更新 README",
			authorName: "Lynricsy",
			authorEmail: "lyn@example.com",
			ts: NOW - 3 * 3_600_000,
			files: [f("README.md", "modified", "none", ["# oryn"], ["# Oryn"])],
		},
	];
	return { files, commits };
}

const orbs = new Map<string, OrbFs>();

function orbFs(orbId: string): OrbFs {
	let fs = orbs.get(orbId);
	if (fs) return fs;
	const nodes = new Map<string, Node>();
	const dir = (p: string) => {
		for (
			let cur = p;
			cur.length >= HOME.length;
			cur = cur.slice(0, cur.lastIndexOf("/"))
		)
			if (!nodes.has(cur))
				nodes.set(cur, { type: "dir", mtime: NOW - 86_400_000, mode: 0o755 });
	};
	dir(HOME);
	dir(`${HOME}/.oryn`);
	for (const [p, content] of Object.entries(seedFiles())) {
		dir(p.slice(0, p.lastIndexOf("/")));
		nodes.set(p, {
			type: "file",
			data: typeof content === "string" ? enc.encode(content) : content,
			mtime: NOW - 3_600_000,
			mode: 0o644,
		});
	}
	fs = {
		nodes,
		workdir: orbId.endsWith("e8") ? `${HOME}/workspace` : REPO,
		git: seedGit(),
	};
	orbs.set(orbId, fs);
	return fs;
}

/** 测试用：清空全部 orb 的内存状态。 */
export function resetOrbFs(): void {
	orbs.clear();
}

function normalize(p: string): string | null {
	if (!p) return null;
	const abs =
		p === "~" ? HOME : p.startsWith("~/") ? `${HOME}/${p.slice(2)}` : p;
	if (!abs.startsWith("/")) return null;
	const out: string[] = [];
	for (const seg of abs.split("/")) {
		if (!seg || seg === ".") continue;
		if (seg === "..") out.pop();
		else out.push(seg);
	}
	return `/${out.join("/")}`;
}

function etagOf(n: Node & { type: "file" }): string {
	return `"${n.mtime * 1_000_000}-${n.data.length}"`;
}

const MIME: Record<string, string> = {
	md: "text/markdown; charset=utf-8",
	ts: "text/plain; charset=utf-8",
	txt: "text/plain; charset=utf-8",
	log: "text/plain; charset=utf-8",
	svg: "image/svg+xml",
	png: "image/png",
	bin: "application/octet-stream",
};

function isText(data: Uint8Array): boolean {
	const head = data.subarray(0, 8192);
	if (head.includes(0)) return false;
	try {
		new TextDecoder("utf-8", { fatal: true }).decode(head, { stream: true });
		return true;
	} catch {
		return false;
	}
}

function entry(path: string, n: Node, withType = false): S["FsEntry"] {
	const name = path === "/" ? "/" : path.slice(path.lastIndexOf("/") + 1);
	if (n.type === "dir")
		return { path, name, type: "dir", size: 0, mtime: n.mtime, mode: n.mode };
	const ext = name.includes(".") ? (name.split(".").pop() ?? "") : "";
	const text = isText(n.data);
	return {
		path,
		name,
		type: "file",
		size: n.data.length,
		mtime: n.mtime,
		mode: n.mode,
		etag: etagOf(n),
		...(withType
			? {
					mime: (
						MIME[ext] ??
						(text ? "text/plain; charset=utf-8" : "application/octet-stream")
					).split(";")[0],
					text,
				}
			: {}),
	};
}

function children(fs: OrbFs, dir: string): string[] {
	const prefix = dir === "/" ? "/" : `${dir}/`;
	return [...fs.nodes.keys()].filter(
		(p) =>
			p.startsWith(prefix) &&
			p !== dir &&
			!p.slice(prefix.length).includes("/"),
	);
}

async function handleFs(
	fs: OrbFs,
	op: string,
	req: Request,
	url: URL,
): Promise<Response> {
	const m = req.method.toUpperCase();
	if (op === "roots" && m === "GET")
		return json({ home: HOME, workdir: fs.workdir });
	let body: Record<string, unknown> = {};
	if (m === "POST") {
		try {
			body = (await req.json()) as Record<string, unknown>;
		} catch {
			return fail(400, "invalid_request");
		}
	}
	const resolve = (raw: unknown, mutate: boolean): string | Response => {
		const p = normalize(typeof raw === "string" ? raw : "");
		if (!p) return fail(400, "invalid_path");
		if (p !== HOME && !p.startsWith(`${HOME}/`))
			return fail(403, "outside_home");
		if (
			mutate &&
			(p === HOME || p === `${HOME}/.oryn` || p.startsWith(`${HOME}/.oryn/`))
		)
			return fail(403, "protected_path");
		return p;
	};
	const qpath = url.searchParams.get("path");

	if (op === "list" && m === "GET") {
		const p = resolve(qpath, false);
		if (typeof p !== "string") return p;
		const n = fs.nodes.get(p);
		if (!n) return fail(404, "not_found");
		if (n.type !== "dir") return fail(409, "not_a_directory");
		const entries = children(fs, p)
			.map((c) => entry(c, fs.nodes.get(c) as Node))
			.sort((a, b) =>
				a.type !== b.type
					? a.type === "dir"
						? -1
						: 1
					: a.name.localeCompare(b.name, undefined, { sensitivity: "base" }),
			);
		return json({ path: p, entries, truncated: false });
	}
	if (op === "stat" && m === "GET") {
		const p = resolve(qpath, false);
		if (typeof p !== "string") return p;
		const n = fs.nodes.get(p);
		return n ? json(entry(p, n, true)) : fail(404, "not_found");
	}
	if (op === "file" && m === "GET") {
		const p = resolve(qpath, false);
		if (typeof p !== "string") return p;
		const n = fs.nodes.get(p);
		if (!n) return fail(404, "not_found");
		if (n.type === "dir") return fail(409, "is_a_directory");
		const headers: Record<string, string> = {
			"Content-Type": "application/octet-stream",
			ETag: etagOf(n),
			"Content-Disposition": `attachment; filename*=UTF-8''${encodeURIComponent(p.slice(p.lastIndexOf("/") + 1))}`,
		};
		const range = /^bytes=(\d+)-(\d*)$/.exec(req.headers.get("Range") ?? "");
		if (range) {
			const size = n.data.length;
			const start = Number(range[1]);
			const end = Math.min(range[2] ? Number(range[2]) : size - 1, size - 1);
			if (start >= size) return new Response(null, { status: 416, headers });
			return new Response(n.data.slice(start, end + 1), {
				status: 206,
				headers: {
					...headers,
					"Content-Range": `bytes ${start}-${end}/${size}`,
				},
			});
		}
		return new Response(n.data.slice(), { status: 200, headers });
	}
	if (op === "file" && m === "PUT") {
		const p = resolve(qpath, true);
		if (typeof p !== "string") return p;
		const parent = fs.nodes.get(p.slice(0, p.lastIndexOf("/")) || "/");
		if (parent?.type !== "dir") return fail(409, "not_a_directory", "parent");
		const cur = fs.nodes.get(p);
		if (cur?.type === "dir") return fail(409, "is_a_directory");
		if (cur && url.searchParams.get("overwrite") === "false")
			return fail(409, "exists");
		const ifMatch = req.headers.get("If-Match");
		if (ifMatch && (!cur || etagOf(cur) !== ifMatch))
			return fail(412, "modified");
		const data = new Uint8Array(await req.arrayBuffer());
		if (data.length > MAX_FILE) return fail(413, "too_large");
		const n: Node = {
			type: "file",
			data,
			mtime: Math.max(Date.now(), (cur?.mtime ?? 0) + 1),
			mode: cur?.mode ?? 0o644,
		};
		fs.nodes.set(p, n);
		return json(entry(p, n));
	}
	if (op === "mkdir" && m === "POST") {
		const p = resolve(body.path, true);
		if (typeof p !== "string") return p;
		const cur = fs.nodes.get(p);
		if (cur && cur.type !== "dir") return fail(409, "exists");
		for (
			let c = p;
			c.length > HOME.length;
			c = c.slice(0, c.lastIndexOf("/"))
		) {
			const n = fs.nodes.get(c);
			if (n && n.type !== "dir") return fail(409, "not_a_directory");
			if (!n) fs.nodes.set(c, { type: "dir", mtime: Date.now(), mode: 0o755 });
		}
		return json(entry(p, fs.nodes.get(p) as Node));
	}
	if (op === "rename" && m === "POST") {
		const from = resolve(body.from, true);
		if (typeof from !== "string") return from;
		const to = resolve(body.to, true);
		if (typeof to !== "string") return to;
		if (from === fs.workdir) return fail(403, "protected_path");
		if (!fs.nodes.has(from)) return fail(404, "not_found");
		if (fs.nodes.has(to) && body.overwrite !== true) return fail(409, "exists");
		if (!fs.nodes.has(to.slice(0, to.lastIndexOf("/"))))
			return fail(409, "not_a_directory");
		for (const [k, v] of [...fs.nodes]) {
			if (k === from || k.startsWith(`${from}/`)) {
				fs.nodes.delete(k);
				fs.nodes.set(to + k.slice(from.length), v);
			}
		}
		return json(entry(to, fs.nodes.get(to) as Node));
	}
	if (op === "delete" && m === "POST") {
		const p = resolve(body.path, true);
		if (typeof p !== "string") return p;
		if (p === fs.workdir) return fail(403, "protected_path");
		for (const k of [...fs.nodes.keys()])
			if (k === p || k.startsWith(`${p}/`)) fs.nodes.delete(k);
		return new Response(null, { status: 204 });
	}
	return fail(404, "not_found", `mock: ${m} fs/${op}`);
}

function publicFile({ patch: _patch, ...f }: GitFile): S["GitFileChange"] {
	return f;
}

async function handleGit(
	fs: OrbFs,
	op: string,
	req: Request,
	url: URL,
): Promise<Response> {
	const m = req.method.toUpperCase();
	const root = url.searchParams.get("root");
	const inRepo = (r: string | null | undefined) => {
		const p = normalize(r ?? fs.workdir);
		return p !== null && (p === REPO || p.startsWith(`${REPO}/`));
	};
	const base: S["GitBase"] = {
		name: "main",
		ref: "refs/remotes/origin/main",
		mergeBase: "0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e",
		approximate: false,
	};
	if (op === "status" && m === "GET") {
		if (!inRepo(root))
			return json({
				repo: false,
				candidates: [REPO],
				scope: "all",
				files: [],
				truncated: false,
			} satisfies S["GitStatus"]);
		const scope = (url.searchParams.get("scope") ?? "all") as S["GitScope"];
		let files: GitFile[];
		if (scope === "commit") {
			const c = fs.git.commits.find((x) =>
				x.sha.startsWith(url.searchParams.get("commit") ?? "-"),
			);
			if (!c) return fail(500, "git_failed", "unknown commit");
			files = c.files;
		} else if (scope === "uncommitted") files = fs.git.files;
		else files = [...fs.git.commits.flatMap((c) => c.files), ...fs.git.files];
		const byPath = new Map(files.map((f) => [f.path, f]));
		return json({
			repo: true,
			root: REPO,
			branch: "oryn/files-panel",
			head: fs.git.commits[0]?.sha,
			base,
			scope,
			files: [...byPath.values()].map(publicFile),
			truncated: false,
		} satisfies S["GitStatus"]);
	}
	if (!inRepo(root) && op !== "commits") return fail(409, "not_a_repo");
	if (op === "diff" && m === "GET") {
		const path = url.searchParams.get("path") ?? "";
		const scope = url.searchParams.get("scope");
		const pool =
			scope === "commit"
				? (fs.git.commits.find((c) =>
						c.sha.startsWith(url.searchParams.get("commit") ?? "-"),
					)?.files ?? [])
				: [...fs.git.files, ...fs.git.commits.flatMap((c) => c.files)];
		const f = pool.find((x) => x.path === path);
		if (!f)
			return json({
				path,
				kind: "text",
				patch: "",
				truncated: false,
			} satisfies S["GitFileDiff"]);
		return json({
			path: f.path,
			oldPath: f.oldPath,
			kind: f.binary ? "binary" : "text",
			patch: f.patch,
			truncated: false,
		} satisfies S["GitFileDiff"]);
	}
	if (op === "commits" && m === "GET") {
		if (!inRepo(root)) return fail(409, "not_a_repo");
		return json({
			commits: fs.git.commits.map(({ files: _f, ...c }) => c),
			truncated: false,
		} satisfies S["GitCommitList"]);
	}
	if ((op === "stage" || op === "unstage") && m === "POST") {
		const body = (await req.json()) as S["GitPathsRequest"];
		for (const f of fs.git.files)
			if (body.paths.includes(f.path))
				f.staged = op === "stage" ? "full" : "none";
		return new Response(null, { status: 204 });
	}
	return fail(404, "not_found", `mock: ${m} git/${op}`);
}

const ROUTE = /^\/api\/v1\/orbs\/([^/]+)\/(fs|git)\/([a-z]+)$/;

/** orb 文件 / git 接口的 mock 应答；其它请求返回 null。 */
export async function handleOrbFetch(req: Request): Promise<Response | null> {
	const url = new URL(
		req.url,
		globalThis.location?.origin ?? "http://localhost",
	);
	const match = ROUTE.exec(url.pathname);
	if (!match) return null;
	const [, rawId = "", area, op = ""] = match;
	const orbId = decodeURIComponent(rawId);
	if (orbId.endsWith("e4"))
		return fail(409, "orbd_outdated", "orbd does not support fs/git");
	const fs = orbFs(orbId);
	return area === "fs"
		? handleFs(fs, op, req, url)
		: handleGit(fs, op, req, url);
}
