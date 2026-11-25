/** 设置 → 通知 → 此设备（移动壳）：各推送状态的呈现、三种 active 通道、通道切换、网关错误码、启用 / 关闭、测试通知、壳推来 mobile 后重取。 */
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import {
	afterEach,
	beforeEach,
	describe,
	expect,
	it,
	type Mock,
	vi,
} from "vitest";
import {
	host,
	type MobileHost,
	type MobileInfo,
	type MobilePushState,
} from "../../lib/host";
import MobilePushCard, { NTFY_GUIDE } from "./MobilePushCard";

function makeInfo(push: MobileInfo["push"]): MobileInfo {
	return {
		version: "0.1.0",
		platform: "android",
		tokenStore: "keystore",
		webview: "141.0.7390.0",
		push,
	};
}

let info: MobileInfo;
let fake: {
	[K in Exclude<
		keyof MobileHost,
		"sink" | "tabBar" | "header" | "appIcon" | "pushTransport"
	>]: Mock<MobileHost[K]>;
} & {
	pushTransport: boolean;
	sink: null;
	tabBar: null;
	header: null;
	appIcon: null;
};
let mobileCb: (() => void) | undefined;
const h = host as { mobile: MobileHost | null };

beforeEach(() => {
	info = makeInfo({ state: "disabled" });
	fake = {
		info: vi.fn(async () => info),
		enablePush: vi.fn(async () => info),
		disablePush: vi.fn(async () => info),
		openExternal: vi.fn(),
		shareAck: vi.fn(),
		setChrome: vi.fn(),
		openNotificationSettings: vi.fn(),
		syncAssistantAvatar: vi.fn(),
		changeServer: vi.fn(),
		pushTransport: true,
		sink: null,
		tabBar: null,
		header: null,
		appIcon: null,
	};
	h.mobile = fake;
	vi.spyOn(host, "onMobile").mockImplementation((cb) => {
		mobileCb = cb;
		return () => {
			mobileCb = undefined;
		};
	});
});

afterEach(() => {
	h.mobile = null;
	vi.restoreAllMocks();
});

async function mountWith(push: MobileInfo["push"]) {
	info = makeInfo(push);
	render(<MobilePushCard />);
	await waitFor(() =>
		expect(screen.getByTestId("mobile-push-card")).toHaveAttribute(
			"data-push-state",
			push.state,
		),
	);
	return screen.getByTestId("mobile-push-card");
}

describe("MobilePushCard", () => {
	it("disabled：说明 + 启用；启用调 enablePush 并用返回的状态刷新", async () => {
		await mountWith({ state: "disabled" });
		expect(screen.getByText(/启用后，App 在后台/)).toBeInTheDocument();
		fake.enablePush.mockResolvedValueOnce(
			makeInfo({ state: "registering", channel: "fcm" }),
		);
		fireEvent.click(screen.getByTestId("mobile-push-enable"));
		expect(fake.enablePush).toHaveBeenCalledTimes(1);
		expect(await screen.findByText("正在注册推送…")).toBeInTheDocument();
		expect(screen.getByTestId("mobile-push-card")).toHaveAttribute(
			"data-push-state",
			"registering",
		);
	});

	it("按钮调用失败：显示错误，按钮恢复可用", async () => {
		await mountWith({ state: "disabled" });
		fake.enablePush.mockRejectedValueOnce(new Error("plugin down"));
		const btn = screen.getByTestId("mobile-push-enable");
		fireEvent.click(btn);
		expect(await screen.findByRole("alert")).toHaveTextContent("plugin down");
		expect(btn).not.toBeDisabled();
	});

	it("no_distributor：UnifiedPush 说明、安装说明打开 ntfy 页、重试再 enablePush", async () => {
		await mountWith({ state: "no_distributor", channel: "unifiedpush" });
		expect(
			screen.getByText(/UnifiedPush 分发器（推荐 ntfy）/),
		).toBeInTheDocument();
		fireEvent.click(screen.getByTestId("mobile-push-guide"));
		expect(fake.openExternal).toHaveBeenCalledWith(NTFY_GUIDE);
		fireEvent.click(screen.getByTestId("mobile-push-retry"));
		await waitFor(() => expect(fake.enablePush).toHaveBeenCalledTimes(1));
	});

	it("permission_denied：打开系统通知设置", async () => {
		await mountWith({ state: "permission_denied" });
		expect(screen.getByText(/系统已关闭 Oryn 的通知权限/)).toBeInTheDocument();
		fireEvent.click(screen.getByRole("button", { name: "打开系统通知设置" }));
		expect(fake.openNotificationSettings).toHaveBeenCalledTimes(1);
	});

	it("registering：转圈 + 文案", async () => {
		await mountWith({ state: "registering" });
		expect(screen.getByText("正在注册推送…")).toBeInTheDocument();
	});

	it("active（FCM）：经官方推送网关的通道文案、data-push-via、发送测试通知、关闭推送", async () => {
		const notify = vi.spyOn(host, "notify").mockImplementation(() => {});
		const card = await mountWith({ state: "active", channel: "fcm" });
		expect(card).toHaveAttribute("data-push-via", "fcm");
		expect(screen.getByTestId("mobile-push-via")).toHaveTextContent(
			"经官方推送网关与 Firebase 云消息（FCM）接收推送",
		);
		expect(screen.queryByTestId("mobile-push-switch")).toBeNull();
		fireEvent.click(screen.getByTestId("mobile-push-test"));
		expect(notify).toHaveBeenCalledWith(
			expect.objectContaining({
				title: "Oryn 测试通知",
				path: "/settings/notifications",
			}),
		);
		expect(screen.getByRole("status")).toHaveTextContent("已发送");
		fake.disablePush.mockResolvedValueOnce(makeInfo({ state: "disabled" }));
		fireEvent.click(screen.getByTestId("mobile-push-disable"));
		expect(await screen.findByTestId("mobile-push-enable")).toBeInTheDocument();
		expect(fake.disablePush).toHaveBeenCalledTimes(1);
	});

	it("active（APNs）：经官方推送网关的 APNs 文案，没有切换按钮", async () => {
		const card = await mountWith({ state: "active", channel: "apns" });
		expect(card).toHaveAttribute("data-push-via", "apns");
		expect(screen.getByTestId("mobile-push-via")).toHaveTextContent(
			"经官方推送网关与 Apple 推送服务（APNs）接收推送",
		);
		expect(screen.queryByTestId("mobile-push-switch")).toBeNull();
	});

	it("gms 包（alt = unifiedpush）：切换按钮以 unifiedpush 调 enablePush 并刷新状态", async () => {
		await mountWith({ state: "active", channel: "fcm", alt: "unifiedpush" });
		fake.enablePush.mockResolvedValueOnce(
			makeInfo({ state: "registering", channel: "unifiedpush", alt: "fcm" }),
		);
		const btn = screen.getByTestId("mobile-push-switch");
		expect(btn).toHaveTextContent("改用 UnifiedPush");
		fireEvent.click(btn);
		expect(fake.enablePush).toHaveBeenCalledWith("unifiedpush");
		expect(await screen.findByText("正在注册推送…")).toBeInTheDocument();
	});

	it("已改用 UnifiedPush（alt = fcm）：切回网关用 default，重试沿用 unifiedpush", async () => {
		await mountWith({
			state: "no_distributor",
			channel: "unifiedpush",
			alt: "fcm",
		});
		const btn = screen.getByTestId("mobile-push-switch");
		expect(btn).toHaveTextContent("改用官方推送网关（FCM）");
		fireEvent.click(screen.getByTestId("mobile-push-retry"));
		await waitFor(() => expect(fake.enablePush).toHaveBeenCalledTimes(1));
		expect(fake.enablePush).toHaveBeenLastCalledWith("unifiedpush");
		fireEvent.click(btn);
		await waitFor(() => expect(fake.enablePush).toHaveBeenCalledTimes(2));
		expect(fake.enablePush).toHaveBeenLastCalledWith("default");
	});

	it("旧壳（bridge < 9）：有 alt 也不显示切换按钮", async () => {
		fake.pushTransport = false;
		await mountWith({ state: "active", channel: "fcm", alt: "unifiedpush" });
		expect(screen.queryByTestId("mobile-push-switch")).toBeNull();
	});

	it("error（gateway_untrusted）：专属文案，不用通用错误文案", async () => {
		await mountWith({ state: "error", error: "gateway_untrusted" });
		expect(screen.getByTestId("mobile-push-error")).toHaveTextContent(
			"服务器指定的推送网关不在本 App 信任的列表里，已拒绝向它发送推送令牌。",
		);
		expect(screen.queryByText(/推送注册失败/)).toBeNull();
	});

	it("error（其它 gateway_* 码）：各有专属文案；带详情的码按前缀识别", async () => {
		await mountWith({ state: "error", error: "gateway_app_mismatch: topic" });
		expect(screen.getByTestId("mobile-push-error")).toHaveAttribute(
			"data-code",
			"gateway_app_mismatch",
		);
		expect(screen.getByTestId("mobile-push-error")).toHaveTextContent(
			/推送网关不服务这个 App/,
		);
	});

	it("active（UnifiedPush）：分发器与端点主机", async () => {
		const card = await mountWith({
			state: "active",
			channel: "unifiedpush",
			distributor: "ntfy",
			endpointHost: "ntfy.sh",
		});
		expect(card).toHaveAttribute("data-push-via", "unifiedpush");
		expect(screen.getByTestId("mobile-push-via")).toHaveTextContent(
			"经 ntfy 接收推送（UnifiedPush · ntfy.sh）",
		);
	});

	it("error：原因 + 重试", async () => {
		await mountWith({ state: "error", error: "SERVICE_NOT_AVAILABLE" });
		expect(
			screen.getByText("推送注册失败：SERVICE_NOT_AVAILABLE"),
		).toBeInTheDocument();
		fireEvent.click(screen.getByTestId("mobile-push-retry"));
		await waitFor(() => expect(fake.enablePush).toHaveBeenCalledTimes(1));
	});

	it("旧壳的 unsupported（默认分支）：一句说明，没有按钮", async () => {
		await mountWith({ state: "unsupported" as unknown as MobilePushState });
		expect(screen.getByText(/这台设备不支持推送/)).toBeInTheDocument();
		expect(screen.queryByRole("button")).toBeNull();
	});

	it("壳推来 mobile 事件后重取状态", async () => {
		await mountWith({ state: "registering" });
		expect(fake.info).toHaveBeenCalledTimes(1);
		info = makeInfo({ state: "active", channel: "fcm" });
		act(() => mobileCb?.());
		await waitFor(() =>
			expect(screen.getByTestId("mobile-push-card")).toHaveAttribute(
				"data-push-state",
				"active",
			),
		);
		expect(fake.info).toHaveBeenCalledTimes(2);
	});
});
