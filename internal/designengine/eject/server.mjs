// 零依赖静态服务：把 dist/ 提供在 PORT（缺省 3000）上；不存在的路径回 index.html（帧路由在 hash 里）。
// 用法：npm run build && node server.mjs

import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { createServer } from "node:http";
import path from "node:path";
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

const server = createServer(async (req, res) => {
	if (req.method !== "GET" && req.method !== "HEAD") {
		res.writeHead(405, { Allow: "GET, HEAD" });
		res.end();
		return;
	}
	const { pathname } = new URL(req.url ?? "/", "http://localhost");
	const hit =
		(await resolveFile(pathname)) ?? (await resolveFile("/index.html"));
	if (!hit) {
		res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8" });
		res.end("dist/ 里没有 index.html，先运行 npm run build。\n");
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
	createReadStream(hit.file).pipe(res);
});

server.listen(port, () => {
	console.log(`serving ${root} on :${port}`);
});
