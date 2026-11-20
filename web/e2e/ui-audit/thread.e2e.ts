/**
 * 线程页核心：转录（顶 / 中 / 底、工具卡、ask / plan 卡）、输入坞及其弹层、页头菜单、线程内对话框、
 * 消息菜单与上下文菜单；外加首页输入卡上未被 home.* 覆盖的斜杠菜单、附件托盘与「目录被占用」对话框。
 */
import type { Locator, Page, TestInfo } from "@playwright/test";
import {
	ENV_THREADS,
	M9_IDS,
	M11B_IDS,
	MAIN_THREAD,
	TOOL_ZOO_THREAD,
} from "../../src/mock/fixtures";
import { isMobile, lastOverlay, settle, surface } from "./audit";

const T = `/t/${MAIN_THREAD}`;
const ZOO = `/t/${TOOL_ZOO_THREAD}`;
const READY = "[data-testid=transcript]";

const composer = (page: Page) => page.getByLabel("消息", { exact: true });
const dialog = (page: Page) =>
	page.getByRole("dialog").filter({ visible: true }).last();

/**
 * 把转录滚到 `frac`（0 顶部，1 底部）处。程序写 scrollTop 不会退出贴底跟随，先派发一次向上的滚轮事件
 * 标记「用户在往上读」。转录还没长过一屏时写 scrollTop 等于停在底部（又回到跟随），懒加载的图表、
 * 公式测高后总高还会变：反复写，直到总高与位置连续两轮不变。
 */
async function scrollTranscript(page: Page, frac: number) {
	await page
		.locator(`${READY} [data-role]`)
		.first()
		.waitFor({ timeout: 30_000 });
	let last = "";
	let still = 0;
	for (let i = 0; i < 30 && still < 2; i++) {
		const now = await page.evaluate((f) => {
			const el = document.querySelector<HTMLElement>(
				"[data-testid=transcript]",
			);
			if (!el) return "";
			if (f < 1) el.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
			el.scrollTop = (el.scrollHeight - el.clientHeight) * f;
			return `${el.scrollHeight}:${Math.round(el.scrollTop)}`;
		}, frac);
		still = now === last ? still + 1 : 0;
		last = now;
		await page.waitForTimeout(300);
	}
	await settle(page);
}

/** 展开转录里当前挂载的全部折叠卡片（虚拟列表会重挂载行，直接在页面里点）。 */
async function expandAll(page: Page) {
	for (let i = 0; i < 6; i++) {
		const n = await page.locator(READY).evaluate((el) => {
			const bs = el.querySelectorAll<HTMLButtonElement>(
				// 菜单触发钮（消息操作）也带 aria-expanded，排除掉，免得顺手把菜单打开
				'button[aria-expanded=false]:not([aria-haspopup]):not([aria-label="消息操作"])',
			);
			for (const b of bs) b.click();
			return bs.length;
		});
		if (n === 0) break;
		await page.waitForTimeout(300);
	}
	await settle(page);
}

/** 线程操作菜单：宽页头点「更多操作」，手机点标题胶囊。 */
async function openActions(page: Page, info: TestInfo) {
	if (isMobile(info)) await page.getByTestId("thread-title").click();
	else await page.getByRole("button", { name: "更多操作" }).first().click();
	await page.getByRole("menu").first().waitFor();
}

/** 线程操作菜单里的一项（宽页头菜单与手机标题菜单同名）。 */
async function action(page: Page, info: TestInfo, name: string | RegExp) {
	await openActions(page, info);
	await page.getByRole("menuitem", { name }).first().click();
}

/** 斜杠命令：输入后回车执行（客户端命令直接打开对话框）。 */
async function slash(page: Page, cmd: string) {
	await composer(page).fill(cmd);
	await page.getByRole("listbox", { name: "命令" }).waitFor();
	await composer(page).press("Enter");
}

const page = (id: string, path: string, open?: (p: Page) => Promise<void>) =>
	surface({
		id,
		path,
		ready: READY,
		open: open
			? async (p) => {
					await open(p);
					return undefined;
				}
			: undefined,
	});

const overlay = (
	id: string,
	// biome-ignore lint/suspicious/noConfusingVoidType: 只做点击、不返回浮层的打开步骤（Promise<void>）取最后一个浮层
	open: (p: Page, info: TestInfo) => Promise<Locator | undefined | void>,
	extra: {
		path?: string;
		only?: "desktop" | "mobile";
		noReopen?: boolean;
		close?: (p: Page) => Promise<void>;
	} = {},
) =>
	surface({
		id,
		path: extra.path ?? T,
		ready: READY,
		only: extra.only,
		noReopen: extra.noReopen,
		close: extra.close,
		open: async (p, info) => (await open(p, info)) ?? lastOverlay(p),
	});

// ---------------------------------------------------------------- 转录

page("thread.top", T, (p) => scrollTranscript(p, 0));
page("thread.mid", T, (p) => scrollTranscript(p, 0.5));
page("thread.bottom", T);
page("thread.tools", ZOO, (p) => scrollTranscript(p, 0));
page("thread.tools.mid", ZOO, (p) => scrollTranscript(p, 0.5));
page("thread.tools.expanded", ZOO, async (p) => {
	await scrollTranscript(p, 0.3);
	await expandAll(p);
});
page("thread.tools.expanded-end", ZOO, async (p) => {
	await expandAll(p);
	await scrollTranscript(p, 1);
	await expandAll(p);
});
for (const [name, id] of Object.entries(ENV_THREADS))
	page(`thread.env.${name}`, `/t/${id}`);
page("thread.runner", `/t/${M11B_IDS.runnerThread}`);
page("thread.runner-offline", `/t/${M11B_IDS.offlineThread}`);
page("thread.scheduled", `/t/${M9_IDS.scheduledThread}`);

surface({
	id: "thread.ask",
	path: T,
	ready: READY,
	noReopen: true,
	open: async (p) => {
		await composer(p).fill("跑一下 uname -a，ask 我要不要保留主机名");
		await composer(p).press("Enter");
		await p.getByText("输出要包含主机名吗？").first().waitFor();
		return undefined;
	},
});

surface({
	id: "thread.plan",
	path: T,
	ready: READY,
	noReopen: true,
	open: async (p) => {
		await composer(p).fill("先跑 uname -a，再给我一份计划");
		await composer(p).press("Enter");
		await p.getByText("重连改造").first().waitFor();
		await settle(p);
		return undefined;
	},
});

// 往上翻后悬在托盘上方的「回到底部」小圆钮
overlay(
	"thread.jump-end",
	async (p) => {
		await scrollTranscript(p, 0.6);
		return p.getByRole("button", { name: "回到底部" });
	},
	{ only: "desktop", noReopen: true },
);

// 手机：往上翻历史，输入坞收成信息胶囊
overlay(
	"thread.dock-pill",
	async (p) => {
		await scrollTranscript(p, 0.6);
		await p.locator("[data-testid=dock-tray][data-dock=mini]").waitFor();
		return p.getByTestId("dock-pill");
	},
	{ only: "mobile", noReopen: true },
);

/** 滚到转录中段，选中一段视口中部的助手正文（触发引用胶囊 / 引用条）。 */
async function selectParagraph(p: Page) {
	await scrollTranscript(p, 0.5);
	await p.evaluate(() => {
		const root = document.querySelector("[data-testid=transcript]");
		const para = [...(root?.querySelectorAll("[data-role=assistant] p") ?? [])]
			.filter((e) => {
				const r = e.getBoundingClientRect();
				return r.top > 120 && r.bottom < innerHeight - 220 && e.textContent;
			})
			.at(0);
		if (!para) throw new Error("没有可选的段落");
		const range = document.createRange();
		range.selectNodeContents(para);
		const sel = getSelection();
		sel?.removeAllRanges();
		sel?.addRange(range);
		document.dispatchEvent(new Event("selectionchange"));
		para.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
	});
	await p.getByRole("button", { name: "引用回复" }).first().waitFor();
}

// 选中正文：桌面是贴着选区的「引用回复」胶囊，触屏是输入坞上方的引用条
overlay(
	"thread.quote",
	async (p) => {
		await selectParagraph(p);
		return p
			.getByRole("button", { name: "引用回复" })
			.first()
			.locator("xpath=ancestor-or-self::*[contains(@class,'glass')][1]");
	},
	{ noReopen: true },
);

// 引用胶囊里点「解释」：宽屏是锚在选区旁的解释浮卡，手机是对话框
overlay(
	"thread.side-ask",
	async (p) => {
		await selectParagraph(p);
		await p.getByRole("button", { name: "解释" }).first().click();
		await p.getByRole("button", { name: "在旁路提问里打开" }).first().waitFor();
		await settle(p);
	},
	{ noReopen: true },
);

// 输入框里 /btw <问题>：对话框打开到这个话题，左栏话题列表、右栏问答详情
overlay(
	"thread.dlg.btw-topic",
	async (p) => {
		await composer(p).fill(
			"/btw 重连时 afterSeq 补拉会不会漏掉断线那一刻的增量？",
		);
		await composer(p).press("Enter");
		await dialog(p).getByRole("button", { name: "追问" }).first().waitFor();
		await settle(p);
	},
	{ noReopen: true },
);

// ---------------------------------------------------------------- 消息菜单 / 上下文菜单

overlay(
	"thread.message-menu",
	async (p) => {
		// 首条用户消息在顶部完整可见；翻到中段时它可能半截在视口外
		await scrollTranscript(p, 0);
		const msg = p.locator("[data-testid=transcript] [data-role=user]").first();
		await msg.hover();
		await msg.getByRole("button", { name: "消息操作" }).first().click();
		return p.getByRole("menu").last();
	},
	{ noReopen: true },
);

overlay(
	"thread.context-menu",
	async (p, info) => {
		await scrollTranscript(p, 0.5);
		const msg = p
			.locator("[data-testid=transcript] [data-role=assistant] p")
			.first();
		if (isMobile(info)) {
			const box = await msg.boundingBox();
			if (!box) throw new Error("没有消息");
			// 长按：触屏 contextmenu 事件
			await msg.dispatchEvent("contextmenu", {
				clientX: box.x + 20,
				clientY: box.y + 8,
			});
		} else await msg.click({ button: "right" });
		return undefined;
	},
	{ noReopen: true },
);

overlay(
	"thread.tool-context-menu",
	async (p) => {
		await scrollTranscript(p, 0.3);
		const card = p.locator("[data-testid=transcript] .tv-card").first();
		await card.click({ button: "right" });
		return undefined;
	},
	{ path: ZOO, only: "desktop", noReopen: true },
);

// ---------------------------------------------------------------- 输入坞

// 关掉 = 清空输入框（Esc 收起后同一个 `#` / `/` 片段会一直记着「已收起」，再输入同样的文字不会重开）
const clearComposer = { close: (p: Page) => composer(p).fill("") };
overlay(
	"thread.slash",
	async (p) => {
		await composer(p).fill("/");
		return p.getByRole("listbox", { name: "命令" });
	},
	clearComposer,
);
overlay(
	"thread.slash.usage",
	async (p) => {
		await composer(p).fill("/goal ");
		return p.getByTestId("menu-usage");
	},
	clearComposer,
);
overlay(
	"thread.ref-menu",
	async (p) => {
		await composer(p).fill("#log");
		await p.locator("[data-testid=ref-menu] [role=option]").first().waitFor();
		return p.getByTestId("ref-menu");
	},
	clearComposer,
);
overlay("thread.thinking", async (p) => {
	await p.getByRole("button", { name: "思考级别" }).first().click();
	return undefined;
});
overlay("thread.speed", async (p) => {
	await p
		.getByRole("button", { name: /^速度：/ })
		.first()
		.click();
	return undefined;
});
overlay(
	"thread.modes-more",
	async (p) => {
		await p.getByRole("button", { name: "更多模式" }).first().click();
		return undefined;
	},
	{ only: "mobile" },
);
overlay(
	"thread.role-track",
	async (p) => {
		await composer(p).focus();
		await p.keyboard.press("Control+p");
		// 4 秒不动就自动收起：悬停住，截图与断言期间一直显示
		const track = p.getByRole("radiogroup", { name: "角色模型" });
		await track.hover();
		return track.locator("xpath=..");
	},
	{ only: "desktop", noReopen: true },
);
overlay(
	"thread.attachments-tray",
	async (p) => {
		await p.getByTestId("attach-file-input").setInputFiles([
			{
				name: "screenshot-2026-11-21-reconnect-failure-full-page.png",
				mimeType: "image/png",
				buffer: PNG,
			},
			{ name: "a.log", mimeType: "text/plain", buffer: Buffer.from("x") },
			{
				name: "websocket-reconnect-trace-with-very-long-name.json",
				mimeType: "application/json",
				buffer: Buffer.from("{}"),
			},
		]);
		await p.waitForTimeout(800);
		return p.getByRole("button", { name: /^移除/ }).first().locator("xpath=..");
	},
	{ noReopen: true },
);

// ---------------------------------------------------------------- 页头

overlay("thread.actions", (p, info) => openActions(p, info));
/** 子菜单在原菜单里换页：Esc 先回上一页再关菜单。 */
async function closeMenus(p: Page) {
	for (let i = 0; i < 3 && (await p.getByRole("menu").count()) > 0; i++) {
		await p.keyboard.press("Escape");
		await p.waitForTimeout(200);
	}
}
overlay(
	"thread.actions.snooze",
	async (p, info) => {
		await openActions(p, info);
		await p.getByRole("menuitem", { name: "稍后提醒" }).first().click();
		await p.getByRole("menuitem", { name: "1 小时" }).first().waitFor();
	},
	{ close: closeMenus },
);
overlay(
	"thread.actions.color",
	async (p, info) => {
		await openActions(p, info);
		await p.getByRole("menuitem", { name: "颜色" }).first().click();
		await p.getByRole("menuitem", { name: "无" }).first().waitFor();
	},
	{ close: closeMenus },
);
overlay(
	"thread.pr-menu",
	async (p) => {
		await p.getByTestId("pr-chip").first().click();
		return undefined;
	},
	{ only: "desktop" },
);
overlay(
	"thread.view-menu",
	async (p) => {
		await p.locator("[data-testid=thread-view-menu]").first().click();
		await p.locator("[data-view=agent]").waitFor();
		return undefined;
	},
	{ only: "mobile" },
);

// ---------------------------------------------------------------- 对话框

overlay("thread.dlg.tree", (p) => slash(p, "/tree"));
overlay("thread.dlg.fork", (p, info) => action(p, info, "分叉"));
overlay("thread.dlg.model", (p) => slash(p, "/model"));
overlay("thread.dlg.goal", (p) => slash(p, "/goal"));
overlay("thread.dlg.btw", (p) => slash(p, "/btw"));
overlay("thread.dlg.rename", (p, info) => action(p, info, "改名"));
overlay("thread.dlg.labels", (p, info) => action(p, info, "标签"));
overlay("thread.dlg.debug", (p, info) => action(p, info, "调试面板"));
overlay("thread.dlg.attachments", (p, info) => action(p, info, "附件"));
overlay("thread.dlg.share", (p, info) => action(p, info, "分享…"));
overlay("thread.dlg.schedule", (p, info) => action(p, info, /^定时/), {
	path: `/t/${M9_IDS.scheduledThread}`,
});
overlay("thread.dlg.archive", (p, info) => action(p, info, "归档"), {
	path: `/t/${M11B_IDS.worktreeThread}`,
	noReopen: true,
});
overlay("thread.dlg.context", async (p) => {
	await p
		.getByRole("button", { name: /^上下文 \d+%/ })
		.first()
		.click();
	await dialog(p).waitFor();
});
overlay(
	"thread.dlg.label",
	async (p) => {
		await scrollTranscript(p, 0);
		const msg = p.locator("[data-testid=transcript] [data-role=user]").first();
		await msg.hover();
		await msg.getByRole("button", { name: "消息操作" }).first().click();
		await p.getByRole("menuitem", { name: "加标签" }).click();
	},
	{ noReopen: true },
);
overlay(
	"thread.dlg.subagent",
	async (p) => {
		for (let f = 0; f <= 1; f += 0.25) {
			await scrollTranscript(p, f);
			await expandAll(p);
			const link = p.getByRole("button", { name: /^HubAudit/ }).first();
			if (await link.isVisible().catch(() => false)) {
				await link.click();
				return;
			}
		}
		throw new Error("没找到子 agent 链接");
	},
	{ path: ZOO, noReopen: true },
);
overlay(
	"thread.dlg.setup-log",
	async (p) => {
		await p
			.locator("[data-testid=env-banner]")
			.getByRole("button", { name: "查看日志" })
			.click();
		await p.locator("[data-testid=setup-log]").waitFor();
	},
	{ path: `/t/${ENV_THREADS.setupFailed}` },
);
overlay(
	"thread.dlg.share-findings",
	async (p, info) => {
		await composer(p).fill(
			"原样输出这一行：sk-test-abcdefghijklmnopqrstuvwx0123",
		);
		await composer(p).press("Enter");
		await p
			.locator("[data-testid=transcript] [data-role=user]")
			.filter({ hasText: "sk-test-" })
			.last()
			.waitFor();
		await action(p, info, "分享…");
		await dialog(p)
			.getByRole("button", { name: "创建链接", exact: true })
			.click();
		await p.locator("[data-testid=share-findings]").waitFor();
	},
	{ noReopen: true },
);

// ---------------------------------------------------------------- 首页输入卡上的其余浮层
// 首页输入卡没有斜杠菜单（命令只在线程里可用）；附件托盘与线程页同一组件（见 thread.attachments-tray）

surface({
	id: "thread.home.dir-in-use",
	path: "/",
	ready: "textarea",
	noReopen: true,
	open: async (p) => {
		await p.locator("textarea").fill("Fix login flicker");
		await p.getByRole("button", { name: /^local/ }).click();
		await p.getByRole("button", { name: /^laptop/ }).click();
		await p.getByRole("button", { name: /^repo/ }).click();
		await p.keyboard.press("Escape");
		await p.getByRole("button", { name: "发送" }).click();
		await p.locator("[data-testid=dir-in-use]").waitFor();
		return lastOverlay(p);
	},
});

/** 1×1 透明 PNG。 */
const PNG = Buffer.from(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
	"base64",
);
