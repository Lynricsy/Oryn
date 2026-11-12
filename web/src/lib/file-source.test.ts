import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import { handleOrbFetch, resetOrbFs } from "../mock/orbfs";
import {
	agentConfigListing,
	agentConfigSource,
	orbFileSource,
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
		expect(await orbFileSource(orb).roots()).toEqual([
			{ label: "files.roots.workspace", path: "/home/user/workspace/repo" },
			{ label: "files.roots.home", path: "/home/user" },
		]);
	});

	it("readText 超过上限时截断并带 etag；小文件不截断", async () => {
		const s = orbFileSource(orb);
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
		const r = await orbFileSource(orb).readText("/home/user/notes.txt", 6);
		expect(r).toMatchObject({ text: "orb ", truncated: true });
	});

	it("$HOME 之外的路径 → 403 outside_home", async () => {
		await expect(orbFileSource(orb).list("/etc")).rejects.toMatchObject({
			status: 403,
			code: "outside_home",
		});
	});

	it("旧版 orbd → ApiError orbd_outdated", async () => {
		const e = await orbFileSource("orb-old00000000000000000000e4")
			.roots()
			.catch((x) => x);
		expect(e).toBeInstanceOf(ApiError);
		expect(e.code).toBe("orbd_outdated");
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
		const entry = await orbFileSource("orb-1").write(
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
			url: "/api/v1/orbs/orb-1/fs/file?path=%2Fhome%2Fuser%2Fa%20b.txt&overwrite=false",
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
			orbFileSource("orb-1").write("/home/user/a.txt", "x", { etag: '"1-1"' }),
		).rejects.toMatchObject({
			status: 412,
			code: "modified",
		});
	});
});
