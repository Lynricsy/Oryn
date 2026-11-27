/**
 * 设计画布原型预览与设备外壳（M29 R1）：
 * - `DeviceShell` 四种形态（none 透传 / phone / tablet / browser）与 390 宽手机屏尺寸、不裁剪内容；
 * - `Prototype` 起始帧选取（design.start → 第一个带 source 的画框 → 第一个画框）；
 * - 收到 `navigated {frame}` 与 `ready.design` 后同步标题、尺寸与设备外壳；
 * - 手动切换帧与重置发送 `{kind:"navigate", frame}`（含 live 模式的 portal targetOrigin）；
 * - 设备外壳临时覆盖不修改文档、Esc 关闭并截断传播；
 * - `PropsPanel` 切换 `device` 发出 set op；`Toolbar` 设计画布原型按钮与连线开关。
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PROTOCOL_VERSION } from "../../../../sandbox/runtime/messages";
import i18n from "../../../i18n";
import {
	CANVAS_FORMAT_DESIGN,
	type CanvasDoc,
	type CanvasOp,
	type CanvasPage,
	parseCanvasDoc,
} from "../../../lib/artifacts/canvas-doc";
import { pickOption } from "../../../test/select";
import { flowBezier, parseFramesLinks } from "./CanvasEditor";
import { DeviceShell, deviceShellSize } from "./DeviceShell";
import type { PageContent } from "./frames";
import { PropsPanel } from "./PropsPanel";
import { Prototype, resolveStartPage } from "./Prototype";
import { DEFAULT_STYLES, Toolbar } from "./Toolbar";

function makeDesignDoc(
	pages: CanvasPage[],
	start: string | null = null,
): CanvasDoc {
	return {
		format: CANVAS_FORMAT_DESIGN,
		design: { engine: 1, start },
		canvases: [{ id: "c1", name: "页面 1" }],
		pages,
		notes: [],
	};
}

const LOGIN: CanvasPage = {
	id: "p-login",
	canvas: "c1",
	name: "登录",
	file: "dist/login.html",
	source: "src/frames/login.tsx",
	x: 0,
	y: 0,
	width: 390,
	height: 844,
	locked: false,
	device: "phone",
};

const DASHBOARD: CanvasPage = {
	id: "p-dash",
	canvas: "c1",
	name: "控制台",
	file: "dist/dashboard.html",
	source: "src/frames/dashboard.tsx",
	x: 500,
	y: 0,
	width: 1280,
	height: 800,
	locked: false,
	device: "browser",
};

const PLAIN_HTML: CanvasPage = {
	id: "p-plain",
	canvas: "c1",
	name: "说明页",
	file: "plain.html",
	x: -500,
	y: 0,
	width: 800,
	height: 600,
	locked: false,
};

/** 给 iframe 装上可控的 `contentWindow`，返回 postMessage spy 与触发 load 拿到 channel 的助手。 */
function attachMockWindow(iframe: HTMLIFrameElement) {
	const postMessage = vi.fn();
	const fakeWin = { postMessage } as unknown as Window;
	Object.defineProperty(iframe, "contentWindow", {
		configurable: true,
		value: fakeWin,
	});
	fireEvent.load(iframe);
	const hello = postMessage.mock.calls
		.map(([msg]) => msg)
		.find(
			(msg): msg is { kind: "hello"; channel: string } =>
				typeof msg === "object" &&
				msg !== null &&
				"kind" in msg &&
				msg.kind === "hello" &&
				"channel" in msg &&
				typeof msg.channel === "string",
		);
	if (!hello) throw new Error("帧 load 后未发出 hello");
	return {
		postMessage,
		fakeWin,
		channel: hello.channel,
		sendFromFrame(body: Record<string, unknown>) {
			act(() => {
				window.dispatchEvent(
					new MessageEvent("message", {
						source: fakeWin,
						data: { v: PROTOCOL_VERSION, channel: hello.channel, ...body },
					}),
				);
			});
		},
	};
}

describe("DeviceShell", () => {
	it("none 直接透传 children，不加外壳节点", () => {
		const { container } = render(
			<DeviceShell device="none">
				<div data-testid="inner">内容</div>
			</DeviceShell>,
		);
		expect(screen.queryByTestId("device-shell")).toBeNull();
		expect(container.firstElementChild).toBe(screen.getByTestId("inner"));
	});

	it("phone 外壳带状态条/听筒与底栏指示，390 宽保留完整内容区尺寸", () => {
		render(
			<DeviceShell device="phone" width={390} height={844}>
				<div data-testid="inner">手机帧</div>
			</DeviceShell>,
		);
		const shell = screen.getByTestId("device-shell");
		expect(shell.getAttribute("data-device")).toBe("phone");
		expect(screen.getByTestId("device-shell-status")).toBeInTheDocument();
		expect(screen.getByTestId("device-shell-home")).toBeInTheDocument();
		const screenBox = screen.getByTestId("device-shell-screen");
		expect(screenBox.style.width).toBe("390px");
		expect(screenBox.style.height).toBe("844px");
		const total = deviceShellSize("phone", 390, 844);
		expect(total.width).toBeGreaterThan(390);
		expect(total.height).toBeGreaterThan(844);
	});

	it("tablet 外壳带摄像头点与内容区", () => {
		render(
			<DeviceShell device="tablet" width={768} height={1024}>
				<div>平板帧</div>
			</DeviceShell>,
		);
		const shell = screen.getByTestId("device-shell");
		expect(shell.getAttribute("data-device")).toBe("tablet");
		expect(screen.getByTestId("device-shell-camera")).toBeInTheDocument();
		expect(screen.getByTestId("device-shell-screen").style.width).toBe("768px");
	});

	it("browser 外壳带标题栏圆点与地址条（显示帧名）", () => {
		render(
			<DeviceShell device="browser" title="控制台" width={1280} height={800}>
				<div>桌面帧</div>
			</DeviceShell>,
		);
		const shell = screen.getByTestId("device-shell");
		expect(shell.getAttribute("data-device")).toBe("browser");
		expect(screen.getByTestId("device-shell-chrome")).toBeInTheDocument();
		expect(screen.getByTestId("device-shell-address").textContent).toBe(
			"控制台",
		);
	});
});

describe("Prototype", () => {
	beforeEach(() => {
		i18n.changeLanguage("zh-CN");
	});

	it("起始帧选取：优先 design.start，缺省第一个带 source 的画框，再缺省第一个画框", () => {
		const withStart = makeDesignDoc([PLAIN_HTML, LOGIN, DASHBOARD], "p-dash");
		expect(resolveStartPage(withStart)?.id).toBe("p-dash");

		const noStart = makeDesignDoc([PLAIN_HTML, LOGIN, DASHBOARD], null);
		expect(resolveStartPage(noStart)?.id).toBe("p-login");

		const plainOnly = makeDesignDoc([PLAIN_HTML], null);
		expect(resolveStartPage(plainOnly)?.id).toBe("p-plain");
	});

	it("收到 navigated {frame} 后同步标题与设备外壳，不卸载 iframe；ready.design 同样同步", () => {
		const doc = makeDesignDoc([LOGIN, DASHBOARD], "p-login");
		const contentOf = (p: CanvasPage): PageContent => ({
			kind: "frame",
			src: `https://frame.test/_oryn/v/1/${p.file}`,
		});
		render(
			<Prototype
				title="登录原型"
				doc={doc}
				contentOf={contentOf}
				onClose={vi.fn()}
			/>,
		);

		expect(screen.getByTestId("prototype-title").textContent).toBe("登录");
		expect(screen.getByTestId("device-shell").getAttribute("data-device")).toBe(
			"phone",
		);

		const iframe = screen.getByTestId("prototype-frame") as HTMLIFrameElement;
		const mock = attachMockWindow(iframe);

		// 帧内点击 <Link to="dashboard"> 后运行时回报 navigated
		mock.sendFromFrame({ kind: "navigated", frame: "dashboard" });
		expect(screen.getByTestId("prototype-title").textContent).toBe("控制台");
		expect(screen.getByTestId("device-shell").getAttribute("data-device")).toBe(
			"browser",
		);
		expect(screen.getByTestId("device-shell-address").textContent).toBe(
			"控制台",
		);
		// 同为有外壳形态时保留同一个 iframe 实例，不二次挂载
		expect(screen.getByTestId("prototype-frame")).toBe(iframe);

		// 通过 ready.design 同步回 login
		mock.sendFromFrame({
			kind: "ready",
			params: {},
			design: { frame: "login", links: ["dashboard"] },
		});
		expect(screen.getByTestId("prototype-title").textContent).toBe("登录");
		expect(screen.getByTestId("device-shell").getAttribute("data-device")).toBe(
			"phone",
		);
	});

	it("落选方案的画框（M34 C6）：不做起始帧、不进下拉；帧内跳到落选帧时跳回当前帧", () => {
		const doc: CanvasDoc = {
			...makeDesignDoc([LOGIN, DASHBOARD, PLAIN_HTML], "p-dash"),
			groups: [
				{
					id: "entry",
					canvas: "c1",
					name: "入口",
					variants: [
						{ id: "a", name: "登录", note: "", theme: "", pages: ["p-login"] },
						{ id: "b", name: "控制台", note: "", theme: "", pages: ["p-dash"] },
					],
					picked: "a",
					pickedBy: null,
				},
			],
		};
		expect(resolveStartPage(doc)?.id).toBe("p-login");
		const contentOf = (p: CanvasPage): PageContent => ({
			kind: "frame",
			src: `https://frame.test/_oryn/v/1/${p.file}`,
		});
		render(
			<Prototype
				title="原型"
				doc={doc}
				contentOf={contentOf}
				onClose={vi.fn()}
			/>,
		);
		fireEvent.click(screen.getByRole("combobox", { name: "切换画框" }));
		expect(
			screen.getAllByRole("option").map((o) => o.textContent?.trim()),
		).toEqual(["登录", "说明页"]);

		const mock = attachMockWindow(
			screen.getByTestId("prototype-frame") as HTMLIFrameElement,
		);
		mock.postMessage.mockClear();
		mock.sendFromFrame({ kind: "navigated", frame: "dashboard" });
		expect(screen.getByTestId("prototype-title").textContent).toBe("登录");
		expect(mock.postMessage).toHaveBeenCalledWith(
			expect.objectContaining({ kind: "navigate", frame: "login" }),
			"*",
		);
		// 指向不存在的帧：同样停在原处，但不发跳回
		mock.postMessage.mockClear();
		mock.sendFromFrame({ kind: "navigated", frame: "missing" });
		expect(mock.postMessage).not.toHaveBeenCalled();
	});

	it("手动切换帧与重置起始帧向 iframe 发送 navigate；支持 live 模式的 portal origin", () => {
		const doc = makeDesignDoc([LOGIN, DASHBOARD], "p-login");
		const portalOrigin = "https://t-abc-p5179.orb.example";
		const contentOf = (p: CanvasPage): PageContent => ({
			kind: "live",
			src: `${portalOrigin}/frames/${p.id === "p-login" ? "login" : "dashboard"}`,
			origin: portalOrigin,
			nonce: 0,
		});
		render(
			<Prototype
				title="实时原型"
				doc={doc}
				contentOf={contentOf}
				onClose={vi.fn()}
			/>,
		);

		const iframe = screen.getByTestId("canvas-live-frame") as HTMLIFrameElement;
		const mock = attachMockWindow(iframe);
		mock.postMessage.mockClear();

		// 顶栏下拉切到控制台
		pickOption(screen.getByRole("combobox", { name: "切换画框" }), "控制台");
		expect(mock.postMessage).toHaveBeenCalledWith(
			expect.objectContaining({
				v: PROTOCOL_VERSION,
				channel: mock.channel,
				kind: "navigate",
				frame: "dashboard",
			}),
			portalOrigin,
		);
		expect(screen.getByTestId("prototype-title").textContent).toBe("控制台");

		// 点击重置回到起始帧
		mock.postMessage.mockClear();
		fireEvent.click(screen.getByTestId("prototype-reset"));
		expect(mock.postMessage).toHaveBeenCalledWith(
			expect.objectContaining({
				kind: "navigate",
				frame: "login",
			}),
			portalOrigin,
		);
		expect(screen.getByTestId("prototype-title").textContent).toBe("登录");
	});

	it("设备外壳下拉仅作预览临时覆盖，不修改文档；Esc 与关闭钮退出原型", () => {
		const doc = makeDesignDoc([LOGIN, DASHBOARD], "p-login");
		const onClose = vi.fn();
		render(
			<Prototype
				title="登录原型"
				doc={doc}
				contentOf={(p) => ({ kind: "frame", src: `https://f.test/${p.file}` })}
				onClose={onClose}
			/>,
		);

		const device = screen.getByRole("combobox", { name: "预览设备外壳" });
		pickOption(device, "平板");
		expect(screen.getByTestId("device-shell").getAttribute("data-device")).toBe(
			"tablet",
		);
		// 原文档未被改动
		expect(doc.pages[0]?.device).toBe("phone");

		// 切回跟随画框
		pickOption(device, "跟随画框");
		expect(screen.getByTestId("device-shell").getAttribute("data-device")).toBe(
			"phone",
		);

		// Esc 截断并回调 onClose
		const below = vi.fn();
		window.addEventListener("keydown", below);
		act(() => {
			fireEvent.keyDown(document.body, { key: "Escape" });
		});
		window.removeEventListener("keydown", below);
		expect(onClose).toHaveBeenCalledTimes(1);
		expect(below).not.toHaveBeenCalled();

		fireEvent.click(screen.getByTestId("prototype-close"));
		expect(onClose).toHaveBeenCalledTimes(2);
	});

	it("从无外壳（none）帧跳转到有外壳帧时同步更新 iframe 源与外壳，不退回起始帧", () => {
		const bare: CanvasPage = {
			...LOGIN,
			device: undefined,
		};
		const doc = makeDesignDoc([bare, DASHBOARD], "p-login");
		render(
			<Prototype
				title="切壳原型"
				doc={doc}
				contentOf={(p) => ({
					kind: "frame",
					src: `https://frame.test/_oryn/v/1/${p.file}`,
				})}
				onClose={vi.fn()}
			/>,
		);
		expect(screen.queryByTestId("device-shell")).toBeNull();
		const iframe = screen.getByTestId("prototype-frame") as HTMLIFrameElement;
		const mock = attachMockWindow(iframe);
		mock.sendFromFrame({ kind: "navigated", frame: "dashboard" });
		expect(screen.getByTestId("prototype-title").textContent).toBe("控制台");
		expect(screen.getByTestId("device-shell").getAttribute("data-device")).toBe(
			"browser",
		);
		expect(
			(screen.getByTestId("prototype-frame") as HTMLIFrameElement).src,
		).toBe("https://frame.test/_oryn/v/1/dist/dashboard.html");
	});
});

describe("PropsPanel 设备外壳选择与 Toolbar 原型按钮", () => {
	it("单选画框切换 device 发出 set op（含写 none）", () => {
		const doc = parseCanvasDoc(
			JSON.stringify({
				format: 2,
				design: { engine: 1, start: "p-login" },
				canvases: [{ id: "c1", name: "首页" }],
				pages: [LOGIN],
				notes: [],
			}),
		);
		const commit = vi.fn((_ops: CanvasOp[]) => true);
		render(
			<PropsPanel
				doc={doc}
				selected={["p-login"]}
				threads={[]}
				commit={commit}
				onSelect={vi.fn()}
				overlay={false}
			/>,
		);
		const select = screen.getByRole("combobox", { name: /^设备外壳/ });
		expect(select).toHaveTextContent("手机");

		pickOption(select, "浏览器");
		expect(commit).toHaveBeenCalledWith([
			{ op: "set", id: "p-login", key: "device", value: "browser" },
		]);

		pickOption(select, "无");
		expect(commit).toHaveBeenLastCalledWith([
			{ op: "set", id: "p-login", key: "device", value: "none" },
		]);
	});

	it("Toolbar 传入 onPrototype 与 flowLinks 时显示对应按钮并触发回调", () => {
		const doc = makeDesignDoc([LOGIN, DASHBOARD], "p-login");
		const onPrototype = vi.fn();
		const onToggle = vi.fn();
		render(
			<div className="canvas-editor relative">
				<Toolbar
					doc={doc}
					canvasKey="c1"
					readOnly={false}
					commentable={false}
					tool="move"
					penKind="pen"
					shape="rect"
					styles={DEFAULT_STYLES}
					onTool={vi.fn()}
					onPenKind={vi.fn()}
					onShape={vi.fn()}
					onStyle={vi.fn()}
					onCanvas={vi.fn()}
					commit={() => true}
					zoom={1}
					hasSelection={false}
					onZoom={vi.fn()}
					onPrototype={onPrototype}
					flowLinks={{ open: true, onToggle }}
				/>
			</div>,
		);
		fireEvent.click(screen.getByTestId("canvas-prototype-open"));
		expect(onPrototype).toHaveBeenCalledTimes(1);
		const flowBtn = screen.getByTestId("canvas-flow-links-toggle");
		expect(flowBtn).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(flowBtn);
		expect(onToggle).toHaveBeenCalledTimes(1);
	});
});

describe("画布帧间连线解析与贝塞尔路径", () => {
	it("解析 dist/frames.json 并在坏数据时安全降级", () => {
		const raw = new TextEncoder().encode(
			JSON.stringify([
				{ id: "login", name: "登录", links: ["dashboard", "signup"] },
				{ id: "dashboard", name: "控制台", links: [] },
			]),
		);
		expect(parseFramesLinks(raw)).toEqual({
			login: ["dashboard", "signup"],
		});
		expect(parseFramesLinks(new TextEncoder().encode("{bad"))).toEqual({});
	});

	it("flowBezier 从源画框右边中点连到目标画框左边中点并生成箭头", () => {
		const geom = flowBezier(LOGIN, DASHBOARD, 1);
		expect(geom.x1).toBe(LOGIN.x + LOGIN.width);
		expect(geom.y1).toBe(LOGIN.y + LOGIN.height / 2);
		expect(geom.line).toMatch(/^M390 422 C.+ 500 400$/);
		expect(geom.arrow).toContain("L500 400L");
	});
});
