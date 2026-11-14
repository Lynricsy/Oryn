/** 设置 → 通知 → 此设备（移动壳）：七种推送状态的呈现、两种 active 通道、启用 / 关闭、测试通知、壳推来 mobile 后重取。 */
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
import { host, type MobileHost, type MobileInfo } from "../../lib/host";
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
let fake: { [K in keyof MobileHost]: Mock<MobileHost[K]> };
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
		changeServer: vi.fn(),
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

	it("active（FCM）：通道文案、data-push-via、发送测试通知、关闭推送", async () => {
		const notify = vi.spyOn(host, "notify").mockImplementation(() => {});
		const card = await mountWith({ state: "active", channel: "fcm" });
		expect(card).toHaveAttribute("data-push-via", "fcm");
		expect(screen.getByTestId("mobile-push-via")).toHaveTextContent(
			"经 Firebase 云消息（FCM）接收推送",
		);
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

	it("unsupported：一句说明，没有按钮", async () => {
		await mountWith({ state: "unsupported" });
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
