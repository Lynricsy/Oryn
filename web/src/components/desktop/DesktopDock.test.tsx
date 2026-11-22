/**
 * 桌面任务栏（M25）：旧 orbd 不渲染；运行中有圆点；单窗口点击按活动 / 非活动发出 minimize / activate；
 * 多窗口弹菜单（切到前台、关闭、新建窗口）；agent 操作中先确认、接管、再启动；图标加载失败换占位图标；方向键与 Shift+F10。
 */
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
import type { Orb, Schemas } from "../../api/client";
import i18n from "../../i18n";
import { ConfirmHost } from "../ui/ConfirmHost";
import { DesktopDock } from "./DesktopDock";

const ORB_ID = "orb-dock000000000000000000001";

let requests: { method: string; path: string }[];

function app(
	id: string,
	name: string,
	running = false,
): Schemas["OrbDesktopApp"] {
	return { id, name, running, launching: false };
}

function win(
	id: string,
	appId: string | undefined,
	title: string,
	patch: Partial<Schemas["OrbDesktopWindow"]> = {},
): Schemas["OrbDesktopWindow"] {
	return {
		id,
		...(appId ? { appId } : {}),
		title,
		active: false,
		minimized: false,
		...patch,
	};
}

function orbWith(desktop: Partial<Schemas["OrbDesktop"]>): Orb {
	return {
		id: ORB_ID,
		providerId: "prv-local",
		type: "standard",
		cpus: 2,
		memoryMB: 4096,
		image: "oryn/orb",
		state: "ready",
		stateChangedAt: 1,
		createdAt: 1,
		threadIds: [],
		desktop: { displayReady: true, browser: "stopped", ...desktop },
	};
}

function mount(
	orb: Orb,
	{
		controlLocked = false,
		onTakeOver = vi.fn(async () => {}),
		onError = vi.fn(),
	}: {
		controlLocked?: boolean;
		onTakeOver?: () => Promise<void>;
		onError?: (e: unknown) => void;
	} = {},
) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<DesktopDock
				orb={orb}
				controlLocked={controlLocked}
				onTakeOver={onTakeOver}
				onError={onError}
			/>
			<ConfirmHost />
		</QueryClientProvider>,
	);
}

const posts = () =>
	requests.filter((r) => r.method === "POST").map((r) => r.path);
const base = `/api/v1/orbs/${ORB_ID}/desktop`;

beforeEach(() => {
	requests = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const path = new URL(req.url).pathname;
			requests.push({ method: req.method, path });
			return Response.json(orbWith({}));
		}),
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("DesktopDock", () => {
	it("旧 orbd（没有 apps）：不渲染任务栏", () => {
		const { container } = mount(orbWith({}));
		expect(screen.queryByRole("toolbar")).toBeNull();
		expect(container.querySelector("[data-testid=desktop-dock]")).toBeNull();
	});

	it("按顺序列出应用，运行中的图标下有圆点，拥有活动窗口的标为活动", () => {
		mount(
			orbWith({
				apps: [app("chromium", "Chromium", true), app("terminal", "Terminal")],
				windows: [win("11", "chromium", "New Tab", { active: true })],
			}),
		);
		const bar = screen.getByRole("toolbar", {
			name: i18n.t("deskPanel.dock.label"),
		});
		const [chromium, terminal] = within(bar).getAllByRole("button");
		expect(chromium).toHaveAccessibleName(/Chromium/);
		expect(terminal).toHaveAccessibleName("Terminal");
		expect(
			within(chromium as HTMLElement).getByTestId("dock-running-dot"),
		).toBeInTheDocument();
		expect(
			within(terminal as HTMLElement).queryByTestId("dock-running-dot"),
		).toBeNull();
		expect(chromium).toHaveAttribute("data-active", "true");
		expect(terminal).not.toHaveAttribute("data-active");
		expect(screen.getByAltText("Terminal")).toHaveAttribute(
			"src",
			`${base}/apps/terminal/icon`,
		);
	});

	it("没在运行的应用：点击启动", async () => {
		mount(orbWith({ apps: [app("terminal", "Terminal")], windows: [] }));
		await act(async () => {
			fireEvent.click(screen.getByTestId("dock-terminal"));
		});
		await waitFor(() =>
			expect(posts()).toEqual([`${base}/apps/terminal/launch`]),
		);
	});

	it.each([
		["活动且未最小化 → minimize", { active: true }, "minimize"],
		["非活动 → activate", { active: false }, "activate"],
		[
			"活动但已最小化 → activate",
			{ active: true, minimized: true },
			"activate",
		],
	])("单窗口：%s", async (_name, patch, action) => {
		mount(
			orbWith({
				apps: [app("terminal", "Terminal", true)],
				windows: [win("42", "terminal", "~", patch)],
			}),
		);
		await act(async () => {
			fireEvent.click(screen.getByTestId("dock-terminal"));
		});
		await waitFor(() =>
			expect(posts()).toEqual([`${base}/windows/42/${action}`]),
		);
	});

	it("多窗口：点击弹出菜单，点标题切到前台、行尾关闭、末行新建窗口", async () => {
		mount(
			orbWith({
				apps: [app("terminal", "Terminal", true)],
				windows: [
					win("1", "terminal", "repo", { active: true }),
					win("2", "terminal", "bun run dev", { minimized: true }),
				],
			}),
		);
		const icon = screen.getByTestId("dock-terminal");
		fireEvent.click(icon);
		const menu = screen.getByRole("menu");
		expect(icon).toHaveAttribute("aria-expanded", "true");
		expect(posts()).toEqual([]);
		await act(async () => {
			fireEvent.click(
				within(menu).getByRole("menuitem", {
					name: (n) => n.startsWith("bun run dev"),
				}),
			);
		});
		await waitFor(() =>
			expect(posts()).toEqual([`${base}/windows/2/activate`]),
		);
		expect(screen.queryByRole("menu")).toBeNull();

		fireEvent.click(icon);
		await act(async () => {
			fireEvent.click(
				within(screen.getByRole("menu")).getByRole("menuitem", {
					name: i18n.t("deskPanel.dock.closeWindow", { title: "repo" }),
				}),
			);
		});
		fireEvent.click(icon);
		await act(async () => {
			fireEvent.click(
				within(screen.getByRole("menu")).getByRole("menuitem", {
					name: i18n.t("deskPanel.dock.newWindow"),
				}),
			);
		});
		await waitFor(() =>
			expect(posts()).toEqual([
				`${base}/windows/2/activate`,
				`${base}/windows/1/close`,
				`${base}/apps/terminal/launch`,
			]),
		);
	});

	it("右键运行中的图标、以及「其它窗口」：都弹出同一个菜单", () => {
		mount(
			orbWith({
				apps: [app("terminal", "Terminal", true)],
				windows: [win("1", "terminal", "repo"), win("9", undefined, "xclock")],
			}),
		);
		fireEvent.contextMenu(screen.getByTestId("dock-terminal"));
		expect(
			within(screen.getByRole("menu")).getByRole("menuitem", { name: "repo" }),
		).toBeInTheDocument();
		fireEvent.mouseDown(document.body);
		expect(screen.queryByRole("menu")).toBeNull();

		fireEvent.click(screen.getByTestId("dock-other"));
		const menu = screen.getByRole("menu");
		expect(
			within(menu).getByRole("menuitem", { name: "xclock" }),
		).toBeInTheDocument();
		// 「其它窗口」没有可启动的应用，不给「新建窗口」
		expect(
			within(menu).queryByRole("menuitem", {
				name: i18n.t("deskPanel.dock.newWindow"),
			}),
		).toBeNull();
	});

	it("agent 操作中：先确认，接管成功后再启动", async () => {
		const order: string[] = [];
		const onTakeOver = vi.fn(async () => {
			order.push("take");
		});
		vi.mocked(fetch).mockImplementation(async (input) => {
			const path = new URL(
				String(input instanceof Request ? input.url : input),
				location.origin,
			).pathname;
			requests.push({ method: "POST", path });
			order.push(path);
			return Response.json(orbWith({}));
		});
		mount(orbWith({ apps: [app("vscode", "VS Code")], windows: [] }), {
			controlLocked: true,
			onTakeOver,
		});
		await act(async () => {
			fireEvent.click(screen.getByTestId("dock-vscode"));
		});
		const dialog = await screen.findByRole("dialog", {
			name: i18n.t("deskPanel.dock.takeConfirm"),
		});
		expect(onTakeOver).not.toHaveBeenCalled();
		expect(posts()).toEqual([]);
		await act(async () => {
			fireEvent.click(
				within(dialog).getByRole("button", {
					name: i18n.t("deskPanel.control.take"),
				}),
			);
		});
		await waitFor(() =>
			expect(order).toEqual(["take", `${base}/apps/vscode/launch`]),
		);
	});

	it("agent 操作中取消确认：不接管、不发请求", async () => {
		const onTakeOver = vi.fn(async () => {});
		mount(orbWith({ apps: [app("vscode", "VS Code")], windows: [] }), {
			controlLocked: true,
			onTakeOver,
		});
		await act(async () => {
			fireEvent.click(screen.getByTestId("dock-vscode"));
		});
		const dialog = await screen.findByRole("dialog");
		await act(async () => {
			fireEvent.click(
				within(dialog).getByRole("button", { name: i18n.t("common.cancel") }),
			);
		});
		expect(onTakeOver).not.toHaveBeenCalled();
		expect(posts()).toEqual([]);
	});

	it("图标加载失败：换成占位图标", () => {
		mount(orbWith({ apps: [app("files", "Files")], windows: [] }));
		const icon = screen.getByTestId("dock-files");
		expect(within(icon).queryByTestId("dock-icon-fallback")).toBeNull();
		fireEvent.error(screen.getByAltText("Files"));
		expect(screen.queryByAltText("Files")).toBeNull();
		expect(within(icon).getByTestId("dock-icon-fallback")).toBeInTheDocument();
	});

	it("键盘：toolbar 里只有一个格子可 Tab，方向键移动焦点，Shift+F10 打开菜单", () => {
		mount(
			orbWith({
				apps: [app("chromium", "Chromium"), app("terminal", "Terminal", true)],
				windows: [win("1", "terminal", "repo")],
			}),
		);
		const chromium = screen.getByTestId("dock-chromium");
		const terminal = screen.getByTestId("dock-terminal");
		expect(chromium).toHaveAttribute("tabindex", "0");
		expect(terminal).toHaveAttribute("tabindex", "-1");
		chromium.focus();
		fireEvent.keyDown(chromium, { key: "ArrowRight" });
		expect(terminal).toHaveFocus();
		expect(terminal).toHaveAttribute("tabindex", "0");
		fireEvent.keyDown(terminal, { key: "F10", shiftKey: true });
		expect(screen.getByRole("menu")).toBeInTheDocument();
		expect(
			within(screen.getByRole("menu")).getAllByRole("menuitem")[0],
		).toHaveFocus();
	});
});
