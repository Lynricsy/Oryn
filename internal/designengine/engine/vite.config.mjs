// 设计画布引擎的 vite 配置（M29）。必须从引擎根加载（探针 P2：放在设计目录时 vite 按设计目录解析插件，报
// Missing "./internal" specifier in "vite"）：
//   node <engine>/node_modules/vite/bin/vite.js [build] --config <engine>/vite.config.mjs
// 环境变量：
//   ORYN_DESIGN_DIR            工作副本根（必填，绝对路径），布局见 plugin-frames.mjs
//   PORT                       dev 端口（orbd 受管服务注入；缺省 5173），只听 127.0.0.1，端口被占即失败
//   PUBLIC_URL                 dev 的公开地址（orbd 受管服务按 portal 模板注入；可缺）。它的主机名加入放行集合
//   ORYN_DESIGN_ALLOWED_HOSTS  dev 额外放行的 Host，逗号分隔；每项是完整主机名或以 . 开头的后缀，原样交给
//                              server.allowedHosts。放行集合 = PUBLIC_URL 主机名 ∪ 本变量；两者都空时不设，保持 vite
//                              默认（localhost、*.localhost、IP 字面量）。不按请求推断 portal 域名：portal 代理保留公开
//                              Host 头，放行范围只能由起服务的一方给出受信值，绝不设 true
//   ORYN_DESIGN_SANDBOX        dev 下 /frames/runtime.js 与 /frames/probe.js 的来源目录（可缺，缺时两者 404，入口静默跳过）

import fs from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import orynLoc from "./babel-loc.mjs";
import { orynDesignCss, orynDesignResolve, orynFrames } from "./plugin-frames.mjs";

const engine = path.dirname(fileURLToPath(import.meta.url));
const engineEntry = path.join(engine, "entry.tsx");

function workDir() {
	const dir = process.env.ORYN_DESIGN_DIR ?? "";
	if (!path.isAbsolute(dir)) {
		throw new Error("ORYN_DESIGN_DIR 必须是设计工作副本的绝对路径");
	}
	return path.resolve(dir);
}

function devPort() {
	const raw = process.env.PORT ?? "";
	if (raw === "") return 5173;
	const port = Number(raw);
	if (!Number.isInteger(port) || port < 1 || port > 65535) {
		throw new Error(`PORT 不是合法端口：${raw}`);
	}
	return port;
}

function allowedHosts() {
	const hosts = (process.env.ORYN_DESIGN_ALLOWED_HOSTS ?? "")
		.split(",")
		.map((h) => h.trim())
		.filter((h) => h !== "");
	const publicURL = (process.env.PUBLIC_URL ?? "").trim();
	if (publicURL !== "") {
		let host = "";
		try {
			host = new URL(publicURL).hostname;
		} catch {
			throw new Error(`PUBLIC_URL 不是合法地址：${publicURL}`);
		}
		if (host !== "" && !hosts.includes(host)) hosts.push(host);
	}
	return hosts.length > 0 ? hosts : undefined;
}

function escapeRegExp(s) {
	return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// react、react-dom 与 lucide-react（含子路径）一律按引擎目录解析：设计级依赖带来的另一份 React 不会进页面；三者都由引擎
// 提供（技能要求设计不重复声明），也只有这样 optimizeDeps.include 才能在启动时从引擎解析到它们
const engineRequire = createRequire(engineEntry);
const pinnedReact = {
	find: /^(react|react-dom|lucide-react)(\/.*)?$/,
	replacement: "$1$2",
	customResolver(id, _importer, options) {
		return this.resolve(id, engineEntry, { ...options, skipSelf: true });
	},
};
// 只用于让 vite 启动时就报出引擎依赖缺失，而不是到第一个请求才失败
engineRequire.resolve("react");
engineRequire.resolve("react-dom/client");

// 引擎的 node_modules 可能是指向镜像预装目录的软链（C3）；vite 按真实路径提供依赖，fs.allow 要含真实路径
const engineModules = fs.realpathSync(path.join(engine, "node_modules"));

export default defineConfig(({ command }) => {
	const work = workDir();
	const root = path.join(work, ".oryn-build");
	return {
		root,
		base: command === "build" ? "./" : "/",
		appType: "custom",
		publicDir: false,
		envDir: false,
		clearScreen: false,
		cacheDir: path.join(root, ".vite"),
		resolve: {
			alias: [
				pinnedReact,
				{ find: /^oryn:design$/, replacement: path.join(engine, "link.tsx") },
				{ find: /^oryn:engine$/, replacement: engineEntry },
			],
		},
		plugins: [
			orynDesignCss({ work, engine }),
			tailwindcss(),
			orynDesignResolve({ work, engine }),
			// babel（打标 + Fast Refresh）只跑设计源码；引擎文件、生成的入口与预构建依赖交给 esbuild
			react({
				include: new RegExp(`^${escapeRegExp(path.join(work, "src") + path.sep)}.*\\.[tj]sx?$`),
				exclude: /\/node_modules\//,
				babel: { plugins: [[orynLoc, { root: work }]] },
			}),
			orynFrames({ work, sandbox: process.env.ORYN_DESIGN_SANDBOX ?? "" }),
		],
		optimizeDeps: {
			entries: ["*.entry.tsx"],
			// dev 在 design.open 时就起，那时往往还没有帧入口，扫描器找不到 React；等几个实时帧并发请求时才发现依赖、
			// 重新预构建，加载中的帧会混用新旧两批 chunk（两份 React，白屏报 useEffect of null）。引擎提供的依赖在启动时
			// 就预构建好，经 pinnedReact 按引擎目录解析。
			include: [
				"react",
				"react/jsx-runtime",
				"react/jsx-dev-runtime",
				"react-dom",
				"react-dom/client",
				"lucide-react",
			],
		},
		server: {
			host: "127.0.0.1",
			port: devPort(),
			strictPort: true,
			allowedHosts: allowedHosts(),
			fs: { strict: true, allow: [work, engine, engineModules] },
		},
		build: {
			outDir: path.join(work, "dist"),
			emptyOutDir: true,
			assetsDir: "assets",
			copyPublicDir: false,
			reportCompressedSize: false,
		},
	};
});
