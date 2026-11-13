import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { handleNotifyFetch, resetNotifyMock } from "../../mock/notify";
import { notificationsStore } from "../../state/notifications";
import NotificationsSettings, {
	buildConfig,
	uaSummary,
} from "./NotificationsSettings";

const push = vi.hoisted(() => ({
	pushSupported: vi.fn(() => true),
	needsHomeScreen: vi.fn(() => false),
	pushRegistration: vi.fn(
		async (): Promise<{ endpoint: string | null; registered: boolean }> => ({
			endpoint: null,
			registered: false,
		}),
	),
	subscribePush: vi.fn(async () => ({
		id: "psub_me",
		kind: "webpush",
		endpoint: "https://push.example/me",
		userAgent: "ua",
		createdAt: 1,
	})),
	unsubscribePush: vi.fn(async () => {}),
}));
vi.mock("../../lib/push", () => push);

interface Call {
	method: string;
	path: string;
	body: unknown;
}
let calls: Call[];

class FakeNotification {
	onclick: (() => void) | null = null;
	static permission: NotificationPermission = "default";
	static requestPermission = vi.fn(async () => {
		FakeNotification.permission = "granted";
		return "granted" as NotificationPermission;
	});
}

beforeEach(() => {
	calls = [];
	resetNotifyMock();
	FakeNotification.permission = "default";
	vi.stubGlobal("Notification", FakeNotification);
	vi.stubGlobal("isSecureContext", true);
	notificationsStore.setState({ pushActive: false });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (req: Request) => {
			const url = new URL(req.url);
			const text = req.method === "GET" ? "" : await req.clone().text();
			calls.push({
				method: req.method,
				path: url.pathname,
				body: text ? JSON.parse(text) : undefined,
			});
			return (
				(await handleNotifyFetch(req)) ?? new Response(null, { status: 404 })
			);
		}),
	);
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
	vi.clearAllMocks();
});

function mount() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={client}>
			<NotificationsSettings />
		</QueryClientProvider>,
	);
}

const posts = (path: string) =>
	calls.filter((c) => c.method === "POST" && c.path === path);

/** 渠道列表里的那一行（同名文字也出现在「最近通知」的投递行里）。 */
async function rowOf(name: string): Promise<HTMLElement> {
	let row: HTMLElement | undefined;
	await waitFor(() => {
		row = screen
			.queryAllByText(name)
			.map((e) => e.closest("li"))
			.find((li): li is HTMLLIElement =>
				Boolean(li && within(li).queryByRole("button", { name: "测试" })),
			);
		expect(row).toBeDefined();
	});
	return row as HTMLElement;
}

async function openAdd(kind: string) {
	fireEvent.click(await screen.findByRole("button", { name: "添加渠道" }));
	fireEvent.click(screen.getByRole("menuitem", { name: kind }));
	return screen.findByRole("dialog", { name: `添加 ${kind} 渠道` });
}

function fill(dialog: HTMLElement, label: string, value: string) {
	fireEvent.change(within(dialog).getByLabelText(label), {
		target: { value },
	});
}

describe("NotificationsSettings 渠道", () => {
	it("四种渠道的创建请求体：配置对象与 kind 同名，空的可选字段不提交", async () => {
		resetNotifyMock({ empty: true });
		mount();
		await screen.findByText(/还没有渠道/);

		let d = await openAdd("Webhook");
		fill(d, "URL", " https://hooks.example.com/x ");
		fill(d, "签名密钥（可选）", "s3cret");
		fill(d, "自定义请求头（可选）", "X-A: 1\nbad line\nX-B:two");
		fireEvent.click(within(d).getByLabelText("需要输入"));
		fireEvent.click(within(d).getByRole("button", { name: "创建" }));
		await waitFor(() =>
			expect(posts("/api/v1/notification-channels")).toHaveLength(1),
		);
		expect(posts("/api/v1/notification-channels")[0]?.body).toEqual({
			kind: "webhook",
			enabled: true,
			name: "Webhook",
			events: ["done", "error"],
			locale: "zh-CN",
			webhook: {
				url: "https://hooks.example.com/x",
				secret: "s3cret",
				headers: { "X-A": "1", "X-B": "two" },
			},
		});

		d = await openAdd("ntfy");
		fill(d, "Topic", "oryn-topic");
		fireEvent.click(within(d).getByRole("button", { name: "创建" }));
		await waitFor(() =>
			expect(posts("/api/v1/notification-channels")).toHaveLength(2),
		);
		expect(posts("/api/v1/notification-channels")[1]?.body).toMatchObject({
			kind: "ntfy",
			ntfy: { topic: "oryn-topic" },
		});

		d = await openAdd("Bark");
		const create = within(d).getByRole("button", { name: "创建" });
		// 必填项没填时不能创建
		expect(create).toBeDisabled();
		fill(d, "服务器（可选）", "https://bark.example");
		fill(d, "Device key", "dk");
		fireEvent.click(create);
		await waitFor(() =>
			expect(posts("/api/v1/notification-channels")).toHaveLength(3),
		);
		expect(posts("/api/v1/notification-channels")[2]?.body).toMatchObject({
			kind: "bark",
			bark: { server: "https://bark.example", deviceKey: "dk" },
		});

		d = await openAdd("Telegram");
		fill(d, "Bot token", "123:abc");
		fireEvent.click(within(d).getByRole("button", { name: "创建" }));
		await waitFor(() =>
			expect(posts("/api/v1/notification-channels")).toHaveLength(4),
		);
		expect(posts("/api/v1/notification-channels")[3]?.body).toMatchObject({
			kind: "telegram",
			telegram: { botToken: "123:abc" },
		});
	});

	it("编辑：机密留空 = 不提交配置对象；填了就整体替换", async () => {
		mount();
		const row = await rowOf("CI 钩子");
		fireEvent.click(within(row).getByRole("button", { name: "编辑" }));
		let d = await screen.findByRole("dialog", { name: "编辑 Webhook 渠道" });
		fill(d, "名称", "CI");
		fireEvent.click(within(d).getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1),
		);
		const patch = calls.find((c) => c.method === "PATCH");
		expect(patch?.path).toBe(
			"/api/v1/notification-channels/nch_01JD0000000000000000000002",
		);
		expect(patch?.body).toEqual({
			name: "CI",
			events: ["done", "error"],
			locale: "en",
		});

		const row2 = await rowOf("CI");
		fireEvent.click(within(row2).getByRole("button", { name: "编辑" }));
		d = await screen.findByRole("dialog", { name: "编辑 Webhook 渠道" });
		// 只填可选的签名密钥：必填的 URL 没填，不能保存（否则会把 URL 整体替换成空）
		fill(d, "签名密钥（可选）", "new");
		expect(within(d).getByRole("button", { name: "保存" })).toBeDisabled();
		fill(d, "URL", "https://hooks.example.com/y");
		fireEvent.click(within(d).getByRole("button", { name: "保存" }));
		await waitFor(() =>
			expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2),
		);
		expect(calls.filter((c) => c.method === "PATCH")[1]?.body).toMatchObject({
			webhook: { url: "https://hooks.example.com/y", secret: "new" },
		});
	});

	it("Telegram 向导：创建后显示 /start <码>，每 2s 拉渠道列表直到已绑定", async () => {
		resetNotifyMock({ empty: true });
		vi.useFakeTimers({ shouldAdvanceTime: true });
		mount();
		const d = await openAdd("Telegram");
		fill(d, "Bot token", "123:abc");
		fireEvent.click(within(d).getByRole("button", { name: "创建" }));
		const link = await screen.findByRole("dialog", { name: "绑定 Telegram" });
		expect(
			await within(link).findByText("/start K7QM-W3XR"),
		).toBeInTheDocument();
		expect(within(link).getByText(/@oryn_mock_bot/)).toBeInTheDocument();
		const gets = () =>
			calls.filter(
				(c) => c.method === "GET" && c.path === "/api/v1/notification-channels",
			).length;
		const before = gets();
		await act(() => vi.advanceTimersByTimeAsync(2_100));
		await act(() => vi.advanceTimersByTimeAsync(2_100));
		expect(gets()).toBeGreaterThan(before);
		expect(await within(link).findByText("已绑定")).toBeInTheDocument();
		fireEvent.click(within(link).getByRole("button", { name: "完成" }));
		expect(await screen.findByText("已绑定 Lyn")).toBeInTheDocument();
	});

	it("测试按钮：结果就地显示（成功 / 渠道报错）", async () => {
		mount();

		const hook = await rowOf("CI 钩子");
		fireEvent.click(within(hook).getByRole("button", { name: "测试" }));
		expect(await within(hook).findByText("测试消息已发送")).toBeInTheDocument();

		const ntfy = await rowOf("手机 ntfy");
		fireEvent.click(within(ntfy).getByRole("button", { name: "测试" }));
		// 哪一步 + 为什么 + 下一步；渠道原文收进「详细信息」
		expect(
			await within(ntfy).findByText("发送测试消息失败：目标服务出错"),
		).toBeInTheDocument();
		expect(
			within(ntfy).getByText(
				"多半是对方暂时故障，稍后再测；一直失败时检查对方服务。",
			),
		).toBeInTheDocument();
		expect(
			within(ntfy).getByText("HTTP 500: upstream unavailable", {
				selector: "details code",
			}),
		).toBeInTheDocument();
	});

	it("测试未绑定的 Telegram：提示先绑定", async () => {
		resetNotifyMock({ empty: true });
		await handleNotifyFetch(
			new Request(`${location.origin}/api/v1/notification-channels`, {
				method: "POST",
				body: JSON.stringify({
					kind: "telegram",
					name: "TG",
					enabled: true,
					telegram: { botToken: "x" },
				}),
			}),
		);
		mount();
		const tg = await rowOf("TG");
		expect(
			within(tg).getByRole("button", { name: "绑定私聊" }),
		).toBeInTheDocument();
		fireEvent.click(within(tg).getByRole("button", { name: "测试" }));
		expect(
			await within(tg).findByText(
				"发送测试消息失败：Telegram 渠道还没绑定聊天",
			),
		).toBeInTheDocument();
		expect(
			within(tg).getByText("先完成绑定，再发送测试消息。"),
		).toBeInTheDocument();
	});
});

describe("NotificationsSettings 此设备", () => {
	it("后台推送开关：先请求通知权限，再调 subscribePush；关掉调 unsubscribePush", async () => {
		mount();
		const toggle = await screen.findByRole("checkbox", {
			name: "后台推送（标签页关闭时也能收到）",
		});
		expect(toggle).not.toBeChecked();
		fireEvent.click(toggle);
		await waitFor(() => expect(push.subscribePush).toHaveBeenCalledTimes(1));
		expect(FakeNotification.requestPermission).toHaveBeenCalled();
		await waitFor(() => expect(toggle).toBeChecked());
		expect(notificationsStore.getState().pushActive).toBe(true);
		fireEvent.click(toggle);
		await waitFor(() => expect(push.unsubscribePush).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(toggle).not.toBeChecked());
	});

	it("本地还留着订阅、服务端登记已被另一台设备移除：开关显示未开启，重新开启会再登记", async () => {
		push.pushRegistration.mockResolvedValueOnce({
			endpoint: "https://push.example/me",
			registered: false,
		});
		notificationsStore.setState({ pushActive: true });
		mount();
		const toggle = await screen.findByRole("checkbox", {
			name: "后台推送（标签页关闭时也能收到）",
		});
		await waitFor(() =>
			expect(notificationsStore.getState().pushActive).toBe(false),
		);
		expect(toggle).not.toBeChecked();
		fireEvent.click(toggle);
		await waitFor(() => expect(push.subscribePush).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(toggle).toBeChecked());
	});

	it("开启推送失败：说明哪一步、为什么与下一步，开关保持未开启", async () => {
		push.subscribePush.mockRejectedValueOnce(
			new DOMException(
				"Registration failed - permission denied",
				"NotAllowedError",
			),
		);
		mount();
		const toggle = await screen.findByRole("checkbox", {
			name: "后台推送（标签页关闭时也能收到）",
		});
		fireEvent.click(toggle);
		expect(
			await screen.findByText("开启推送失败：浏览器拒绝了通知权限"),
		).toBeInTheDocument();
		expect(
			screen.getByText("在浏览器的网站设置里允许通知，然后再开启。"),
		).toBeInTheDocument();
		expect(toggle).not.toBeChecked();
	});

	it("其它已订阅的浏览器：UA 摘要与移除", async () => {
		mount();
		const row = (await screen.findByText("Chrome · macOS")).closest(
			"li",
		) as HTMLElement;
		fireEvent.click(within(row).getByRole("button", { name: "移除订阅" }));
		await waitFor(() =>
			expect(
				calls.some(
					(c) =>
						c.method === "DELETE" &&
						c.path ===
							"/api/v1/push/subscriptions/psub_01JD000000000000000000001",
				),
			).toBe(true),
		);
	});

	it("不是安全上下文时只显示说明", async () => {
		vi.stubGlobal("isSecureContext", false);
		push.pushSupported.mockReturnValue(false);
		mount();
		expect(
			await screen.findByText(/系统通知与后台推送需要 HTTPS/),
		).toBeInTheDocument();
		expect(screen.queryByRole("checkbox", { name: /后台推送/ })).toBeNull();
		push.pushSupported.mockReturnValue(true);
	});
});

describe("最近通知", () => {
	it("各投递的状态：已送达淡色、待重试显示原因、失败显示错误", async () => {
		mount();
		const card = (
			await screen.findByRole("heading", { name: "最近通知" })
		).closest("section") as HTMLElement;
		expect(await within(card).findAllByText(/已送达/)).not.toHaveLength(0);
		const pending = within(card)
			.getByText(/待重试（已尝试 2 次）/)
			.closest("li");
		expect(pending).toHaveClass("text-warn");
		expect(
			within(pending as HTMLElement).getByText(
				"HTTP 500: upstream unavailable",
			),
		).toBeInTheDocument();
		const failed = within(card)
			.getByText(/失败（共 4 次）/)
			.closest("li");
		expect(failed).toHaveClass("text-err");
	});
});

describe("纯函数", () => {
	it("buildConfig：全空 → undefined；headers 解析", () => {
		expect(buildConfig("ntfy", { topic: " ", token: "" })).toBeUndefined();
		expect(buildConfig("webhook", { url: "u", headers: "A: b" })).toEqual({
			url: "u",
			headers: { A: "b" },
		});
	});

	it("uaSummary", () => {
		expect(
			uaSummary(
				"Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Mobile Safari/537.36",
			),
		).toBe("Chrome · Android");
		expect(uaSummary("")).toBe("—");
	});
});
