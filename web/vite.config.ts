/// <reference types="vitest/config" />
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// 开发时把 API 与健康检查转发到本机 orynd（dev/orynd.yaml 监听 127.0.0.1:7600）
const orynd = "http://127.0.0.1:7600";

export default defineConfig({
	plugins: [react(), tailwindcss()],
	server: {
		proxy: {
			"/api": { target: orynd, ws: true, changeOrigin: true },
			"/healthz": { target: orynd, changeOrigin: true },
		},
	},
	test: {
		environment: "jsdom",
		globals: true,
		setupFiles: ["./src/test/setup.ts"],
	},
});
