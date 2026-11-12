#!/usr/bin/env bun
/**
 * mock 模式截图：起 `VITE_MOCK=1` 的 vite 开发服务器，用本机 Chromium 在 390 / 720 / 1280 宽度下
 * 截 `/` 与 `/t/<id>`，输出到 `$ORYN_SHOTS_DIR`（默认 /tmp/oryn-shots）。
 *
 * 用法（web/ 目录）：bun run shots
 * 环境变量：CHROMIUM=<可执行文件>（默认 /usr/bin/chromium）
 */
import { mkdir } from "node:fs/promises";
import { chromium } from "playwright-core";
import { MAIN_THREAD } from "../src/mock/fixtures";

const PORT = 5179;
const OUT = process.env.ORYN_SHOTS_DIR ?? "/tmp/oryn-shots";
const BASE = `http://127.0.0.1:${PORT}`;
const WIDTHS = [
	{ width: 390, height: 844 },
	{ width: 720, height: 1000 },
	{ width: 1280, height: 860 },
];
const PAGES = [
	{ name: "home", path: "/", ready: "textarea" },
	{
		name: "thread",
		path: `/t/${MAIN_THREAD}`,
		ready: "[data-testid=transcript]",
	},
];

async function waitForServer(url: string, timeoutMs: number) {
	const deadline = Date.now() + timeoutMs;
	while (Date.now() < deadline) {
		try {
			const res = await fetch(url);
			if (res.ok) return;
		} catch {
			// 尚未就绪
		}
		await Bun.sleep(200);
	}
	throw new Error(`vite 未在 ${timeoutMs}ms 内就绪`);
}

const server = Bun.spawn(
	[
		"bunx",
		"vite",
		"--port",
		String(PORT),
		"--strictPort",
		"--host",
		"127.0.0.1",
	],
	{
		cwd: `${import.meta.dir}/..`,
		env: { ...process.env, VITE_MOCK: "1" },
		stdout: "ignore",
		stderr: "inherit",
	},
);

try {
	await waitForServer(BASE, 30_000);
	await mkdir(OUT, { recursive: true });
	const browser = await chromium.launch({
		executablePath: process.env.CHROMIUM ?? "/usr/bin/chromium",
	});
	const written: string[] = [];
	for (const size of WIDTHS) {
		const context = await browser.newContext({
			viewport: size,
			deviceScaleFactor: 2,
			locale: "zh-CN",
		});
		const page = await context.newPage();
		for (const p of PAGES) {
			await page.goto(`${BASE}${p.path}`, { waitUntil: "networkidle" });
			await page.waitForSelector(p.ready, { timeout: 15_000 });
			// 等待懒加载的高亮 / mermaid / KaTeX 渲染完成
			await page.waitForTimeout(2500);
			const file = `${OUT}/${p.name}-${size.width}.png`;
			await page.screenshot({ path: file });
			written.push(file);
			if (p.name === "thread") {
				// 额外一张：滚动到转录中部，展示思考块与工具卡片
				await page.evaluate(() => {
					const el = document.querySelector<HTMLElement>(
						"[data-testid=transcript]",
					);
					if (el) el.scrollTop = el.scrollHeight * 0.25;
				});
				await page.waitForTimeout(1200);
				const mid = `${OUT}/${p.name}-${size.width}-mid.png`;
				await page.screenshot({ path: mid });
				written.push(mid);
			}
		}
		await context.close();
	}
	await browser.close();
	for (const f of written) console.log(f);
} finally {
	server.kill();
}
