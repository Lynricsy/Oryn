/**
 * 文件来源抽象：线程 Files 面板（orb 的 `$HOME`）与设置页的 agent 文本配置共用同一个文件浏览器。
 * - orb：`/api/v1/orbs/{id}/fs/*`，路径一律为绝对路径；写入用 XHR 以获得上传进度。
 * - agent 配置：`/api/v1/agent-config/files*`，路径相对 omp-home 根（根为 `""`）；
 *   服务端只有扁平的文件列表，目录树在客户端合成，不支持新建文件夹与重命名。
 */
import {
	ApiError,
	api,
	encodePathParam,
	rawFetch,
	type Schemas,
	UNAUTHORIZED_EVENT,
	unwrap,
} from "../api/client";

export type FileEntry = Schemas["FsEntry"];

/** 单文件写入 / 上传上限（与控制面、orbd 一致）。 */
export const MAX_FILE_BYTES = 512 * 1024 * 1024;
/** 可在编辑器里打开的文本上限。 */
export const MAX_EDIT_BYTES = 2 * 1024 * 1024;
/** 超过编辑上限时只读预览的字节数。 */
export const PREVIEW_BYTES = 256 * 1024;

export interface WriteOptions {
	/** 期望的当前 etag；不符 → 412 `modified` */
	etag?: string;
	/** 目标已存在时是否覆盖（缺省 true）；false 且已存在 → 409 `exists` */
	overwrite?: boolean;
	signal?: AbortSignal;
	onProgress?(sent: number, total: number): void;
}

export interface FileSource {
	/** `"orb:<orbId>"` | `"agent-config"` */
	id: string;
	caps: { mkdir: boolean; rename: boolean; upload: boolean; download: boolean };
	/** `label` 为 i18n 键（`files.roots.*`）。 */
	roots(): Promise<{ label: string; path: string }[]>;
	list(path: string): Promise<FileEntry[]>;
	stat(path: string): Promise<FileEntry>;
	readText(
		path: string,
		maxBytes?: number,
	): Promise<{ text: string; etag?: string; truncated: boolean }>;
	readBlob(path: string): Promise<Blob>;
	write(
		path: string,
		body: Blob | string,
		opts?: WriteOptions,
	): Promise<FileEntry>;
	mkdir(path: string): Promise<void>;
	rename(from: string, to: string): Promise<void>;
	remove(path: string): Promise<void>;
	downloadUrl(path: string): string | null;
}

// ---------------------------------------------------------------- 路径工具

/** 拼接目录与名称；目录为 `""`（agent 配置根）时返回名称本身。 */
export function joinPath(dir: string, name: string): string {
	if (!dir) return name;
	return dir.endsWith("/") ? `${dir}${name}` : `${dir}/${name}`;
}

/** 父目录；顶层相对路径的父目录为 `""`，`/x` 的父目录为 `/`。 */
export function parentPath(path: string): string {
	const i = path.lastIndexOf("/");
	if (i < 0) return "";
	if (i === 0) return "/";
	return path.slice(0, i);
}

export function baseName(path: string): string {
	const i = path.lastIndexOf("/");
	return i < 0 ? path : path.slice(i + 1);
}

/** `path` 是否等于 `dir` 或位于其下。 */
export function isWithin(path: string, dir: string): boolean {
	if (dir === "" || dir === "/") return true;
	return path === dir || path.startsWith(`${dir}/`);
}

const IMAGE_MIME: Record<string, string> = {
	png: "image/png",
	jpg: "image/jpeg",
	jpeg: "image/jpeg",
	gif: "image/gif",
	webp: "image/webp",
	avif: "image/avif",
	bmp: "image/bmp",
	svg: "image/svg+xml",
};

/** 可在预览里以 `<img>` 显示的类型。 */
export function isPreviewImage(mime: string | undefined): boolean {
	return mime !== undefined && Object.values(IMAGE_MIME).includes(mime);
}

const BINARY_EXT: Record<string, true> = {
	zip: true,
	gz: true,
	tgz: true,
	xz: true,
	zst: true,
	bz2: true,
	"7z": true,
	tar: true,
	pdf: true,
	woff: true,
	woff2: true,
	ttf: true,
	otf: true,
	ico: true,
	mp3: true,
	mp4: true,
	mov: true,
	webm: true,
	wasm: true,
	so: true,
	bin: true,
	db: true,
	sqlite: true,
};

function extOf(path: string): string {
	const name = baseName(path);
	const i = name.lastIndexOf(".");
	return i <= 0 ? "" : name.slice(i + 1).toLowerCase();
}

/** 按扩展名推断 mime 与是否文本（agent 配置没有服务端嗅探）。 */
export function guessType(path: string): { mime: string; text: boolean } {
	const ext = extOf(path);
	const image = IMAGE_MIME[ext];
	if (image) return { mime: image, text: ext === "svg" };
	if (BINARY_EXT[ext]) return { mime: "application/octet-stream", text: false };
	if (ext === "md") return { mime: "text/markdown", text: true };
	if (ext === "json") return { mime: "application/json", text: true };
	if (ext === "yml" || ext === "yaml")
		return { mime: "application/yaml", text: true };
	return { mime: "text/plain", text: true };
}

/** 读取到的字节按 UTF-8 解码；截断时丢掉末尾残缺的码点。 */
function decodeUtf8(buf: ArrayBuffer, truncated: boolean): string {
	return new TextDecoder("utf-8").decode(buf, { stream: truncated });
}

/** 目录在前、按名不分大小写排序（与 orbd 一致）。 */
export function sortEntries(entries: FileEntry[]): FileEntry[] {
	const isDir = (e: FileEntry) =>
		e.type === "dir" || (e.type === "symlink" && e.targetType === "dir");
	return [...entries].sort((a, b) => {
		const d = Number(isDir(b)) - Number(isDir(a));
		if (d) return d;
		return a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
	});
}

/** 条目是否可展开为目录（含指向目录的符号链接）。 */
export function isDirEntry(e: FileEntry): boolean {
	return e.type === "dir" || (e.type === "symlink" && e.targetType === "dir");
}

// ---------------------------------------------------------------- XHR 写入

export interface XhrUpload {
	/** 缺省 PUT。 */
	method?: "PUT" | "POST";
	url: string;
	body: Blob | string;
	headers: Record<string, string>;
	signal?: AbortSignal;
	onProgress?(sent: number, total: number): void;
}

function bodySize(body: Blob | string): number {
	return typeof body === "string" ? new Blob([body]).size : body.size;
}

function xhrError(xhr: XMLHttpRequest): ApiError {
	let code = xhr.status === 401 ? "unauthorized" : "http_error";
	let message = `HTTP ${xhr.status}`;
	try {
		const body = JSON.parse(xhr.responseText) as unknown;
		if (body && typeof body === "object") {
			if ("code" in body && typeof body.code === "string") code = body.code;
			if ("message" in body && typeof body.message === "string")
				message = body.message;
		}
	} catch {
		// 非 JSON 错误体：保留默认文案
	}
	const ra = Number(xhr.getResponseHeader("Retry-After"));
	return new ApiError(
		xhr.status,
		code,
		message,
		Number.isFinite(ra) && ra > 0 ? ra : undefined,
	);
}

/** 上传原始字节并报告进度（文件面板与消息附件共用）；带 `X-Oryn-CSRF: 1`，响应体按 JSON 解析。 */
export function xhrUpload<T>({
	method = "PUT",
	url,
	body,
	headers,
	signal,
	onProgress,
}: XhrUpload): Promise<T> {
	return new Promise<T>((resolve, reject) => {
		if (signal?.aborted) {
			reject(new DOMException("aborted", "AbortError"));
			return;
		}
		const xhr = new XMLHttpRequest();
		xhr.open(method, url);
		xhr.withCredentials = true;
		xhr.setRequestHeader("X-Oryn-CSRF", "1");
		xhr.setRequestHeader("Content-Type", "application/octet-stream");
		for (const [k, v] of Object.entries(headers)) xhr.setRequestHeader(k, v);
		const total = bodySize(body);
		xhr.upload.onprogress = (e) =>
			onProgress?.(e.loaded, e.lengthComputable ? e.total : total);
		const onAbort = () => xhr.abort();
		signal?.addEventListener("abort", onAbort, { once: true });
		const done = () => signal?.removeEventListener("abort", onAbort);
		xhr.onload = () => {
			done();
			if (xhr.status === 401 && typeof window !== "undefined")
				window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
			if (xhr.status < 200 || xhr.status >= 300) {
				reject(xhrError(xhr));
				return;
			}
			onProgress?.(total, total);
			try {
				resolve(JSON.parse(xhr.responseText) as T);
			} catch {
				reject(new ApiError(xhr.status, "http_error", "invalid response"));
			}
		};
		xhr.onerror = () => {
			done();
			reject(new ApiError(0, "network_error", "network error"));
		};
		xhr.onabort = () => {
			done();
			reject(new DOMException("aborted", "AbortError"));
		};
		xhr.send(body);
	});
}

// ---------------------------------------------------------------- orb

export function orbFileSource(orbId: string): FileSource {
	const id = { id: orbId };
	const fileUrl = (path: string) =>
		`/api/v1/orbs/${encodeURIComponent(orbId)}/fs/file?path=${encodeURIComponent(path)}`;
	return {
		id: `orb:${orbId}`,
		caps: { mkdir: true, rename: true, upload: true, download: true },
		async roots() {
			const r = await unwrap(
				api.GET("/api/v1/orbs/{id}/fs/roots", { params: { path: id } }),
			);
			return [
				{ label: "files.roots.workspace", path: r.workdir },
				{ label: "files.roots.home", path: r.home },
			];
		},
		async list(path) {
			const r = await unwrap(
				api.GET("/api/v1/orbs/{id}/fs/list", {
					params: { path: id, query: { path } },
				}),
			);
			return r.entries;
		},
		stat(path) {
			return unwrap(
				api.GET("/api/v1/orbs/{id}/fs/stat", {
					params: { path: id, query: { path } },
				}),
			);
		},
		async readText(path, maxBytes = MAX_EDIT_BYTES) {
			let res: Response;
			try {
				res = await rawFetch(fileUrl(path), {
					headers: { Range: `bytes=0-${maxBytes - 1}` },
				});
			} catch (e) {
				// 空文件上的 Range 请求按 RFC 9110 回 416
				if (e instanceof ApiError && e.status === 416)
					return { text: "", truncated: false };
				throw e;
			}
			const buf = await res.arrayBuffer();
			// 206：Content-Range 给出总长；200：服务端忽略了 Range，自行截断
			const total = Number(
				res.headers.get("Content-Range")?.split("/")[1] ?? buf.byteLength,
			);
			const truncated =
				(Number.isFinite(total) && total > maxBytes) ||
				buf.byteLength > maxBytes;
			const bytes = buf.byteLength > maxBytes ? buf.slice(0, maxBytes) : buf;
			return {
				text: decodeUtf8(bytes, truncated),
				etag: res.headers.get("ETag") ?? undefined,
				truncated,
			};
		},
		async readBlob(path) {
			return (await rawFetch(fileUrl(path))).blob();
		},
		write(path, body, opts = {}) {
			const headers: Record<string, string> = {};
			if (opts.etag) headers["If-Match"] = opts.etag;
			const overwrite = opts.overwrite ?? true;
			return xhrUpload<FileEntry>({
				url: `${fileUrl(path)}&overwrite=${overwrite}`,
				body,
				headers,
				signal: opts.signal,
				onProgress: opts.onProgress,
			});
		},
		async mkdir(path) {
			await unwrap(
				api.POST("/api/v1/orbs/{id}/fs/mkdir", {
					params: { path: id },
					body: { path },
				}),
			);
		},
		async rename(from, to) {
			await unwrap(
				api.POST("/api/v1/orbs/{id}/fs/rename", {
					params: { path: id },
					body: { from, to, overwrite: false },
				}),
			);
		},
		async remove(path) {
			await unwrap(
				api.POST("/api/v1/orbs/{id}/fs/delete", {
					params: { path: id },
					body: { path },
				}),
			);
		},
		downloadUrl: fileUrl,
	};
}

// ---------------------------------------------------------------- agent 配置

type AgentFile = Schemas["AgentConfigFile"];

const agentUrl = (path: string) =>
	`/api/v1/agent-config/files/${encodePathParam(path)}`;

/** 由扁平的文件列表合成 `dir` 下一层的条目（`dir` 为 `""` 表示根）。 */
export function agentConfigListing(
	files: AgentFile[],
	dir: string,
): FileEntry[] {
	const prefix = dir ? `${dir}/` : "";
	const out = new Map<string, FileEntry>();
	for (const f of files) {
		if (!f.path.startsWith(prefix)) continue;
		const rest = f.path.slice(prefix.length);
		if (!rest) continue;
		const slash = rest.indexOf("/");
		if (slash < 0) {
			out.set(rest, agentFileEntry(f));
			continue;
		}
		const name = rest.slice(0, slash);
		const prev = out.get(name);
		const mtime = Math.max(prev?.mtime ?? 0, f.updatedAt);
		if (prev && prev.type !== "dir") continue;
		out.set(name, {
			path: prefix + name,
			name,
			type: "dir",
			size: 0,
			mtime,
			mode: 0o755,
		});
	}
	return sortEntries([...out.values()]);
}

function agentFileEntry(f: AgentFile): FileEntry {
	return {
		path: f.path,
		name: baseName(f.path),
		type: "file",
		size: f.size,
		mtime: f.updatedAt,
		mode: f.mode,
		...guessType(f.path),
	};
}

export function agentConfigSource(
	onWriteResult?: (r: Schemas["AgentConfigWriteResult"]) => void,
): FileSource {
	const files = async () =>
		(await unwrap(api.GET("/api/v1/agent-config/files"))).files;
	return {
		id: "agent-config",
		caps: { mkdir: false, rename: false, upload: true, download: true },
		async roots() {
			return [{ label: "files.roots.agentConfig", path: "" }];
		},
		async list(path) {
			return agentConfigListing(await files(), path);
		},
		async stat(path) {
			const all = await files();
			const f = all.find((x) => x.path === path);
			if (f) return agentFileEntry(f);
			if (all.some((x) => x.path.startsWith(`${path}/`)))
				return {
					path,
					name: baseName(path),
					type: "dir",
					size: 0,
					mtime: 0,
					mode: 0o755,
				};
			throw new ApiError(404, "not_found", path);
		},
		async readText(path, maxBytes = MAX_EDIT_BYTES) {
			const buf = await (await rawFetch(agentUrl(path))).arrayBuffer();
			const truncated = buf.byteLength > maxBytes;
			return {
				text: decodeUtf8(truncated ? buf.slice(0, maxBytes) : buf, truncated),
				truncated,
			};
		},
		async readBlob(path) {
			return (await rawFetch(agentUrl(path))).blob();
		},
		async write(path, body, opts = {}) {
			if (opts.overwrite === false) {
				const all = await files();
				if (all.some((x) => x.path === path))
					throw new ApiError(409, "exists", path);
			}
			const size = bodySize(body);
			const res = await rawFetch(agentUrl(path), {
				method: "PUT",
				headers: { "Content-Type": "application/octet-stream" },
				body,
				signal: opts.signal,
			});
			const result = (await res.json()) as Schemas["AgentConfigWriteResult"];
			opts.onProgress?.(size, size);
			onWriteResult?.(result);
			return agentFileEntry(result.file);
		},
		async mkdir() {
			throw new ApiError(400, "unsupported", "mkdir");
		},
		async rename() {
			throw new ApiError(400, "unsupported", "rename");
		},
		async remove(path) {
			const all = await files();
			const targets = all.filter(
				(x) => x.path === path || x.path.startsWith(`${path}/`),
			);
			if (targets.length === 0) throw new ApiError(404, "not_found", path);
			for (const f of targets)
				await rawFetch(agentUrl(f.path), { method: "DELETE" });
		},
		downloadUrl: agentUrl,
	};
}
