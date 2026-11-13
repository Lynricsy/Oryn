/// <reference types="vitest/config" />
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// 开发时把 API、健康检查、portal 授权端点（`/portal/auth`）、公开文件（`/f/…`）与分享的 Markdown 导出（`/s/<slug>.md`）
// 转发到本机 orynd（dev/orynd.yaml 监听 127.0.0.1:7600）；分享页 `/s/<slug>` 本身由 vite 提供
const orynd = "http://127.0.0.1:7600";

export default defineConfig({
	plugins: [react(), tailwindcss()],
	build: {
		// 超过 500KB 的只有按需加载的 mermaid 子图分块（elk、cytoscape 等），不在首屏
		chunkSizeWarningLimit: 1600,
	},
	server: {
		proxy: {
			"/api": { target: orynd, ws: true, changeOrigin: true },
			"/healthz": { target: orynd, changeOrigin: true },
			"/portal": { target: orynd, changeOrigin: true },
			"/f/": { target: orynd, changeOrigin: true },
			"^/s/[0-9a-f]{32}\\.md$": { target: orynd, changeOrigin: true },
		},
	},
	test: {
		environment: "jsdom",
		globals: true,
		setupFiles: ["./src/test/setup.ts"],
	},
});
