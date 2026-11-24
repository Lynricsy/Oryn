// 导出工程的 vite 配置：普通单页应用，产物在 dist/（相对路径，放在任何子路径下都能打开）。
// oryn:design 是设计源码里 Link 的模块名，指到本工程的 oryn-design.tsx（hash 路由跳转）。

import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
	base: "./",
	plugins: [tailwindcss(), react()],
	resolve: {
		alias: [
			{
				find: /^oryn:design$/,
				replacement: fileURLToPath(
					new URL("./oryn-design.tsx", import.meta.url),
				),
			},
		],
	},
	build: {
		outDir: "dist",
		emptyOutDir: true,
	},
});
