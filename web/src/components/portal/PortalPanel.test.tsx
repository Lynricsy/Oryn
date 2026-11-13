/**
 * Portal 面板：iframe 的地址与沙箱属性、URL 胶囊、工具条历史（后退 / 前进 / 刷新）、链接菜单、
 * 复制与新窗口、外部改端口时历史重置，以及没有模板 / orb 时的空态。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Schemas, Thread } from "../../api/client";
import i18n from "../../i18n";
import { getThreadStore, indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import PortalPanel, { PORTAL_SANDBOX } from "./PortalPanel";

const TEMPLATE = "https://t-3f9a1c0b7e2d-p{port}.orb.example.com/";
const HOST = "https://t-3f9a1c0b7e2d-p5173.orb.example.com";
let seq = 0;

function web(links?: Schemas["OrbPortalLink"][]): Schemas["OrbPortal"] {
	return {
		port: 5173,
		service: "web",
		links: links ?? [
			{ title: "web", path: "/" },
			{ title: "Docs", path: "/docs" },
		],
	};
}

/** 建一个线程 + orb（每个用例独立的 id），返回线程 id。 */
function setup(
	portals: Schemas["OrbPortal"][],
	opts: { template?: string | null; state?: Orb["state"] } = {},
): { threadId: string; orbId: string } {
	seq++;
	const threadId = `T-portal-${seq}`;
	const orbId = `orb-portal${String(seq).padStart(17, "0")}`;
	const template = opts.template === undefined ? TEMPLATE : opts.template;
	const orb = {
		id: orbId,
		state: opts.state ?? "ready",
		stateChangedAt: 1,
		threadIds: [threadId],
		portals,
	} as Orb;
	getThreadStore(threadId)
		.getState()
		.patchLocal({
			thread: {
				id: threadId,
				orbId,
				...(template ? { portalUrlTemplate: template } : {}),
			} as Thread,
			orb,
		});
	indexStore.getState().upsertOrb(orb);
	return { threadId, orbId };
}

function mount(threadId: string) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<PortalPanel threadId={threadId} />
		</QueryClientProvider>,
	);
}

const frame = () => screen.getByTestId("portal-frame");
const src = () => frame().getAttribute("src");
const nextOf = (path: string) =>
	`${HOST}/__oryn/login?next=${encodeURIComponent(path)}`;
const button = (name: string) => screen.getByRole("button", { name });

beforeEach(() => {
	uiStore.setState({ portalPort: {} });
});

afterEach(() => {
	vi.restoreAllMocks();
});

describe("PortalPanel", () => {
	it("iframe 经 /__oryn/login 打开首个链接；沙箱不含 allow-top-navigation；胶囊显示主机名与路径", () => {
		const { threadId } = setup([web()]);
		mount(threadId);
		expect(src()).toBe(nextOf("/"));
		const sandbox = frame().getAttribute("sandbox") ?? "";
		expect(sandbox).toBe(PORTAL_SANDBOX);
		expect(sandbox.split(" ")).toEqual(
			expect.arrayContaining([
				"allow-scripts",
				"allow-same-origin",
				"allow-forms",
				"allow-popups",
			]),
		);
		expect(sandbox).not.toContain("allow-top-navigation");
		expect(frame()).toHaveAttribute("referrerpolicy", "no-referrer");
		// 不委派剪贴板读取：权限按顶层源（Oryn 主域名）记，授权一次后任何 portal 应用都能静默读剪贴板
		expect(frame()).toHaveAttribute("allow", "clipboard-write; fullscreen");
		expect(frame().getAttribute("allow")).not.toContain("clipboard-read");
		expect(frame()).toHaveAttribute("title", "Portal：web");
		expect(screen.getByTestId("portal-host")).toHaveTextContent(
			"t-3f9a1c0b7e2d-p5173.orb.example.com",
		);
		expect(screen.getByRole("textbox", { name: "路径" })).toHaveValue("/");
		// 未公开：锁
		expect(screen.getByTestId("portal-private")).toBeInTheDocument();
		expect(screen.queryByTestId("portal-public")).toBeNull();
	});

	it("工具条历史：回车入栈，后退 / 前进在栈内移动，刷新以同一地址重建 iframe", () => {
		const { threadId } = setup([web()]);
		mount(threadId);
		const back = button(i18n.t("portal.back"));
		const forward = button(i18n.t("portal.forward"));
		expect(back).toBeDisabled();
		expect(forward).toBeDisabled();

		const input = screen.getByRole("textbox", { name: "路径" });
		fireEvent.change(input, { target: { value: "about?tab=2" } });
		fireEvent.submit(input);
		expect(src()).toBe(nextOf("/about?tab=2"));
		expect(input).toHaveValue("/about?tab=2");
		expect(back).toBeEnabled();

		fireEvent.click(back);
		expect(src()).toBe(nextOf("/"));
		expect(forward).toBeEnabled();
		fireEvent.click(forward);
		expect(src()).toBe(nextOf("/about?tab=2"));

		// 后退后再导航：前进分支被截断
		fireEvent.click(back);
		fireEvent.change(input, { target: { value: "/x" } });
		fireEvent.submit(input);
		expect(forward).toBeDisabled();
		fireEvent.click(back);
		expect(src()).toBe(nextOf("/"));

		const before = frame();
		fireEvent.click(button(i18n.t("portal.reload")));
		expect(frame()).not.toBe(before);
		expect(src()).toBe(nextOf("/"));
	});

	it("编辑路径后 Esc 或失焦还原，不导航", () => {
		const { threadId } = setup([web()]);
		mount(threadId);
		const input = screen.getByRole("textbox", { name: "路径" });
		fireEvent.change(input, { target: { value: "/draft" } });
		fireEvent.keyDown(input, { key: "Escape" });
		expect(input).toHaveValue("/");
		fireEvent.change(input, { target: { value: "/draft" } });
		fireEvent.blur(input);
		expect(input).toHaveValue("/");
		expect(src()).toBe(nextOf("/"));
	});

	it("链接菜单：同端口切路径；另一个端口的链接切换端口并记入 portalPort", () => {
		const { threadId } = setup([
			web(),
			{
				port: 8080,
				service: "api",
				links: [{ title: "Swagger", path: "/swagger", folder: "API" }],
			},
		]);
		mount(threadId);
		fireEvent.click(button(i18n.t("portal.links")));
		fireEvent.click(screen.getByRole("menuitem", { name: /Docs/ }));
		expect(src()).toBe(nextOf("/docs"));
		expect(frame()).toHaveAttribute("title", "Portal：Docs");

		// folder 是子菜单
		fireEvent.click(button(i18n.t("portal.links")));
		fireEvent.click(screen.getByRole("menuitem", { name: /API/ }));
		fireEvent.click(screen.getByRole("menuitem", { name: /Swagger/ }));
		expect(src()).toBe(
			"https://t-3f9a1c0b7e2d-p8080.orb.example.com/__oryn/login?next=%2Fswagger",
		);
		expect(uiStore.getState().portalPort[threadId]).toBe(8080);
		expect(screen.getByTestId("portal-host")).toHaveTextContent(
			"t-3f9a1c0b7e2d-p8080.orb.example.com",
		);

		// 后退跨端口：端口跟着回去
		fireEvent.click(button(i18n.t("portal.back")));
		expect(src()).toBe(nextOf("/docs"));
		expect(uiStore.getState().portalPort[threadId]).toBe(5173);
	});

	it("只有一个链接时不显示链接菜单", () => {
		const { threadId } = setup([web([{ title: "web", path: "/" }])]);
		mount(threadId);
		expect(
			screen.queryByRole("button", { name: i18n.t("portal.links") }),
		).toBeNull();
	});

	it("外部改端口（信息面板的「打开」）：历史从该 portal 的首个链接重新开始", () => {
		const { threadId } = setup([
			web(),
			{ port: 8080, service: "api", links: [{ title: "api", path: "/v1" }] },
		]);
		mount(threadId);
		const input = screen.getByRole("textbox", { name: "路径" });
		fireEvent.change(input, { target: { value: "/a" } });
		fireEvent.submit(input);
		act(() => uiStore.getState().setPortalPort(threadId, 8080));
		expect(src()).toBe(
			"https://t-3f9a1c0b7e2d-p8080.orb.example.com/__oryn/login?next=%2Fv1",
		);
		expect(button(i18n.t("portal.back"))).toBeDisabled();
	});

	it("复制不带 /__oryn/login 的公开地址；新窗口打开 iframe 同一地址", async () => {
		const writeText = vi.fn(async () => {});
		Object.defineProperty(navigator, "clipboard", {
			configurable: true,
			value: { writeText },
		});
		const open = vi.spyOn(window, "open").mockImplementation(() => null);
		const { threadId } = setup([web()]);
		mount(threadId);
		const input = screen.getByRole("textbox", { name: "路径" });
		fireEvent.change(input, { target: { value: "/docs" } });
		fireEvent.submit(input);
		await act(async () => {
			fireEvent.click(button(i18n.t("portal.copyUrl")));
		});
		expect(writeText).toHaveBeenCalledWith(`${HOST}/docs`);
		fireEvent.click(button(i18n.t("portal.openNew")));
		expect(open).toHaveBeenCalledWith(nextOf("/docs"), "_blank", "noopener");
	});

	it("公开中：胶囊显示地球与剩余时间；已过期按未公开显示", () => {
		const now = Date.now();
		const { threadId } = setup([
			{ ...web(), public: { expiresAt: now + 2 * 3_600_000 + 60_000 } },
		]);
		mount(threadId);
		expect(screen.getByTestId("portal-public")).toHaveTextContent("2");
		const expired = setup([{ ...web(), public: { expiresAt: now - 1 } }]);
		mount(expired.threadId);
		expect(screen.getByTestId("portal-private")).toBeInTheDocument();
	});

	it("控制面没配 portal_domain（无模板）与 orb 已删除时显示空态", () => {
		const off = setup([web()], { template: null });
		const { unmount } = mount(off.threadId);
		expect(
			screen.getByText(i18n.t("portal.disabledTitle")),
		).toBeInTheDocument();
		expect(screen.queryByTestId("portal-frame")).toBeNull();
		unmount();
		const gone = setup([web()], { state: "deleted" });
		mount(gone.threadId);
		expect(screen.getByText(i18n.t("portal.orbGone"))).toBeInTheDocument();
	});
});
