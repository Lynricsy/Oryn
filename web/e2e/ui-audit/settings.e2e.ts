/**
 * 设置：27 个分区页面（长页另按卡片分段截图），以及分区里的下拉、菜单、弹窗、确认框。
 * 页面级用例先等主内容里的加载转圈消失，避免截到半加载的骨架。
 */
import type { Locator, Page } from "@playwright/test";
import { expect, go, lastOverlay, PHASE, surface } from "./audit";

/** 等设置页主内容加载完：网络空闲且主滚动区里没有可见的转圈。 */
async function loaded(page: Page) {
	await page.waitForLoadState("networkidle").catch(() => undefined);
	await page
		.locator("main [role=status].animate-spin")
		.filter({ visible: true })
		.first()
		.waitFor({ state: "hidden", timeout: 10_000 })
		.catch(() => undefined);
}

const READY = "h1:has-text('设置')";

/** 主内容里按名字找按钮（排除侧栏里的同名入口）。 */
const btn = (page: Page, name: string | RegExp) =>
	page.locator("main").getByRole("button", { name }).first();

/** 打开一个对话框；已经开着（关掉浮层后对话框还在）就不再点触发按钮。 */
async function dialog(
	page: Page,
	title: string | RegExp,
	trigger: () => Promise<void>,
) {
	const d = page.getByRole("dialog", { name: title });
	if (!(await d.isVisible())) {
		await loaded(page);
		await trigger();
	}
	await d.waitFor({ state: "visible" });
	return d;
}

/**
 * 把主内容里某段文字所在的卡片滚到视口顶部（长页面分段截图）。只滚最近的可滚动祖先：
 * scrollIntoView 会连 overflow:hidden 的外壳一起滚，截出页头被推走的假画面。
 * 跳过页内目录里的同名标签；停靠位置让出滚动区声明的 `scroll-padding-top`（吸顶目录的高度），没有就留 12px。
 */
async function scrollTo(page: Page, text: string | RegExp) {
	await loaded(page);
	await page
		.locator("main")
		.getByText(text)
		.and(page.locator(":not([data-testid=memory-toc] *)"))
		.first()
		.evaluate(async (e) => {
			let s = e.parentElement;
			while (s && !/(auto|scroll)/.test(getComputedStyle(s).overflowY))
				s = s.parentElement;
			if (s)
				s.scrollTop +=
					e.getBoundingClientRect().top -
					s.getBoundingClientRect().top -
					(Number.parseFloat(s.style.scrollPaddingTop) || 12);
			const { promise, resolve } = Promise.withResolvers();
			requestAnimationFrame(() => requestAnimationFrame(resolve));
			await promise;
		});
}

/** 页面级界面（可选滚到某段）。 */
function pageSurface(id: string, section: string, anchor?: string | RegExp) {
	surface({
		id,
		path: `/settings/${section}`,
		ready: READY,
		open: async (page) => {
			await loaded(page);
			if (anchor) await scrollTo(page, anchor);
			return undefined;
		},
	});
}

/** 弹层界面：`open` 只负责点开，返回值统一取最后一个浮层。 */
function overlay(
	id: string,
	section: string,
	open: (page: Page) => Promise<unknown>,
	extra: { only?: "desktop" | "mobile"; noReopen?: boolean } = {},
) {
	surface({
		id,
		path: `/settings/${section}`,
		ready: READY,
		...extra,
		open: async (page): Promise<Locator> => {
			await loaded(page);
			await open(page);
			return lastOverlay(page);
		},
	});
}

// ---------------------------------------------------------------- 页面

const SECTIONS = [
	"devices",
	"providers",
	"runners",
	"secrets",
	"browser-profiles",
	"git-identity",
	"github",
	"agent-skills",
	"agent-subagents",
	"agent-roles",
	"agent-prompts",
	"agent-commands",
	"agent-extensions",
	"agent-memory",
	"agent-ssh",
	"agent-composer",
	"selection-actions",
	"agent-config",
	"identity",
	"assistant",
	"appearance",
	"notifications",
	"shares",
	"artifacts",
	"automations",
	"about",
] as const;

for (const s of SECTIONS) pageSurface(`settings.${s}`, s);

// 长页面：按卡片分段
pageSurface("settings.github.automation", "github", "自动化");
pageSurface("settings.agent-roles.others", "agent-roles", "其他角色");
pageSurface("settings.agent-ssh.audit", "agent-ssh", "网关审计");
pageSurface("settings.agent-memory.models", "agent-memory", /^模型$/);
pageSurface("settings.agent-memory.browse", "agent-memory", /^浏览$/);
// 状态卡里也有一行「待批准」：锚在紧挨着的「全局记忆」卡片标题上，一屏里是全局记忆、待批准与 mental models
pageSurface("settings.agent-memory.candidates", "agent-memory", /^全局记忆$/);
pageSurface("settings.agent-memory.usage", "agent-memory", /^用量$/);
pageSurface("settings.agent-memory.owner", "agent-memory", "助手记忆");
pageSurface("settings.agent-memory.owner-facts", "agent-memory", /^事实$/);
// 页内目录：点「全局记忆」跳过去。只滚内容区（文档不动、页头还在），目录贴着内容区上沿吸顶，
// 跳到的节停在目录下方不被压住，目录高亮跟到这一节。基线没有目录，before 只截页顶
surface({
	id: "settings.agent-memory.toc",
	path: "/settings/agent-memory",
	ready: READY,
	open: async (page) => {
		await loaded(page);
		if (PHASE === "before") return undefined;
		const toc = page.getByTestId("memory-toc");
		await toc.getByRole("button", { name: "全局记忆", exact: true }).click();
		await expect(toc.locator("[aria-current]")).toHaveText("全局记忆");
		await expect
			.poll(() =>
				page.evaluate(() => {
					const nav = document.querySelector("[data-testid=memory-toc]");
					const sec = document.getElementById("memory-global");
					let s = nav?.parentElement ?? null;
					while (s && !/(auto|scroll)/.test(getComputedStyle(s).overflowY))
						s = s.parentElement;
					if (!nav || !sec || !s) return "缺元素";
					const n = nav.getBoundingClientRect();
					const top = sec.getBoundingClientRect().top;
					const edge = s.getBoundingClientRect().top;
					if (document.scrollingElement?.scrollTop) return "文档被滚动";
					if (Math.abs(n.top - edge) > 1) return `目录没贴上沿 ${n.top} vs ${edge}`;
					if (top < n.bottom || top > n.bottom + 16)
						return `节顶 ${top} 不在目录下沿 ${n.bottom} 之下 16px 内`;
					return "ok";
				}),
			)
			.toBe("ok");
		return undefined;
	},
});
pageSurface("settings.agent-memory.owner-sources", "agent-memory", /^来源$/);
pageSurface("settings.identity.assistant", "identity", "全局助手");
pageSurface("settings.notifications.recent", "notifications", "最近通知");
pageSurface("settings.automations.actions", "automations", "操作记录");

/**
 * 壳里才有的卡片：注入一个假的 Tauri 宿主再进页面。桌面壳（linux）有「桌面应用」分区与桌面通知卡；
 * 移动壳（iOS bridge 6）有推送卡、App 图标与「更换服务器」。原生页头 / 底栏命令一律拒绝，页面退回网页版页头与底栏。
 */
function shellSurface(
	id: string,
	section: string,
	shell: "desktop" | "mobile",
	anchor?: string | RegExp,
) {
	surface({
		id,
		path: "/settings/about",
		ready: READY,
		only: shell,
		open: async (page) => {
			await page.addInitScript((kind) => {
				const desktop = {
					version: "0.9.3",
					settings: {
						shortcut: "CommandOrControl+Shift+Space",
						closeToTray: true,
						autoUpdate: true,
					},
					shortcutError: null,
					tokenStore: "keyring",
					update: { state: "available", version: "0.9.4", error: null },
				};
				const mobile = {
					version: "0.9.3 (412)",
					platform: "ios",
					tokenStore: "keychain",
					webview: "WebKit 619.1.26 (iOS 18.2)",
					push: {
						state: "active",
						channel: "fcm",
						distributor: null,
						endpointHost: "fcm.googleapis.com",
						error: null,
					},
				};
				const reply: Record<string, unknown> = {
					host_ready: { bridge: 6, visible: true },
					host_info: desktop,
					host_set_settings: desktop,
					host_check_update: {
						version: "0.9.4",
						notes: "修复托盘图标在 KDE 下错位。",
					},
					host_mobile_info: mobile,
					host_push_enable: mobile,
					host_push_disable: mobile,
					host_app_icon_get: {
						supported: true,
						current: "default",
						options: ["default", "accent"],
					},
				};
				const w = window as unknown as Record<string, unknown>;
				w.__ORYN_HOST__ = {
					kind: "tauri",
					platform: kind === "desktop" ? "linux" : "ios",
					bridge: 6,
				};
				w.__TAURI_INTERNALS__ = {
					invoke: async (cmd: string) => {
						if (/^host_(header|tab_bar)_/.test(cmd))
							throw new Error("native chrome off");
						return reply[cmd] ?? null;
					},
				};
			}, shell);
			await go(page, `/settings/${section}`, READY);
			await loaded(page);
			if (anchor) await scrollTo(page, anchor);
			return undefined;
		},
	});
}
shellSurface("settings.desktop", "desktop", "desktop");
shellSurface("settings.desktop.notifications", "notifications", "desktop");
shellSurface("settings.desktop.about", "about", "desktop");
shellSurface("settings.mobile.notifications", "notifications", "mobile");
shellSurface("settings.mobile.appearance", "appearance", "mobile", "壁纸");
shellSurface("settings.mobile.about", "about", "mobile");

// 手机：分区导航在侧栏抽屉里（抽屉是 <nav>，不是浮层；关抽屉要点遮罩，不做关掉重开）
surface({
	id: "settings.nav",
	path: "/settings/devices",
	ready: READY,
	only: "mobile",
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		await page.getByRole("button", { name: "侧边栏", exact: true }).click();
		const nav = page.getByRole("navigation", { name: "侧边栏" });
		await nav.waitFor({ state: "visible" });
		return nav;
	},
});

// ---------------------------------------------------------------- 自动化（定时对话框）

const NEW_SCHEDULE = "新建定时任务";
const newSchedule = (page: Page) =>
	dialog(page, NEW_SCHEDULE, () => btn(page, "创建").click());

overlay("settings.automations.new", "automations", newSchedule);
// 用户报告的下拉：定时对话框里的「对象」弹层（下方是复选框、输入框，页脚是启用开关）
overlay("settings.automations.new.role-picker", "automations", async (page) => {
	const d = await newSchedule(page);
	await d.getByRole("button", { name: /^对象：/ }).click();
});
overlay(
	"settings.automations.new.model-picker",
	"automations",
	async (page) => {
		const d = await newSchedule(page);
		await d.getByRole("button", { name: /^对象：/ }).click();
		await page.getByRole("button", { name: /指定模型/ }).click();
	},
);
overlay("settings.automations.edit", "automations", (page) =>
	dialog(page, "编辑定时", () => btn(page, "nightly").click()),
);
overlay("settings.automations.row-menu", "automations", (page) =>
	btn(page, "「nightly」的操作").click(),
);

// ---------------------------------------------------------------- SSH

overlay("settings.agent-ssh.host-new", "agent-ssh", (page) =>
	dialog(page, /主机/, () => btn(page, "添加主机").click()),
);
overlay("settings.agent-ssh.host-menu", "agent-ssh", (page) =>
	btn(page, "bastion 的操作").click(),
);
overlay("settings.agent-ssh.host-edit", "agent-ssh", (page) =>
	dialog(page, /编辑主机/, async () => {
		await btn(page, "prod-web 的操作").click();
		await page.getByRole("menuitem", { name: "编辑" }).click();
	}),
);
overlay("settings.agent-ssh.host-delete", "agent-ssh", async (page) => {
	await btn(page, "down 的操作").click();
	await page.getByRole("menuitem", { name: "删除" }).click();
});
overlay("settings.agent-ssh.host-files", "agent-ssh", async (page) => {
	await btn(page, "bastion 的操作").click();
	await page.getByRole("menuitem", { name: /文件/ }).click();
});
overlay("settings.agent-ssh.key-generate", "agent-ssh", (page) =>
	btn(page, "生成钥匙").click(),
);
overlay("settings.agent-ssh.key-import", "agent-ssh", (page) =>
	btn(page, "导入私钥").click(),
);
overlay("settings.agent-ssh.key-menu", "agent-ssh", (page) =>
	btn(page, "deploy 的操作").click(),
);
overlay("settings.agent-ssh.key-rename", "agent-ssh", async (page) => {
	await btn(page, "deploy 的操作").click();
	await page.getByRole("menuitem", { name: /重命名/ }).click();
});
overlay("settings.agent-ssh.key-delete", "agent-ssh", async (page) => {
	await btn(page, "legacy rsa 的操作").click();
	await page.getByRole("menuitem", { name: "删除" }).click();
});
surface({
	id: "settings.agent-ssh.test-result",
	path: "/settings/agent-ssh",
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		await btn(page, "测试").click();
		await loaded(page);
		return undefined;
	},
});

// ---------------------------------------------------------------- Agent 子页

overlay("settings.agent-skills.new", "agent-skills", (page) =>
	btn(page, "新建").click(),
);
overlay("settings.agent-skills.edit", "agent-skills", (page) =>
	btn(page, "编辑 review").click(),
);
overlay("settings.agent-skills.delete", "agent-skills", (page) =>
	btn(page, "删除 review").click(),
);
overlay("settings.agent-commands.new", "agent-commands", (page) =>
	btn(page, "新建").click(),
);
overlay("settings.agent-subagents.new", "agent-subagents", (page) =>
	btn(page, "新建").click(),
);
surface({
	id: "settings.agent-subagents.expanded",
	path: "/settings/agent-subagents",
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		await btn(page, "设置 task 的模型与头像").click();
		await loaded(page);
		return undefined;
	},
});
overlay("settings.agent-roles.new", "agent-roles", (page) =>
	btn(page, "新建角色").click(),
);
surface({
	id: "settings.agent-roles.expanded",
	path: "/settings/agent-roles",
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		await btn(page, "设置 默认").click();
		await loaded(page);
		return undefined;
	},
});
overlay("settings.agent-prompts.edit", "agent-prompts", (page) =>
	btn(page, "编辑 AGENTS.md").click(),
);
overlay("settings.agent-prompts.delete", "agent-prompts", (page) =>
	btn(page, "删除 AGENTS.md").click(),
);
overlay("settings.agent-extensions.new", "agent-extensions", (page) =>
	btn(page, "在「扩展模块」中新建").click(),
);
overlay("settings.agent-config.file-menu", "agent-config", (page) =>
	btn(page, "config.yml 的操作").click(),
);
overlay("settings.agent-config.new-file", "agent-config", (page) =>
	btn(page, "新建文件").click(),
);
surface({
	id: "settings.agent-config.file-view",
	path: "/settings/agent-config",
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		await page.locator("main").getByText("config.yml", { exact: true }).click();
		await loaded(page);
		return undefined;
	},
});
overlay("settings.selection-actions.new", "selection-actions", (page) =>
	btn(page, "添加动作").click(),
);

// ---------------------------------------------------------------- 记忆

overlay("settings.agent-memory.env-file", "agent-memory", (page) =>
	btn(page, "重新生成 .env").click(),
);
overlay("settings.agent-memory.owner-brief", "agent-memory", (page) =>
	btn(page, "注入预览").click(),
);
overlay("settings.agent-memory.owner-forget", "agent-memory", (page) =>
	btn(page, "按来源遗忘").click(),
);

// ---------------------------------------------------------------- 通知

overlay("settings.notifications.add-menu", "notifications", (page) =>
	btn(page, "添加渠道").click(),
);
overlay("settings.notifications.channel-new", "notifications", async (page) => {
	await btn(page, "添加渠道").click();
	await page.getByRole("menuitem", { name: /Webhook/ }).click();
});
overlay("settings.notifications.channel-edit", "notifications", (page) =>
	btn(page, "编辑").click(),
);

// ---------------------------------------------------------------- 其他分区

overlay("settings.github.connect", "github", (page) =>
	btn(page, "连接").click(),
);
overlay(
	"settings.identity.crop",
	"identity",
	async (page) => {
		await page.locator("main input[type=file]").first().setInputFiles({
			name: "avatar.png",
			mimeType: "image/png",
			buffer: AVATAR_PNG,
		});
	},
	{ noReopen: true },
);
surface({
	id: "settings.providers.created",
	path: "/settings/providers",
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		await page
			.locator("main")
			.getByPlaceholder(/名称/)
			.fill("gpu-box-shanghai-02");
		await btn(page, "创建").click();
		await loaded(page);
		return undefined;
	},
});
surface({
	id: "settings.runners.created",
	path: "/settings/runners",
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		await page.locator("main").getByPlaceholder(/名字/).fill("workstation-lab");
		await btn(page, "创建").click();
		await loaded(page);
		return undefined;
	},
});
surface({
	id: "settings.devices.pair",
	path: "/settings/devices",
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		await page.locator("main").getByPlaceholder("ABCD-EFGH").fill("ABCD-EFGH");
		await btn(page, "查看").click();
		await loaded(page);
		return undefined;
	},
});

/** 96×64 的渐变 PNG（裁剪对话框用）。 */
const AVATAR_PNG = Buffer.from(
	"iVBORw0KGgoAAAANSUhEUgAAAGAAAABACAMAAADS6oI8AAAApVBMVEUqnY8tno4wno42n405oIw8oYtCoopFo4pIo4lLpIlOpIhRpYdUpodbp4ZeqIVhqIRkqYRnqYNqqoNtq4Jwq4FzrIF2rIB5rYB8rn+Cr36FsH2IsH2OsXyRsnuUs3qXs3qatHmdtXmgtXimtnejtnipt3asuHayuXW1uXS4unO8u3PCvHLFvXHIvXDOvm/Rv2/UwG7awW3dwmzgwmzjw2vmw2vpxGo31QjwAAABTElEQVRYw7XNiVKDMBSF4aOhuAXrbqvYKmBx3/X9H80JSVugtg6j/zfMzc3NYa5EW4NpHVYtMI2R+aFbX5L4PS9josgEUeT6KPTG1OfVm/9Ml7wimHow9eI4dk2thrI47y2Zr8orhmkDpk2YtmDahmkHJgtTYq1NkkZNrE1sVRMfCjP/3jGvBKZdmPow7XXU75jXPkwHMB3CdATTMUwnMJ3CNICFBcPB6vMPC4YwncF0DqsWpKm/hGPxPp+ltX/T2mBxHvJKYbqAaQTTaDweV92y0/ezw7fT2o405+7QJUxXMGUw5UWWZUVR5LnrfG3cXVMUhRtm/qmS5+EScu4H997OK4fpGqYJzC0oy3Apy6pt3ad1Mnsqp8+1PuQn7bxKmG5guoXpDsYvuIfpAaZHGL/gCaZnmF5g/IJXmN5geofpAzZf8Fmb/mOvL9g36FuH42FE7IAAAAAASUVORK5CYII=",
	"base64",
);
