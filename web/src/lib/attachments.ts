/**
 * 消息附件（M3 计划 5.4）：上传到控制面（`POST attachments`，原始字节，单文件 ≤100MiB），发消息时以
 * `orynAttachments` 携带 id，控制面投递进 orb 后在消息末尾追加 `<oryn-attachments>` 块。
 */
import { api, rawFetch, type Schemas, unwrap } from "../api/client";
import { xhrUpload } from "./file-source";

export type Attachment = Schemas["Attachment"];

/** 单个附件上限（服务端同样校验，超限 413 too_large）。 */
export const MAX_ATTACHMENT_BYTES = 100 * 1024 * 1024;
/** 每条消息最多可带的附件数。 */
export const MAX_ATTACHMENTS_PER_MESSAGE = 20;

export interface UploadOptions {
	/** 直接绑定到该线程（线程输入区）；缺省 = 暂不绑定（新建线程框，随 `POST threads` 绑定）。 */
	threadId?: string;
	onProgress?(sent: number, total: number): void;
	signal?: AbortSignal;
}

export function uploadAttachment(
	file: File,
	{ threadId, onProgress, signal }: UploadOptions = {},
): Promise<Attachment> {
	const q = new URLSearchParams({ name: file.name || "file" });
	if (threadId) q.set("threadId", threadId);
	const url = `/api/v1/attachments?${q.toString()}`;
	// mock 模式只拦截 fetch（XHR 不经过假服务端）：改用 fetch，进度只在完成时报告
	if (import.meta.env.VITE_MOCK === "1")
		return rawFetch(url, { method: "POST", body: file, signal }).then(
			async (res) => {
				onProgress?.(file.size, file.size);
				return (await res.json()) as Attachment;
			},
		);
	return xhrUpload<Attachment>({
		method: "POST",
		url,
		body: file,
		headers: {},
		signal,
		onProgress,
	});
}

/** 附件内容地址；`inline` 只对安全的栅格图片生效（服务端判定），其余一律以附件下载。 */
export function attachmentContentUrl(id: string, inline = false): string {
	const base = `/api/v1/attachments/${encodeURIComponent(id)}/content`;
	return inline ? `${base}?inline=true` : base;
}

export async function listThreadAttachments(
	threadId: string,
): Promise<Attachment[]> {
	const res = await unwrap(
		api.GET("/api/v1/threads/{id}/attachments", {
			params: { path: { id: threadId } },
		}),
	);
	return res.attachments;
}

export async function deleteAttachment(id: string): Promise<void> {
	await unwrap(
		api.DELETE("/api/v1/attachments/{id}", { params: { path: { id } } }),
	);
}

/** 用户消息里解析出的附件（来自控制面追加的附件块）。 */
export interface MessageAttachment {
	/** `att_…`；路径不符合约定时为 null（只显示名称）。 */
	id: string | null;
	name: string;
	path: string;
	mime: string;
	size: number;
}

const BLOCK_RE =
	/(?:\n\n)?<oryn-attachments>\n([\s\S]*?)<\/oryn-attachments>\s*$/;
const LINE_RE = /^- (.+) \(([^,()]+), (\d+) bytes\)$/;
const ID_RE = /\/(att_[0-9A-Z]{26})\/[^/]+$/;

/**
 * 把用户消息末尾的 `<oryn-attachments>…</oryn-attachments>` 块拆出来：返回去掉该块的正文与附件列表；
 * 没有附件块时原样返回正文。
 */
export function parseAttachmentBlock(text: string): {
	text: string;
	attachments: MessageAttachment[];
} {
	const m = BLOCK_RE.exec(text);
	if (!m) return { text, attachments: [] };
	const attachments: MessageAttachment[] = [];
	for (const line of (m[1] ?? "").split("\n")) {
		const l = LINE_RE.exec(line);
		if (!l) continue;
		const path = l[1] ?? "";
		attachments.push({
			id: ID_RE.exec(path)?.[1] ?? null,
			name: path.slice(path.lastIndexOf("/") + 1),
			path,
			mime: l[2] ?? "",
			size: Number(l[3]),
		});
	}
	return { text: text.slice(0, m.index), attachments };
}
