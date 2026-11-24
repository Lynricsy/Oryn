// 零依赖静态服务：把 dist/ 提供在 PORT（缺省 3000）上；不存在的路径回 index.html（帧路由在 hash 里）。
// 用法：npm run build && node server.mjs

import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { createServer } from "node:http";
import path from "node:path";
import { pipeline } from "node:stream/promises";
import { fileURLToPath } from "node:url";

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), "dist");
const port = Number(process.env.PORT || 3000);

const types = {
	".html": "text/html; charset=utf-8",
	".js": "text/javascript; charset=utf-8",
	".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
	".json": "application/json",
	".svg": "image/svg+xml",
	".png": "image/png",
	".jpg": "image/jpeg",
	".jpeg": "image/jpeg",
	".gif": "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".ico": "image/x-icon",
	".woff": "font/woff",
	".woff2": "font/woff2",
	".ttf": "font/ttf",
	".otf": "font/otf",
	".txt": "text/plain; charset=utf-8",
};

/** 请求路径对应的 dist 内文件；越出 dist 或不是文件时为 null。 */
async function resolveFile(pathname) {
	let rel;
	try {
		rel = decodeURIComponent(pathname);
	} catch {
		return null;
	}
	const file = path.join(root, path.normalize(rel));
	if (file !== root && !file.startsWith(root + path.sep)) return null;
	try {
		const st = await stat(file);
		if (st.isFile()) return { file, size: st.size };
		if (st.isDirectory()) {
			const index = path.join(file, "index.html");
			const ist = await stat(index);
			if (ist.isFile()) return { file: index, size: ist.size };
		}
	} catch {}
	return null;
}

/** 解析请求目标的路径部分；请求行里的目标不是合法 URL（如 `http://[::`）时为 null。 */
function requestPath(target) {
	try {
		return new URL(target ?? "/", "http://localhost").pathname;
	} catch {
		return null;
	}
}

function plain(res, status, text) {
	res.writeHead(status, { "Content-Type": "text/plain; charset=utf-8" });
	res.end(text);
}

async function handle(req, res) {
	if (req.method !== "GET" && req.method !== "HEAD") {
		res.writeHead(405, { Allow: "GET, HEAD" });
		res.end();
		return;
	}
	const pathname = requestPath(req.url);
	if (pathname === null) {
		plain(res, 400, "bad request\n");
		return;
	}
	const hit =
		(await resolveFile(pathname)) ?? (await resolveFile("/index.html"));
	if (!hit) {
		plain(res, 404, "dist/ 里没有 index.html，先运行 npm run build。\n");
		return;
	}
	const ext = path.extname(hit.file).toLowerCase();
	const immutable = hit.file.startsWith(path.join(root, "assets") + path.sep);
	res.writeHead(200, {
		"Content-Type": types[ext] ?? "application/octet-stream",
		"Content-Length": hit.size,
		"Cache-Control": immutable
			? "public, max-age=31536000, immutable"
			: "no-cache",
		"X-Content-Type-Options": "nosniff",
	});
	if (req.method === "HEAD") {
		res.end();
		return;
	}
	// pipeline 在读文件出错或客户端中途断开时销毁两端并 reject，由下面的 catch 收尾。
	await pipeline(createReadStream(hit.file), res);
}

const server = createServer((req, res) => {
	handle(req, res).catch((err) => {
		// 客户端断开（ERR_STREAM_PREMATURE_CLOSE 等）不是服务端错误，不记日志。
		if (err?.code !== "ERR_STREAM_PREMATURE_CLOSE") console.error(err);
		if (!res.headersSent) plain(res, 500, "internal error\n");
		else res.destroy();
	});
});

server.listen(port, () => {
	console.log(`serving ${root} on :${port}`);
});
