/**
 * 文件来源抽象：线程 Files 面板（orb 的 `$HOME`）、设置页与项目页的 agent 文本配置共用同一个文件浏览器。
 * - orb：`/api/v1/orbs/{id}/fs/*`，路径一律为绝对路径；写入用 XHR 以获得上传进度。
 *   orb 停机时可用只读的停机预览（`offline=1`，由 provider 读取已停止的容器，不唤醒 orb）。
 * - agent 配置：全局 `/api/v1/agent-config/files*`（相对 omp-home 根）与项目级
 *   `/api/v1/projects/{id}/agent-config/files*`（相对项目配置根，同仓库里的 `.omp/`），根为 `""`；
 *   服务端只有扁平的文件列表，目录树在客户端合成，不支持新建文件夹与重命名。
 * - SSH 主机（M18）：`/api/v1/ssh/hosts/{id}/fs/*`，控制面经 SFTP 代为读写，接口形状同 orb（不带 `thread` / `offline`）。
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
/** 整份读入预览（图片、PDF、Office、字体、压缩包、3D 模型）的上限：更大的文件只显示信息与下载按钮，读取时也最多收这么多字节。 */
export const MAX_PREVIEW_BLOB_BYTES = 64 * 1024 * 1024;

export interface WriteOptions {
	/** 期望的当前 etag；不符 → 412 `modified` */
	etag?: string;
	/** 目标已存在时是否覆盖（缺省 true）；false 且已存在 → 409 `exists` */
	overwrite?: boolean;
	signal?: AbortSignal;
	onProgress?(sent: number, total: number): void;
}

export interface FileSource {
	/** `"orb:<orbId>"` | `"ssh:<hostId>"` | `"agent-config"` */
	id: string;
	/**
	 * `write`：可写入（新建、上传、重命名、删除、保存）；为 false 时一切变更入口都隐藏，其余各项随之失效。
	 * `mkdir` / `rename` / `upload` 只在可写时有意义。
	 */
	caps: {
		write: boolean;
		mkdir: boolean;
		rename: boolean;
		upload: boolean;
		download: boolean;
	};
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

/**
 * 流式读取响应体，最多保留 `maxBytes` 字节；再多就取消读取（服务端可能无视 Range 回整个大文件）。
 * `over` 为真表示响应体超过了 `maxBytes`。
 */
export async function readCapped(
	res: Response,
	maxBytes: number,
): Promise<{ bytes: Uint8Array<ArrayBuffer>; over: boolean }> {
	const chunks: Uint8Array[] = [];
	let size = 0;
	let over = false;
	const reader = res.body?.getReader();
	if (reader)
		for (;;) {
			const { done, value } = await reader.read();
			if (done) break;
			const room = maxBytes - size;
			if (value.byteLength > room) {
				chunks.push(value.subarray(0, room));
				size += room;
				over = true;
				await reader.cancel();
				break;
			}
			chunks.push(value);
			size += value.byteLength;
		}
	const bytes = new Uint8Array(size);
	let at = 0;
	for (const c of chunks) {
		bytes.set(c, at);
		at += c.byteLength;
	}
	return { bytes, over };
}

/** 读取到的字节按 UTF-8 解码；截断时丢掉末尾残缺的码点。 */
function decodeUtf8(buf: Uint8Array, truncated: boolean): string {
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

/** 预览用的整份内容；超过 `MAX_PREVIEW_BLOB_BYTES` 即停止读取并报 413 `too_large`。 */
async function cappedBlob(res: Response): Promise<Blob> {
	const { bytes, over } = await readCapped(res, MAX_PREVIEW_BLOB_BYTES);
	if (over) throw new ApiError(413, "too_large", "file too large to preview");
	return new Blob([bytes]);
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

/** 用 `Range` 读文件开头至多 `maxBytes` 字节并按 UTF-8 解码（orb 与 SSH 主机的 `fs/file` 共用）。 */
async function readTextRange(
	url: string,
	maxBytes: number,
): Promise<{ text: string; etag?: string; truncated: boolean }> {
	let res: Response;
	try {
		res = await rawFetch(url, {
			headers: { Range: `bytes=0-${maxBytes - 1}` },
		});
	} catch (e) {
		// 空文件上的 Range 请求按 RFC 9110 回 416
		if (e instanceof ApiError && e.status === 416)
			return { text: "", truncated: false };
		throw e;
	}
	// 206：Content-Range 给出总长；200：服务端忽略了 Range，读满上限即停
	const { bytes, over } = await readCapped(res, maxBytes);
	const total = Number(res.headers.get("Content-Range")?.split("/")[1]);
	const truncated = over || (Number.isFinite(total) && total > maxBytes);
	return {
		text: decodeUtf8(bytes, truncated),
		etag: res.headers.get("ETag") ?? undefined,
		truncated,
	};
}

// ---------------------------------------------------------------- orb

/**
 * `threadId`：当前线程，给出时所有请求都带 `thread`——runner 的 orb 必须带（范围是该线程的工作目录），普通 orb 由控制面校验后照旧；
 * 省略时（M19 助手的电脑，不属于任何线程）请求不带 `thread`。
 * runner 线程的 `workdir` 与 `home` 是同一个目录（线程的工作目录），此时只给一个根。
 * `offline`：orb 已停机时的只读预览——roots / list / stat / 文件读取都带 `offline=1`（不唤醒 orb），
 * 一切写操作直接以 409 `read_only` 拒绝。来源 `id` 不变，草稿仍按同一个键保存。
 */
export function orbFileSource(
	orbId: string,
	threadId?: string,
	opts: { offline?: boolean } = {},
): FileSource {
	const offline = opts.offline === true;
	const id = { id: orbId };
	const thread: { thread?: string } = threadId ? { thread: threadId } : {};
	// 只读请求的查询参数：停机预览多带 `offline`（服务端按布尔解析，`1` / `true` 等价）
	const readQuery = offline ? { ...thread, offline: true } : thread;
	const fileUrl = (path: string) =>
		`/api/v1/orbs/${encodeURIComponent(orbId)}/fs/file?${threadId ? `thread=${encodeURIComponent(threadId)}&` : ""}${offline ? "offline=1&" : ""}path=${encodeURIComponent(path)}`;
	const source: FileSource = {
		id: `orb:${orbId}`,
		caps: offline
			? {
					write: false,
					mkdir: false,
					rename: false,
					upload: false,
					download: true,
				}
			: {
					write: true,
					mkdir: true,
					rename: true,
					upload: true,
					download: true,
				},
		async roots() {
			const r = await unwrap(
				api.GET("/api/v1/orbs/{id}/fs/roots", {
					params: { path: id, query: readQuery },
				}),
			);
			const workspace = { label: "files.roots.workspace", path: r.workdir };
			return r.home === r.workdir
				? [workspace]
				: [workspace, { label: "files.roots.home", path: r.home }];
		},
		async list(path) {
			const r = await unwrap(
				api.GET("/api/v1/orbs/{id}/fs/list", {
					params: { path: id, query: { ...readQuery, path } },
				}),
			);
			return r.entries;
		},
		stat(path) {
			return unwrap(
				api.GET("/api/v1/orbs/{id}/fs/stat", {
					params: { path: id, query: { ...readQuery, path } },
				}),
			);
		},
		readText(path, maxBytes = MAX_EDIT_BYTES) {
			return readTextRange(fileUrl(path), maxBytes);
		},
		async readBlob(path) {
			return cappedBlob(await rawFetch(fileUrl(path)));
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
					params: { path: id, query: thread },
					body: { path },
				}),
			);
		},
		async rename(from, to) {
			await unwrap(
				api.POST("/api/v1/orbs/{id}/fs/rename", {
					params: { path: id, query: thread },
					body: { from, to, overwrite: false },
				}),
			);
		},
		async remove(path) {
			await unwrap(
				api.POST("/api/v1/orbs/{id}/fs/delete", {
					params: { path: id, query: thread },
					body: { path },
				}),
			);
		},
		downloadUrl: fileUrl,
	};
	if (!offline) return source;
	const readOnly = (): Promise<never> =>
		Promise.reject(
			new ApiError(409, "read_only", "orb is stopped; files are read-only"),
		);
	return {
		...source,
		write: readOnly,
		mkdir: readOnly,
		rename: readOnly,
		remove: readOnly,
	};
}

// ---------------------------------------------------------------- SSH 主机（M18）

/**
 * 主人侧的 SSH 主机文件浏览：`/api/v1/ssh/hosts/{id}/fs/*`，由控制面经 SFTP 代为读写目标机；路径一律为绝对路径。
 * 根是登录用户的主目录与 `/`（主目录就是 `/` 时只给一个）。
 */
export function sshHostFileSource(hostId: string): FileSource {
	const id = { id: hostId };
	const fileUrl = (path: string) =>
		`/api/v1/ssh/hosts/${encodeURIComponent(hostId)}/fs/file?path=${encodeURIComponent(path)}`;
	return {
		id: `ssh:${hostId}`,
		caps: {
			write: true,
			mkdir: true,
			rename: true,
			upload: true,
			download: true,
		},
		async roots() {
			const { home } = await unwrap(
				api.GET("/api/v1/ssh/hosts/{id}/fs/roots", { params: { path: id } }),
			);
			const root = { label: "files.roots.fsRoot", path: "/" };
			return home === "/"
				? [root]
				: [{ label: "files.roots.home", path: home }, root];
		},
		async list(path) {
			const r = await unwrap(
				api.GET("/api/v1/ssh/hosts/{id}/fs/list", {
					params: { path: id, query: { path } },
				}),
			);
			return r.entries;
		},
		stat(path) {
			return unwrap(
				api.GET("/api/v1/ssh/hosts/{id}/fs/stat", {
					params: { path: id, query: { path } },
				}),
			);
		},
		readText(path, maxBytes = MAX_EDIT_BYTES) {
			return readTextRange(fileUrl(path), maxBytes);
		},
		async readBlob(path) {
			return cappedBlob(await rawFetch(fileUrl(path)));
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
				api.POST("/api/v1/ssh/hosts/{id}/fs/mkdir", {
					params: { path: id },
					body: { path },
				}),
			);
		},
		async rename(from, to) {
			await unwrap(
				api.POST("/api/v1/ssh/hosts/{id}/fs/rename", {
					params: { path: id },
					body: { from, to, overwrite: false },
				}),
			);
		},
		async remove(path) {
			await unwrap(
				api.POST("/api/v1/ssh/hosts/{id}/fs/delete", {
					params: { path: id },
					body: { path },
				}),
			);
		},
		downloadUrl: fileUrl,
	};
}

// ---------------------------------------------------------------- agent 配置（全局与项目级）

type AgentFile = Schemas["AgentConfigFile"];

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

/** 中央配置存储的一个根：全局 omp-home 或某个项目的配置层；两者接口形状相同，只差列表与文件地址。 */
interface ConfigSpec {
	id: string;
	rootLabel: string;
	listFiles(): Promise<AgentFile[]>;
	fileUrl(path: string): string;
}

function configSource(
	spec: ConfigSpec,
	onWriteResult?: (r: Schemas["AgentConfigWriteResult"]) => void,
): FileSource {
	const files = spec.listFiles;
	const agentUrl = spec.fileUrl;
	return {
		id: spec.id,
		caps: {
			write: true,
			mkdir: false,
			rename: false,
			upload: true,
			download: true,
		},
		async roots() {
			return [{ label: spec.rootLabel, path: "" }];
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
			const { bytes, over } = await readCapped(
				await rawFetch(agentUrl(path)),
				maxBytes,
			);
			return { text: decodeUtf8(bytes, over), truncated: over };
		},
		async readBlob(path) {
			return cappedBlob(await rawFetch(agentUrl(path)));
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

/** 全局 Agent 配置（`<data_dir>/omp-home`，orb 内为 `~/.omp/agent/`）。 */
export function agentConfigSource(
	onWriteResult?: (r: Schemas["AgentConfigWriteResult"]) => void,
): FileSource {
	return configSource(
		{
			id: "agent-config",
			rootLabel: "files.roots.agentConfig",
			listFiles: async () =>
				(await unwrap(api.GET("/api/v1/agent-config/files"))).files,
			fileUrl: (path) => `/api/v1/agent-config/files/${encodePathParam(path)}`,
		},
		onWriteResult,
	);
}

/** 项目级 Agent 配置（M12.2；目录结构同仓库里的 `.omp/`，orb 内为 `~/.oryn/project-config/.omp/`）。 */
export function projectAgentConfigSource(
	projectId: string,
	onWriteResult?: (r: Schemas["AgentConfigWriteResult"]) => void,
): FileSource {
	return configSource(
		{
			id: `project-config:${projectId}`,
			rootLabel: "files.roots.projectAgentConfig",
			listFiles: async () =>
				(
					await unwrap(
						api.GET("/api/v1/projects/{id}/agent-config/files", {
							params: { path: { id: projectId } },
						}),
					)
				).files,
			fileUrl: (path) =>
				`/api/v1/projects/${encodePathParam(projectId)}/agent-config/files/${encodePathParam(path)}`,
		},
		onWriteResult,
	);
}
