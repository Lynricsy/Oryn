/**
 * mock 后端的全局助手文件库（M13）：`/api/v1/assistant/files*`，格式与 orynd（`internal/api/library.go`）一致。
 * - 上传（`POST ?name=&threadId=`）先是未绑定状态：列表不显示，随消息发送（`bind`）后才出现；
 * - 列表按 `threadId`（该对话自己的 + 库内共享的）与 `scope` 过滤，新的在前；
 * - 内容接口支持单段 `Range`（预览只读前 2MiB）。
 * `MockServer.handleFetch` 调 `handle`，不归这里的请求返回 null。
 */
import type { Schemas, Thread } from "../api/client";
import { M9_IDS, NOW } from "./fixtures";

type AssistantFile = Schemas["AssistantFile"];

const MIN = 60_000;
const MAX_BYTES = 100 * 1024 * 1024;

const MIME_BY_EXT: Record<string, string> = {
	png: "image/png",
	jpg: "image/jpeg",
	jpeg: "image/jpeg",
	gif: "image/gif",
	webp: "image/webp",
	svg: "image/svg+xml",
	pdf: "application/pdf",
	json: "application/json",
	csv: "text/csv; charset=utf-8",
	txt: "text/plain; charset=utf-8",
	md: "text/markdown; charset=utf-8",
};

function mimeOf(name: string): string {
	const ext = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
	return MIME_BY_EXT[ext] ?? "application/octet-stream";
}

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

interface Stored {
	meta: AssistantFile;
	/** 字节（不用 Blob：测试环境里 jsdom 的 Blob 不能直接作响应体） */
	data: Uint8Array<ArrayBuffer>;
	/** 随消息发送过（或由助手 / orb 存入）；未绑定的上传不进列表 */
	bound: boolean;
}

/** 夹具：本对话的上传与助手保存的文件、orb 存入的共享文件。 */
function seed(): Stored[] {
	const file = (
		meta: Omit<AssistantFile, "mime" | "size" | "sha256">,
		text: string,
	): Stored => {
		const data = new TextEncoder().encode(text);
		return {
			meta: {
				...meta,
				mime: mimeOf(meta.name),
				size: data.byteLength,
				sha256: "0".repeat(64),
			},
			data,
			bound: true,
		};
	};
	return [
		file(
			{
				id: "afl_01JDM000000000000000LIB001",
				name: "周报-11月第2周.md",
				scope: "conversation",
				threadId: M9_IDS.assistant,
				source: "upload",
				createdAt: NOW - 3 * 60 * MIN,
			},
			"# 周报\n\n- 完成 M12 的配置下发\n- 文件库面板评审中\n",
		),
		file(
			{
				id: "afl_01JDM000000000000000LIB002",
				name: "等待中的线程.csv",
				scope: "conversation",
				threadId: M9_IDS.assistant,
				source: "assistant",
				createdAt: NOW - 40 * MIN,
			},
			"thread,state,since\nScheduleDialog 预览防抖,working,09:12\nM9 截图矩阵,idle,08:47\n",
		),
		file(
			{
				id: "afl_01JDM000000000000000LIB003",
				name: "shots-index.json",
				scope: "shared",
				source: "orb",
				sourceThreadId: M9_IDS.spaceIdle,
				createdAt: NOW - 20 * MIN,
			},
			'{\n  "shots": ["assistant-light-1280.png", "assistant-dark-390.png"]\n}\n',
		),
	];
}

export class MockLibrary {
	private readonly files = new Map<string, Stored>(
		seed().map((f) => [f.meta.id, f]),
	);
	private seq = 0;
	private readonly threads: Map<string, Thread>;

	constructor(threads: Map<string, Thread>) {
		this.threads = threads;
	}

	/**
	 * 随消息发送（`orynFiles`）：绑定到对话（与 `BindAssistantFile` 相同：只在未归属、同对话或共享时成功），
	 * 返回附件块的行（沙箱路径同 `library.SandboxPath`）；有文件不存在或属于别的对话时返回 null。
	 */
	deliver(threadId: string, ids: unknown): string[] | null {
		if (!Array.isArray(ids)) return [];
		const picked: Stored[] = [];
		for (const id of ids) {
			const f = this.files.get(String(id));
			if (
				!f ||
				(f.meta.threadId &&
					f.meta.threadId !== threadId &&
					f.meta.scope !== "shared")
			)
				return null;
			picked.push(f);
		}
		return picked.map((f) => {
			f.bound = true;
			if (!f.meta.threadId) f.meta = { ...f.meta, threadId };
			const { id, name, mime, size, scope } = f.meta;
			const path =
				scope === "shared"
					? `~/library/shared/${id}/${name}`
					: `~/library/threads/${f.meta.threadId}/${id}/${name}`;
			return `- ${path} (${mime}, ${size} bytes)`;
		});
	}

	private newId(): string {
		this.seq++;
		return `afl_01JDN${String(this.seq).padStart(21, "0")}`;
	}

	async handle(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		if (p === "assistant/files") {
			if (m === "GET") {
				const threadId = url.searchParams.get("threadId") ?? "";
				const scope = url.searchParams.get("scope") ?? "";
				if (scope && scope !== "conversation" && scope !== "shared")
					return error(400, "invalid_request", `unknown scope "${scope}"`);
				const files = [...this.files.values()]
					.filter(
						(f) =>
							f.bound &&
							(!threadId ||
								f.meta.threadId === threadId ||
								f.meta.scope === "shared") &&
							(!scope || f.meta.scope === scope),
					)
					.map((f) => f.meta)
					.sort((a, b) => b.createdAt - a.createdAt || (a.id < b.id ? 1 : -1));
				return json({ files });
			}
			if (m === "POST") {
				const raw = url.searchParams.get("name") ?? "";
				const name = raw.split("/").pop()?.trim() ?? "";
				if (!name) return error(400, "invalid_request", "name is required");
				const threadId = url.searchParams.get("threadId") ?? undefined;
				if (threadId) {
					const th = this.threads.get(threadId);
					if (!th) return error(404, "not_found", "thread not found");
					if (th.kind !== "assistant")
						return error(400, "invalid_request", "threadId 必须是全局助手对话");
				}
				const data = new Uint8Array(await req.arrayBuffer());
				if (data.byteLength > MAX_BYTES)
					return error(413, "too_large", "file exceeds 100MiB");
				const meta: AssistantFile = {
					id: this.newId(),
					name,
					mime: mimeOf(name),
					size: data.byteLength,
					sha256: "0".repeat(64),
					scope: "conversation",
					source: "upload",
					createdAt: Date.now(),
					...(threadId ? { threadId } : {}),
				};
				this.files.set(meta.id, { meta, data, bound: false });
				return json(meta, 201);
			}
			return null;
		}
		const one = /^assistant\/files\/([^/]+)(\/content)?$/.exec(p);
		if (!one) return null;
		const f = this.files.get(one[1] ?? "");
		if (!f) return error(404, "not_found", "file not found");
		if (one[2]) {
			if (m !== "GET") return null;
			return this.content(f, req.headers.get("Range"));
		}
		if (m === "PATCH") {
			let scope: unknown;
			try {
				const b: unknown = await req.json();
				if (b && typeof b === "object" && "scope" in b) scope = b.scope;
			} catch {
				scope = undefined;
			}
			if (scope !== "conversation" && scope !== "shared")
				return error(
					400,
					"invalid_request",
					`unknown scope "${String(scope)}"`,
				);
			f.meta = { ...f.meta, scope };
			return json(f.meta);
		}
		if (m === "DELETE") {
			this.files.delete(f.meta.id);
			return new Response(null, { status: 204 });
		}
		return null;
	}

	private content(f: Stored, range: string | null): Response {
		const headers = {
			"Content-Type": "application/octet-stream",
			"Content-Disposition": `attachment; filename*=UTF-8''${encodeURIComponent(f.meta.name)}`,
		};
		const r = range ? /^bytes=(\d+)-(\d*)$/.exec(range) : null;
		if (!r) return new Response(f.data, { headers });
		const size = f.data.byteLength;
		const start = Number(r[1]);
		const end = Math.min(r[2] ? Number(r[2]) : size - 1, size - 1);
		if (start >= size)
			return new Response(null, {
				status: 416,
				headers: { "Content-Range": `bytes */${size}` },
			});
		return new Response(f.data.slice(start, end + 1), {
			status: 206,
			headers: { ...headers, "Content-Range": `bytes ${start}-${end}/${size}` },
		});
	}
}
