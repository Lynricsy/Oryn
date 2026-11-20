/**
 * UI 排版审计的公共部分：界面登记（surface）、浮层断言（遮挡 / 裁切 / 玻璃一致性）与截图。
 *
 * 每个界面 = 一个用例：打开路由 → 执行 `open` 打开浮层（可选）→ 断言 → 截图到
 * `docs/ui-audit/shots/<id>/<project>-<phase>.png`。浮层另做「滚到底再断言」与「关掉重开再断言」。
 * before 阶段（基线代码）只记录问题、不判失败，问题清单写进截图目录旁的 `<project>-before.json`。
 */
import { mkdirSync, writeFileSync } from "node:fs";
import {
	test as base,
	expect,
	type Locator,
	type Page,
	type TestInfo,
} from "@playwright/test";

export const PHASE: "before" | "after" =
	process.env.UI_AUDIT_PHASE === "before" ? "before" : "after";
const SHOTS = new URL("../../../docs/ui-audit/shots/", import.meta.url)
	.pathname;

/**
 * 页内记录器：任何带 backdrop-filter 的元素（`.glass` 的 ::before、托盘、侧栏……）插入或换类名后的下一帧，
 * 检查它自己与祖先上正在跑的动画 / 过渡是否动到 opacity、filter、mask、clip-path、backdrop-filter。
 * 这些属性取非初值时祖先成为 Backdrop Root，模糊只取得到祖先内部，读起来就是「透明度时有时无」。
 */
const RECORDER = `(() => {
	const BAD = /^(opacity|filter|clip-?path|mask(-image)?|maskImage|backdrop-?filter|backdropFilter|mix-?blend-?mode)$/i;
	const hits = (window.__uaGlassAnim = []);
	const blurs = (el) => {
		const s = getComputedStyle(el);
		const b = getComputedStyle(el, "::before");
		return (s.backdropFilter && s.backdropFilter !== "none") || (b.backdropFilter && b.backdropFilter !== "none");
	};
	const desc = (e) => e.tagName.toLowerCase() + (e.className && typeof e.className === "string" ? "." + e.className.trim().split(/\\s+/).slice(0, 4).join(".") : "");
	const inspect = (g) => requestAnimationFrame(() => {
		if (!g.isConnected || !blurs(g)) return;
		for (let e = g; e && e !== document.documentElement; e = e.parentElement) {
			for (const a of e.getAnimations()) {
				const props = typeof CSSTransition !== "undefined" && a instanceof CSSTransition
					? [a.transitionProperty]
					: (a.effect && a.effect.getKeyframes ? a.effect.getKeyframes() : []).flatMap((k) => Object.keys(k));
				const bad = props.filter((p) => BAD.test(p));
				if (bad.length) hits.push({ el: g, text: desc(g) + " 打开时 " + (e === g ? "自身" : "祖先 " + desc(e)) + " 在动画 " + bad.join("/") });
			}
		}
	});
	const scan = (n) => {
		if (!(n instanceof Element)) return;
		inspect(n);
		for (const c of n.querySelectorAll("*")) inspect(c);
	};
	new MutationObserver((ms) => {
		for (const m of ms) {
			if (m.type === "attributes") scan(m.target);
			else for (const n of m.addedNodes) scan(n);
		}
	}).observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ["class"] });
})();`;

export const test = base.extend<{ audit: undefined }>({
	audit: [
		async ({ page }, use) => {
			await page.addInitScript({ content: RECORDER });
			await use();
		},
		{ auto: true },
	],
});
export { expect };

export const isMobile = (info: TestInfo) => info.project.name.startsWith("m");

/** 等字体与所有有限时长的动画结束（转圈之类无限动画不等）。 */
export async function settle(page: Page) {
	await page.evaluate(async () => {
		await document.fonts.ready;
		const finite = document
			.getAnimations()
			.filter(
				(a) =>
					a.effect?.getComputedTiming().endTime !== Number.POSITIVE_INFINITY,
			);
		await Promise.all(finite.map((a) => a.finished.catch(() => undefined)));
		await new Promise((r) =>
			requestAnimationFrame(() => requestAnimationFrame(r)),
		);
	});
}

/** 打开路由并等 `ready` 出现（默认等侧栏或主内容的任一可交互元素）。 */
export async function go(page: Page, path: string, ready?: string) {
	await page.goto(path);
	await page
		.locator(ready ?? "main, [role=main], textarea, [data-testid=transcript]")
		.first()
		.waitFor({ timeout: 60_000 });
	await settle(page);
}

/** 页内断言：返回问题列表（空 = 通过）。 */
async function inspect(target: Locator): Promise<string[]> {
	return target.evaluate((root) => {
		const issues: string[] = [];
		const desc = (e: Element) =>
			`${e.tagName.toLowerCase()}${typeof e.className === "string" && e.className.trim() ? `.${e.className.trim().split(/\s+/).slice(0, 4).join(".")}` : ""}${e.getAttribute("aria-label") ? `[${e.getAttribute("aria-label")}]` : ""}`;
		const vw = window.innerWidth;
		const vh = window.innerHeight;
		const r = root.getBoundingClientRect();
		if (r.width < 1 || r.height < 1) return [`浮层尺寸为 0（${desc(root)}）`];
		const tol = 1;
		if (
			r.left < -tol ||
			r.top < -tol ||
			r.right > vw + tol ||
			r.bottom > vh + tol
		)
			issues.push(
				`超出视口：${Math.round(r.left)},${Math.round(r.top)} → ${Math.round(r.right)},${Math.round(r.bottom)}（视口 ${vw}×${vh}）`,
			);
		// 遮挡 / 裁切：在可见部分取 5×5 网格点，命中的必须是浮层自己（或它的后代）
		const L = Math.max(r.left, 0) + 6;
		const T = Math.max(r.top, 0) + 6;
		const R = Math.min(r.right, vw) - 6;
		const B = Math.min(r.bottom, vh) - 6;
		const foreign = new Map<string, number>();
		if (R > L && B > T) {
			for (let i = 0; i < 5; i++)
				for (let j = 0; j < 5; j++) {
					const x = L + ((R - L) * i) / 4;
					const y = T + ((B - T) * j) / 4;
					const hit = document.elementFromPoint(x, y);
					if (!hit || root.contains(hit)) continue;
					// 被祖先的 overflow 裁掉还是被别的元素压住
					let clippedBy: Element | null = null;
					for (let a = root.parentElement; a; a = a.parentElement) {
						const s = getComputedStyle(a);
						if (s.overflowX === "visible" && s.overflowY === "visible")
							continue;
						const ar = a.getBoundingClientRect();
						if (x < ar.left || x > ar.right || y < ar.top || y > ar.bottom) {
							clippedBy = a;
							break;
						}
					}
					const key = clippedBy
						? `被 ${desc(clippedBy)} 的 overflow 裁切`
						: `被 ${desc(hit)} 遮挡`;
					foreign.set(key, (foreign.get(key) ?? 0) + 1);
				}
		}
		for (const [k, n] of foreign) issues.push(`${k}（${n}/25 个采样点）`);
		// 玻璃一致性：浮层自身与后代里所有带 backdrop-filter 的元素
		const reduced = matchMedia(
			"(prefers-reduced-transparency: reduce)",
		).matches;
		const glassy = [root, ...root.querySelectorAll("*")].filter((e) => {
			const b = getComputedStyle(e, "::before").backdropFilter;
			return e.classList.contains("glass") || (b && b !== "none");
		});
		for (const g of glassy) {
			const s = getComputedStyle(g);
			const before = getComputedStyle(g, "::before");
			if (
				!reduced &&
				g.classList.contains("glass") &&
				(!before.backdropFilter || before.backdropFilter === "none")
			)
				issues.push(`${desc(g)} 的玻璃没有 backdrop-filter`);
			if (/(auto|scroll)/.test(`${s.overflowX} ${s.overflowY}`))
				issues.push(
					`${desc(g)} 既是玻璃又是滚动容器：内容一滚，::before 的模糊底跟着滚走，下方文字直接透出`,
				);
			const roots: string[] = [];
			for (
				let a: Element | null = g;
				a && a !== document.documentElement;
				a = a.parentElement
			) {
				const c = getComputedStyle(a);
				const why: string[] = [];
				if (Number(c.opacity) < 1) why.push(`opacity ${c.opacity}`);
				if (c.filter !== "none") why.push("filter");
				if (c.maskImage && c.maskImage !== "none") why.push("mask");
				if (c.clipPath !== "none") why.push("clip-path");
				if (c.mixBlendMode !== "normal") why.push("mix-blend-mode");
				if (a !== g && c.backdropFilter && c.backdropFilter !== "none")
					why.push("backdrop-filter");
				if (/opacity|filter|mask|clip-path/.test(c.willChange))
					why.push(`will-change ${c.willChange}`);
				if (why.length)
					roots.push(`${a === g ? "自身" : desc(a)}（${why.join("、")}）`);
			}
			if (roots.length)
				issues.push(
					`${desc(g)} 的模糊被 Backdrop Root 截断：${roots.join("；")}`,
				);
		}
		// 记录器由 RECORDER 注入；页面没跑到 init script 时没有这个属性
		const anim: { el: Element; text: string }[] =
			"__uaGlassAnim" in window && Array.isArray(window.__uaGlassAnim)
				? window.__uaGlassAnim
				: [];
		for (const h of anim.splice(0))
			if (root.contains(h.el) || h.el.contains(root)) issues.push(h.text);
		return [...new Set(issues)];
	});
}

/** 把浮层里所有可滚动的区域滚到底，返回是否滚动过。 */
async function scrollInside(target: Locator): Promise<boolean> {
	return target.evaluate(async (root) => {
		let moved = false;
		for (const e of [root, ...root.querySelectorAll<HTMLElement>("*")]) {
			const s = getComputedStyle(e);
			if (
				!/(auto|scroll)/.test(s.overflowY) ||
				e.scrollHeight <= e.clientHeight + 1
			)
				continue;
			e.scrollTop = e.scrollHeight;
			moved = true;
		}
		await new Promise((r) =>
			requestAnimationFrame(() => requestAnimationFrame(r)),
		);
		return moved;
	});
}

/** 页面级断言：文档没有横向溢出。 */
async function inspectPage(page: Page): Promise<string[]> {
	return page.evaluate(() => {
		const d = document.documentElement;
		return d.scrollWidth > window.innerWidth + 1
			? [
					`页面横向溢出：scrollWidth ${d.scrollWidth} > 视口 ${window.innerWidth}`,
				]
			: [];
	});
}

export interface Surface {
	/** 清单里的界面 id，如 `home.role-picker`（也是截图目录名） */
	id: string;
	/** 路由 */
	path: string;
	/** 路由就绪的选择器 */
	ready?: string;
	/** 只在桌面（d*）或只在手机（m*）上跑；缺省都跑 */
	only?: "desktop" | "mobile";
	/** 打开浮层；返回浮层根元素（省略 = 页面级界面） */
	open?: (page: Page, info: TestInfo) => Promise<Locator | undefined>;
	/** 关闭浮层（缺省按 Escape） */
	close?: (page: Page) => Promise<void>;
	/** 不做「关掉重开」（打开有副作用的界面） */
	noReopen?: boolean;
	/** 打开路由前注入的脚本（如去掉 Popover API，验证老 WebKit 上的退化路径） */
	init?: string;
	/** 关掉后重开的步骤（缺省重跑 `open`）；浮层开在对话框里时只重做最后一步 */
	reopen?: (page: Page, info: TestInfo) => Promise<Locator | undefined>;
}

function record(info: TestInfo, id: string, issues: string[]) {
	const dir = `${SHOTS}${id}/`;
	mkdirSync(dir, { recursive: true });
	writeFileSync(
		`${dir}${info.project.name}-${PHASE}.json`,
		`${JSON.stringify({ id, project: info.project.name, phase: PHASE, issues }, null, "\t")}\n`,
	);
}

async function shoot(page: Page, info: TestInfo, id: string, suffix = "") {
	const dir = `${SHOTS}${id}/`;
	mkdirSync(dir, { recursive: true });
	await page.screenshot({
		path: `${dir}${info.project.name}-${PHASE}${suffix}.png`,
	});
}

/** 登记一个界面用例。 */
export function surface(s: Surface) {
	test(s.id, async ({ page }, info) => {
		test.skip(
			(s.only === "desktop" && isMobile(info)) ||
				(s.only === "mobile" && !isMobile(info)),
			`仅${s.only === "desktop" ? "桌面" : "手机"}`,
		);
		const issues: string[] = [];
		if (s.init) await page.addInitScript({ content: s.init });
		await go(page, s.path, s.ready);
		// 页面进入过程中记下的玻璃动画问题归到页面本身，不算到随后打开的浮层上
		issues.push(
			...(await page.evaluate(() =>
				"__uaGlassAnim" in window && Array.isArray(window.__uaGlassAnim)
					? window.__uaGlassAnim
							.splice(0)
							.map((h: { text: string }) => `页面进入：${h.text}`)
					: [],
			)),
		);
		const overlay = s.open ? await s.open(page, info) : undefined;
		if (overlay) {
			await overlay.waitFor({ state: "visible" });
			await settle(page);
			issues.push(...(await inspect(overlay)).map((i) => `首次打开：${i}`));
		}
		issues.push(...(await inspectPage(page)));
		await shoot(page, info, s.id);
		if (overlay) {
			if (await scrollInside(overlay)) {
				issues.push(...(await inspect(overlay)).map((i) => `滚到底后：${i}`));
				await shoot(page, info, s.id, "-scrolled");
			}
			if (!s.noReopen && s.open) {
				// 钉住这一个元素再关：lastOverlay 这类定位器关掉后会重新解析到外层对话框，不能拿来判断是否关掉
				const pinned = await overlay.elementHandle();
				await (s.close ? s.close(page) : page.keyboard.press("Escape"));
				await expect(overlay).toBeHidden();
				const again = await s.open(page, info);
				if (again) {
					await again.waitFor({ state: "visible" });
					await settle(page);
					issues.push(...(await inspect(again)).map((i) => `重开后：${i}`));
				}
			}
		}
		const unique = [...new Set(issues)];
		record(info, s.id, unique);
		if (PHASE === "after") expect(unique, unique.join("\n")).toEqual([]);
	});
}

/**
 * 最后一个可见的浮层：顶层浮层（ui/Floating）、旧式玻璃弹层、菜单、对话框、上下文菜单 / 动作面板。
 * 窄屏上不少弹层换成底部抽屉（Dialog），同一个用例两种形态都能取到。
 */
export const lastOverlay = (page: Page) =>
	page
		.locator(
			"[data-floating], .glass.pop-in, [role=menu], [role=dialog], [role=listbox], [data-testid=context-sheet]",
		)
		.filter({ visible: true })
		.last();
