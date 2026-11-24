#!/usr/bin/env bun
/**
 * Artifact 帧 / 发布页的真浏览器回归探针：用 WebKit 与 Chromium 打开 Go 测试起的泛域名帧服务，断言查看器读到入口文件、
 * 页面 fetch 到自己的数据文件、同帧图片解码成功。
 *
 * 为什么不止服务端单测：页面运行在不透明源里（sandbox 不含 allow-same-origin），CSP 的 `'self'` 在 WebKit 里不匹配
 * 页面自己的地址、在 Chromium 里却放行。只用 Chromium 冒烟会漏掉 Safari 与 macOS / iOS App 上「无法读取这个 artifact
 * 的内容」这类问题。
 *
 * 用法（web/ 目录）：bun run probe:artifact-browser
 * 前提：`make web`（orynd 嵌入查看器产物）；`bunx playwright install webkit`（或已有对应版本的 WebKit）；
 * Chromium 取 $CHROMIUM，缺省 /usr/bin/chromium，没有时用 Playwright 自带的。
 */
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { type BrowserType, chromium, type Page, webkit } from "playwright-core";

interface Target {
	name: string;
	url: string;
	want: string;
	image: boolean;
}

const repo = resolve(import.meta.dir, "../..");
const dir = mkdtempSync(join(tmpdir(), "oryn-artifact-probe-"));
const manifest = join(dir, "targets.json");

const server = Bun.spawn(
	[
		"go",
		"test",
		"./internal/artifacts/",
		"-run",
		"^TestArtifactBrowserProbeServer$",
		"-count=1",
		"-v",
	],
	{
		cwd: repo,
		env: { ...process.env, ORYN_ARTIFACT_BROWSER_PROBE: manifest },
		stdout: "pipe",
		stderr: "pipe",
	},
);

async function waitManifest(): Promise<{ targets: Target[]; done: string }> {
	const deadline = Date.now() + 180_000;
	while (Date.now() < deadline) {
		if (existsSync(manifest)) return JSON.parse(readFileSync(manifest, "utf8"));
		if (server.exitCode !== null) break;
		await Bun.sleep(200);
	}
	throw new Error(
		`Go 探针服务没有就绪：\n${await new Response(server.stdout).text()}${await new Response(server.stderr).text()}`,
	);
}

/** 打开一个目标，返回失败原因；通过时返回 null。 */
async function check(page: Page, t: Target): Promise<string | null> {
	const blocked: string[] = [];
	page.on("console", (m) => {
		if (
			m.type() === "error" &&
			/Content Security Policy|connect-src|img-src|Refused/.test(m.text())
		)
			blocked.push(
				m
					.text()
					.replace(/a-[a-z2-7]{26}|p-[a-z2-7]{26}/g, "<label>")
					.slice(0, 160),
			);
	});
	await page.goto(t.url, { waitUntil: "networkidle" });
	try {
		await page.waitForFunction(
			(w) => document.body.innerText.includes(w),
			t.want,
			{ timeout: 8000 },
		);
	} catch {
		const body = (await page.evaluate(() => document.body.innerText))
			.slice(0, 120)
			.replace(/\s+/g, " ");
		return `页面里没有 ${t.want}（实际：${body}）${blocked.length ? `；CSP 拦截：${blocked[0]}` : ""}`;
	}
	if (t.image) {
		const ok = await page
			.waitForFunction(
				() =>
					[...document.images].some((i) => i.complete && i.naturalWidth > 0),
				undefined,
				{ timeout: 8000 },
			)
			.then(
				() => true,
				() => false,
			);
		if (!ok)
			return `同帧图片没有加载成功${blocked.length ? `；CSP 拦截：${blocked[0]}` : ""}`;
	}
	return blocked.length ? `CSP 拦截：${blocked[0]}` : null;
}

let failed = 0;
try {
	const { targets, done } = await waitManifest();
	const sysChromium = process.env.CHROMIUM ?? "/usr/bin/chromium";
	const engines: [string, BrowserType, string | undefined][] = [
		["webkit", webkit, undefined],
		["chromium", chromium, existsSync(sysChromium) ? sysChromium : undefined],
	];
	for (const [name, type, executablePath] of engines) {
		const browser = await type.launch({ executablePath });
		for (const t of targets) {
			const page = await browser.newPage();
			const why = await check(page, t).catch((e: Error) => e.message);
			console.log(
				`${why ? "FAIL" : "PASS"} ${name.padEnd(8)} ${t.name}${why ? `：${why}` : ""}`,
			);
			if (why) failed++;
			await page.close();
		}
		await browser.close();
	}
	await fetch(done).catch(() => {});
} finally {
	const code = await Promise.race([
		server.exited,
		Bun.sleep(10_000).then(() => null),
	]);
	if (code === null) server.kill();
	rmSync(dir, { recursive: true, force: true });
}
console.log(failed ? `\n${failed} 项失败` : "\n全部通过");
process.exit(failed ? 1 : 0);
