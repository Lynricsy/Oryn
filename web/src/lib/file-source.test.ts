import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import { handleOrbFetch, resetOrbFs } from "../mock/orbfs";
import {
	agentConfigListing,
	agentConfigSource,
	orbFileSource,
	projectAgentConfigSource,
} from "./file-source";

const f = (path: string, size = 1, updatedAt = 1) => ({
	path,
	size,
	mode: 420,
	sha256: "0".repeat(64),
	updatedAt,
});

describe("agentConfigListing：由扁平列表合成目录树", () => {
	const files = [
		f("models.yml"),
		f("AGENTS.md"),
		f("skills/foo/SKILL.md", 10, 5),
		f("skills/bar/SKILL.md", 10, 9),
		f("skills/README.md"),
		f("agents/x.md"),
	];

	it("根目录：目录在前、不分大小写排序，目录只出现一次且取子项最新时间", () => {
		const root = agentConfigListing(files, "");
		expect(root.map((e) => [e.name, e.type])).toEqual([
			["agents", "dir"],
			["skills", "dir"],
			["AGENTS.md", "file"],
			["models.yml", "file"],
		]);
		expect(root.find((e) => e.name === "skills")?.mtime).toBe(9);
		expect(root.find((e) => e.name === "skills")?.path).toBe("skills");
	});

	it("子目录只列下一层，路径相对 omp-home 根", () => {
		expect(agentConfigListing(files, "skills").map((e) => e.path)).toEqual([
			"skills/bar",
			"skills/foo",
			"skills/README.md",
		]);
		expect(agentConfigListing(files, "skills/foo")).toMatchObject([
			{ path: "skills/foo/SKILL.md", type: "file", size: 10, text: true },
		]);
	});

	it("前缀相同但不在该目录下的文件不会混入", () => {
		expect(
			agentConfigListing([f("skills2/a.md"), f("skills/b.md")], "skills"),
		).toMatchObject([{ path: "skills/b.md" }]);
	});
});

describe("agentConfigSource", () => {
	let calls: { method: string; path: string }[];
	beforeEach(() => {
		calls = [];
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
				const req =
					input instanceof Request
						? input
						: new Request(new URL(String(input), "http://x"), init);
				const url = new URL(req.url);
				calls.push({
					method: req.method,
					path: decodeURIComponent(url.pathname),
				});
				if (url.pathname === "/api/v1/agent-config/files")
					return Response.json({
						files: [f("skills/a/SKILL.md"), f("skills/b.md"), f("skillsx.md")],
					});
				return new Response(null, { status: 204 });
			}),
		);
	});
	afterEach(() => vi.unstubAllGlobals());

	it("删除合成目录 = 删除其下全部文件，不碰同前缀的兄弟文件", async () => {
		await agentConfigSource().remove("skills");
		expect(
			calls.filter((c) => c.method === "DELETE").map((c) => c.path),
		).toEqual([
			"/api/v1/agent-config/files/skills/a/SKILL.md",
			"/api/v1/agent-config/files/skills/b.md",
		]);
	});

	it("overwrite=false 且已存在 → 409 exists，不发 PUT", async () => {
		await expect(
			agentConfigSource().write("skills/b.md", "x", { overwrite: false }),
		).rejects.toMatchObject({ status: 409, code: "exists" });
		expect(calls.some((c) => c.method === "PUT")).toBe(false);
	});

	it("不支持 mkdir / rename", () => {
		const s = agentConfigSource();
		expect(s.caps).toMatchObject({ mkdir: false, rename: false });
	});
});

describe("projectAgentConfigSource（M12.2）", () => {
	let calls: { method: string; raw: string; body: string }[];
	let writeResults: unknown[];
	beforeEach(() => {
		calls = [];
		writeResults = [];
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
				const req =
					input instanceof Request
						? input
						: new Request(new URL(String(input), "http://x"), init);
				const url = new URL(req.url);
				calls.push({
					method: req.method,
					raw: url.pathname,
					body: req.method === "PUT" ? await req.text() : "",
				});
				if (url.pathname === "/api/v1/projects/prj_A/agent-config/files")
					return Response.json({ files: [f("rules/a.md"), f("mcp.json")] });
				if (
					url.pathname ===
					"/api/v1/projects/prj_A/agent-config/files/rules%2Fa.md"
				)
					return new Response("rule a\n");
				if (req.method === "PUT")
					return Response.json({
						file: f("rules/b.md"),
						extractedSecrets: [],
						warnings: [],
					});
				return Response.json(
					{ code: "not_found", message: "项目不存在" },
					{ status: 404 },
				);
			}),
		);
	});
	afterEach(() => vi.unstubAllGlobals());

	it("id 与根标签独立于全局配置；列表、读写、删除都走项目接口，路径里的 / 编码为 %2F", async () => {
		const s = projectAgentConfigSource("prj_A", (r) => writeResults.push(r));
		expect(s.id).toBe("project-config:prj_A");
		expect(await s.roots()).toEqual([
			{ label: "files.roots.projectAgentConfig", path: "" },
		]);
		expect((await s.list("")).map((e) => e.path)).toEqual([
			"rules",
			"mcp.json",
		]);
		expect(await s.readText("rules/a.md")).toEqual({
			text: "rule a\n",
			truncated: false,
		});
		await s.write("rules/b.md", "b\n");
		await s.remove("rules");
		expect(s.downloadUrl("rules/a.md")).toBe(
			"/api/v1/projects/prj_A/agent-config/files/rules%2Fa.md",
		);
		expect(calls.filter((c) => c.method !== "GET")).toEqual([
			{
				method: "PUT",
				raw: "/api/v1/projects/prj_A/agent-config/files/rules%2Fb.md",
				body: "b\n",
			},
			{
				method: "DELETE",
				raw: "/api/v1/projects/prj_A/agent-config/files/rules%2Fa.md",
				body: "",
			},
		]);
		expect(writeResults).toHaveLength(1);
		expect(calls.some((c) => c.raw.startsWith("/api/v1/agent-config"))).toBe(
			false,
		);
	});

	it("overwrite=false 且项目里已存在 → 409 exists，不发 PUT", async () => {
		await expect(
			projectAgentConfigSource("prj_A").write("mcp.json", "{}", {
				overwrite: false,
			}),
		).rejects.toMatchObject({ status: 409, code: "exists" });
		expect(calls.some((c) => c.method === "PUT")).toBe(false);
	});

	it("项目不存在（列表 404）→ ApiError not_found；不存在的文件 stat → 404", async () => {
		await expect(
			projectAgentConfigSource("prj_gone").list(""),
		).rejects.toMatchObject({ status: 404, code: "not_found" });
		await expect(
			projectAgentConfigSource("prj_A").stat("rules/zzz.md"),
		).rejects.toMatchObject({ status: 404, code: "not_found" });
	});
});

describe("orbFileSource（对 mock orb 文件系统）", () => {
	const orb = "orb-test0000000000000000000001";
	beforeEach(() => {
		resetOrbFs();
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
				const req =
					input instanceof Request
						? input
						: new Request(new URL(String(input), location.origin), init);
				return (
					(await handleOrbFetch(req)) ?? new Response(null, { status: 404 })
				);
			}),
		);
	});
	afterEach(() => vi.unstubAllGlobals());

	it("roots：Workspace 在前、Home 在后", async () => {
		expect(await orbFileSource(orb, "T-1").roots()).toEqual([
			{ label: "files.roots.workspace", path: "/home/user/workspace/repo" },
			{ label: "files.roots.home", path: "/home/user" },
		]);
	});

	it("readText 超过上限时截断并带 etag；小文件不截断", async () => {
		const s = orbFileSource(orb, "T-1");
		const big = await s.readText("~/workspace/repo/logs/big.log", 1024);
		expect(big.truncated).toBe(true);
		expect(new TextEncoder().encode(big.text).length).toBeLessThanOrEqual(1024);
		const small = await s.readText("/home/user/notes.txt");
		expect(small).toMatchObject({
			text: "orb 里的随手笔记。\n",
			truncated: false,
		});
		expect(small.etag).toMatch(/^"\d+-\d+"$/);
	});

	it("截断处恰好落在多字节字符中间时不产生替换字符", async () => {
		// 「orb 里」：'里' 占 3 字节，从第 5 字节起；只读 6 字节会切开它
		const r = await orbFileSource(orb, "T-1").readText(
			"/home/user/notes.txt",
			6,
		);
		expect(r).toMatchObject({ text: "orb ", truncated: true });
	});

	it("$HOME 之外的路径 → 403 outside_home", async () => {
		await expect(orbFileSource(orb, "T-1").list("/etc")).rejects.toMatchObject({
			status: 403,
			code: "outside_home",
		});
	});

	it("旧版 orbd → ApiError orbd_outdated", async () => {
		const e = await orbFileSource("orb-old00000000000000000000e4", "T-1")
			.roots()
			.catch((x) => x);
		expect(e).toBeInstanceOf(ApiError);
		expect(e.code).toBe("orbd_outdated");
	});
});

describe("读取上限：服务端无视 Range 回整个大文件时不整份读入", () => {
	/**
	 * 远超上限的 200 响应体（有限长，没有上限的实现会整份读完而不是挂起），
	 * 记录被拉取的块数与是否被取消。
	 */
	function oversized(chunk = 64 * 1024, count = 64) {
		const stats = { pulls: 0, cancelled: false };
		const body = new ReadableStream<Uint8Array>({
			pull(c) {
				stats.pulls++;
				c.enqueue(new Uint8Array(chunk).fill(0x61));
				if (stats.pulls === count) c.close();
			},
			cancel() {
				stats.cancelled = true;
			},
		});
		return { stats, body };
	}
	afterEach(() => vi.unstubAllGlobals());

	it("orb readText：读满上限即取消读取并标记截断", async () => {
		const { stats, body } = oversized();
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => new Response(body, { status: 200 })),
		);
		const r = await orbFileSource(
			"orb-cap00000000000000000000001",
			"T-1",
		).readText("/home/user/huge.log", 100_000);
		expect(r.truncated).toBe(true);
		expect(r.text.length).toBe(100_000);
		expect(stats.cancelled).toBe(true);
		expect(stats.pulls).toBeLessThan(10);
	});

	it("agent 配置 readText 同样有上限", async () => {
		const { stats, body } = oversized();
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => new Response(body, { status: 200 })),
		);
		const r = await agentConfigSource().readText("big.md", 1000);
		expect(r).toMatchObject({ truncated: true });
		expect(r.text.length).toBe(1000);
		expect(stats.cancelled).toBe(true);
	});

	it("readBlob（整份预览）超过 64MiB → 413 too_large，并取消读取", async () => {
		const { stats, body } = oversized(1024 * 1024, 80);
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => new Response(body, { status: 200 })),
		);
		await expect(
			orbFileSource("orb-cap00000000000000000000001", "T-1").readBlob("/x.png"),
		).rejects.toMatchObject({ status: 413, code: "too_large" });
		expect(stats.cancelled).toBe(true);
		expect(stats.pulls).toBeLessThan(72);
	});
});

describe("orbFileSource.write（XHR）", () => {
	interface Sent {
		method: string;
		url: string;
		headers: Record<string, string>;
		body: unknown;
	}
	let sent: Sent[];
	let reply: { status: number; body: unknown };

	class FakeXhr {
		upload: { onprogress: ((e: ProgressEvent) => void) | null } = {
			onprogress: null,
		};
		status = 0;
		responseText = "";
		withCredentials = false;
		onload: (() => void) | null = null;
		onerror: (() => void) | null = null;
		onabort: (() => void) | null = null;
		private req: Sent = { method: "", url: "", headers: {}, body: null };
		open(method: string, url: string) {
			this.req.method = method;
			this.req.url = url;
		}
		setRequestHeader(k: string, v: string) {
			this.req.headers[k] = v;
		}
		getResponseHeader() {
			return null;
		}
		send(body: unknown) {
			this.req.body = body;
			sent.push(this.req);
			this.upload.onprogress?.({
				loaded: 2,
				total: 4,
				lengthComputable: true,
			} as ProgressEvent);
			this.status = reply.status;
			this.responseText = JSON.stringify(reply.body);
			queueMicrotask(() => this.onload?.());
		}
		abort() {
			this.onabort?.();
		}
	}

	beforeEach(() => {
		sent = [];
		vi.stubGlobal("XMLHttpRequest", FakeXhr);
	});
	afterEach(() => vi.unstubAllGlobals());

	it("带 CSRF、If-Match 与 overwrite，报告进度并返回 FsEntry", async () => {
		reply = {
			status: 200,
			body: {
				path: "/home/user/a.txt",
				name: "a.txt",
				type: "file",
				size: 4,
				mtime: 1,
				mode: 420,
				etag: '"2-4"',
			},
		};
		const progress: [number, number][] = [];
		const entry = await orbFileSource("orb-1", "T-1").write(
			"/home/user/a b.txt",
			"abcd",
			{
				etag: '"1-3"',
				overwrite: false,
				onProgress: (s, t) => progress.push([s, t]),
			},
		);
		expect(entry.etag).toBe('"2-4"');
		expect(sent[0]).toMatchObject({
			method: "PUT",
			// 带 thread（runner 的 orb 必须带，M11b）
			url: "/api/v1/orbs/orb-1/fs/file?thread=T-1&path=%2Fhome%2Fuser%2Fa%20b.txt&overwrite=false",
			headers: {
				"X-Oryn-CSRF": "1",
				"If-Match": '"1-3"',
				"Content-Type": "application/octet-stream",
			},
			body: "abcd",
		});
		expect(progress).toEqual([
			[2, 4],
			[4, 4],
		]);
	});

	it("412 → ApiError modified", async () => {
		reply = {
			status: 412,
			body: { code: "modified", message: "etag mismatch" },
		};
		await expect(
			orbFileSource("orb-1", "T-1").write("/home/user/a.txt", "x", {
				etag: '"1-1"',
			}),
		).rejects.toMatchObject({
			status: 412,
			code: "modified",
		});
	});
});

describe("orbFileSource 停机预览（offline）", () => {
	const orb = "orb-test0000000000000000000001";
	let urls: URL[];
	beforeEach(() => {
		resetOrbFs();
		urls = [];
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
				const req =
					input instanceof Request
						? input
						: new Request(new URL(String(input), location.origin), init);
				urls.push(new URL(req.url));
				return (
					(await handleOrbFetch(req)) ?? new Response(null, { status: 404 })
				);
			}),
		);
	});
	afterEach(() => vi.unstubAllGlobals());

	const offlineFlag = (u: URL | string) =>
		new URL(String(u), location.origin).searchParams.get("offline");

	it("roots / list / stat / 读取 / 下载地址都带 offline，来源 id 不变", async () => {
		const s = orbFileSource(orb, "T-1", { offline: true });
		expect(s.id).toBe(`orb:${orb}`);
		await s.roots();
		await s.list("/home/user");
		await s.stat("/home/user/notes.txt");
		await s.readText("/home/user/notes.txt");
		await s.readBlob("/home/user/notes.txt");
		expect(urls.map((u) => u.pathname.split("/").pop())).toEqual([
			"roots",
			"list",
			"stat",
			"file",
			"file",
		]);
		for (const u of urls) {
			expect(["1", "true"]).toContain(offlineFlag(u));
			expect(u.searchParams.get("thread")).toBe("T-1");
		}
		expect(offlineFlag(s.downloadUrl("/home/user/notes.txt") ?? "")).toBe("1");
	});

	it("在线来源不带 offline", async () => {
		const s = orbFileSource(orb, "T-1");
		await s.list("/home/user");
		expect(offlineFlag(urls[0] ?? "")).toBeNull();
		expect(offlineFlag(s.downloadUrl("/home/user/notes.txt") ?? "")).toBeNull();
		expect(s.caps.write).toBe(true);
	});

	it("只读：caps 全关（可下载），写操作以 409 read_only 拒绝且不发请求", async () => {
		const s = orbFileSource(orb, "T-1", { offline: true });
		expect(s.caps).toEqual({
			write: false,
			mkdir: false,
			rename: false,
			upload: false,
			download: true,
		});
		const ops = [
			s.write("/home/user/a.txt", "x"),
			s.mkdir("/home/user/d"),
			s.rename("/home/user/notes.txt", "/home/user/n2.txt"),
			s.remove("/home/user/notes.txt"),
		];
		for (const op of ops)
			await expect(op).rejects.toMatchObject({
				status: 409,
				code: "read_only",
			});
		expect(urls).toEqual([]);
	});
});
