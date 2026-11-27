/**
 * 设置：各分区页面（id 同 src/pages/settings/registry.ts；长页另按分组分段截图），以及分区里的下拉、菜单、弹窗、确认框。
 * 页面级用例先等主内容里的加载转圈消失，避免截到半加载的骨架。
 */
import type { Locator, Page } from "@playwright/test";
import { expect, go, lastOverlay, surface } from "./audit";

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

/** 设置壳的标题：宽屏是「设置」，手机分区页是分区名，手机首页是「设置」。 */
const READY = "header h1";

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

/** 确认真的停在该分区：标题选择器在任何设置页都成立，被重定向到别处时会拿错页截图。 */
async function onSection(page: Page, section: string) {
	await expect
		.poll(() => new URL(page.url()).pathname)
		.toBe(`/settings/${section}`);
}

/** 页面级界面（可选滚到某段）。 */
function pageSurface(id: string, section: string, anchor?: string | RegExp) {
	surface({
		id,
		path: `/settings/${section}`,
		ready: READY,
		open: async (page) => {
			await onSection(page, section);
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
	"account",
	"appearance",
	"input",
	"notifications",
	"assistant",
	"agent-models",
	"agent-subagents",
	"agent-instructions",
	"agent-extensions",
	"agent-memory",
	"agent-config",
	"automations",
	"compute",
	"server",
	"secrets",
	"ssh",
	"git",
	"browser-profiles",
	"sharing",
	"about",
] as const;

for (const s of SECTIONS) pageSurface(`settings.${s}`, s);

// 长页面：按分组分段
pageSurface("settings.git.accounts", "git", "GitHub 账号");
pageSurface("settings.agent-models.others", "agent-models", "其他角色");
pageSurface("settings.ssh.audit", "ssh", "网关审计");
pageSurface("settings.agent-memory.models", "agent-memory", /^模型$/);
pageSurface("settings.agent-memory.browse", "agent-memory", /^浏览$/);
// 状态卡里也有一行「待批准」：锚在紧挨着的「全局记忆」卡片标题上，一屏里是全局记忆、待批准与 mental models
pageSurface("settings.agent-memory.candidates", "agent-memory", /^全局记忆$/);
pageSurface("settings.agent-memory.usage", "agent-memory", /^用量$/);
// 助手记忆（M17）在「全局助手」分区；全局助手身份是该分区第一组，页顶截图 settings.assistant 已覆盖
pageSurface("settings.assistant.owner", "assistant", "助手记忆");
pageSurface("settings.assistant.owner-facts", "assistant", /^事实$/);
pageSurface("settings.assistant.owner-sources", "assistant", /^来源$/);
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
						autostart: false,
						startHidden: false,
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
			await onSection(page, section);
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

// 手机：`/settings` 是分组首页，页内导航（搜索 + 四组分区）就是整页内容
surface({
	id: "settings.nav",
	path: "/settings",
	ready: READY,
	only: "mobile",
	open: async (page) => {
		await loaded(page);
		const nav = page.getByRole("navigation", { name: "设置", exact: true });
		await nav.waitFor({ state: "visible" });
		await expect(nav.getByRole("link", { name: /通知与预算/ })).toBeVisible();
		// 首页导航是整页内容，不是浮层：按整页审计，不做关掉重开
		return undefined;
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

overlay("settings.ssh.host-new", "ssh", (page) =>
	dialog(page, /主机/, () => btn(page, "添加主机").click()),
);
overlay("settings.ssh.host-menu", "ssh", (page) =>
	btn(page, "bastion 的操作").click(),
);
overlay("settings.ssh.host-edit", "ssh", (page) =>
	dialog(page, /编辑主机/, async () => {
		await btn(page, "prod-web 的操作").click();
		await page.getByRole("menuitem", { name: "编辑" }).click();
	}),
);
overlay("settings.ssh.host-delete", "ssh", async (page) => {
	await btn(page, "down 的操作").click();
	await page.getByRole("menuitem", { name: "删除" }).click();
});
overlay("settings.ssh.host-files", "ssh", async (page) => {
	await btn(page, "bastion 的操作").click();
	await page.getByRole("menuitem", { name: /文件/ }).click();
});
overlay("settings.ssh.key-generate", "ssh", (page) =>
	btn(page, "生成钥匙").click(),
);
overlay("settings.ssh.key-import", "ssh", (page) =>
	btn(page, "导入私钥").click(),
);
overlay("settings.ssh.key-menu", "ssh", (page) =>
	btn(page, "deploy 的操作").click(),
);
overlay("settings.ssh.key-rename", "ssh", async (page) => {
	await btn(page, "deploy 的操作").click();
	await page.getByRole("menuitem", { name: /重命名/ }).click();
});
overlay("settings.ssh.key-delete", "ssh", async (page) => {
	await btn(page, "legacy rsa 的操作").click();
	await page.getByRole("menuitem", { name: "删除" }).click();
});
surface({
	id: "settings.ssh.test-result",
	path: "/settings/ssh",
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		// 「测试」是悬停才出现的行尾动作：先悬停到第一行
		await page.locator("main [data-testid=ssh-host-row]").first().hover();
		await btn(page, "测试").click();
		await loaded(page);
		return undefined;
	},
});

// ---------------------------------------------------------------- Agent 子页

/**
 * 点悬停才出现的行尾动作（`Reveal`）：先悬停这一行，等动作展开就位再点，跟鼠标用户的顺序一致。
 * 直接点会按收起时（宽 0）的位置瞄准，落到后面的开关上。
 */
async function rowAction(page: Page, name: string) {
	const b = btn(page, name);
	await b.locator("xpath=ancestor::li[1]").hover();
	await b.click();
}

overlay("settings.agent-instructions.new", "agent-instructions", (page) =>
	btn(page, "新建").click(),
);
overlay("settings.agent-instructions.edit", "agent-instructions", (page) =>
	rowAction(page, "编辑 review"),
);
overlay("settings.agent-instructions.delete", "agent-instructions", (page) =>
	rowAction(page, "删除 review"),
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
overlay("settings.agent-models.new", "agent-models", (page) =>
	btn(page, "新建角色").click(),
);
surface({
	id: "settings.agent-models.expanded",
	path: "/settings/agent-models",
	ready: READY,
	noReopen: true,
	open: async (page) => {
		await loaded(page);
		await btn(page, "设置 默认").click();
		await loaded(page);
		return undefined;
	},
});
overlay(
	"settings.agent-instructions.prompt-edit",
	"agent-instructions",
	(page) => rowAction(page, "编辑 AGENTS.md"),
);
overlay(
	"settings.agent-instructions.prompt-delete",
	"agent-instructions",
	(page) => rowAction(page, "删除 AGENTS.md"),
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
overlay("settings.input.selection-action-new", "input", (page) =>
	btn(page, "添加动作").click(),
);

// ---------------------------------------------------------------- 记忆

overlay("settings.agent-memory.env-file", "agent-memory", (page) =>
	btn(page, "重新生成 .env").click(),
);
overlay("settings.assistant.owner-brief", "assistant", (page) =>
	btn(page, "注入预览").click(),
);
overlay("settings.assistant.owner-forget", "assistant", (page) =>
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

overlay("settings.git.connect", "git", (page) => btn(page, "连接").click());
overlay(
	"settings.account.crop",
	"account",
	async (page) => {
		await page.locator("main input[type=file]").first().setInputFiles({
			name: "avatar.png",
			mimeType: "image/png",
			buffer: AVATAR_PNG,
		});
	},
	{ noReopen: true },
);
overlay("settings.assistant.mcp-research-tools", "assistant", (page) =>
	btn(page, /^研究可用工具/).click(),
);
/** 算力：Provider / Runner 分组的「新建」→ 对话框填名字 → 一次性令牌。 */
function computeCreated(id: string, group: string, name: string) {
	surface({
		id,
		path: "/settings/compute",
		ready: READY,
		noReopen: true,
		open: async (page) => {
			await loaded(page);
			await page
				.locator("main")
				.getByRole("region", { name: group, exact: true })
				.getByRole("button", { name: "新建" })
				.click();
			const d = page.getByRole("dialog");
			await d.getByRole("textbox", { name: "名字" }).fill(name);
			await d.getByRole("button", { name: "创建" }).click();
			await d.getByTestId("compute-token").waitFor();
			return d;
		},
	});
}
computeCreated(
	"settings.compute.provider-created",
	"Provider",
	"gpu-box-shanghai-02",
);
computeCreated("settings.compute.runner-created", "Runner", "workstation-lab");
surface({
	id: "settings.account.pair",
	path: "/settings/account",
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
