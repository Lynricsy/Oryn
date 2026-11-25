/**
 * 设计画布实时预览（M29 T6）：live 帧上限（6 帧、只在最新版本）、徽标切换与本机偏好、WebKit 5 s 握手超时退回静态并
 * 提示新窗口授权、FrameLink 的 portal targetOrigin、loc 进锚点与提示、带 source 的画框不可删/不可复制。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PageSelection } from "../../../../sandbox/runtime/messages";
import type * as ArtifactQueries from "../../../api/artifact-queries";
import type { ArtifactDesign } from "../../../api/artifact-queries";
import i18n from "../../../i18n";
import {
	CANVAS_FORMAT_DESIGN,
	type CanvasDoc,
	type CanvasPage,
} from "../../../lib/artifacts/canvas-doc";
import { handoffPrompt, selectionPrompt } from "../../../lib/artifacts/prompt";
import { type IndexState, indexStore } from "../../../state/stores";
import { anchorOf } from "../anchors";
import { FrameLink } from "../frame-link";
import { DesignBar } from "./DesignBar";
import {
	LIVE_READY_MS,
	LiveFrame,
	liveFrameTarget,
	MAX_LIVE,
} from "./LiveFrame";
import { deleteOps, duplicateOps } from "./selection";
import {
	type DesignPreview,
	PREVIEW_PREF_KEY,
	useDesignPreview,
} from "./use-design-preview";

const mocks = vi.hoisted(() => ({
	design: {
		design: true,
		autoNotes: true,
		direction: null,
		live: { threadId: "T-live", port: 5179 },
	} as ArtifactDesign | undefined,
}));

vi.mock("../../../api/artifact-queries", async (orig) => {
	const actual = await orig<typeof ArtifactQueries>();
	return {
		...actual,
		useArtifactDesign: () => ({ data: mocks.design }),
	};
});

type IndexThread = IndexState["threads"][string];
const TEMPLATE = "https://t-abc-p{port}.orb.example";

function makePage(i: number, source?: string): CanvasPage {
	return {
		id: `p${i}`,
		canvas: "c1",
		name: `页 ${i}`,
		file: source ? `dist/f${i}.html` : `p${i}.html`,
		...(source ? { source } : {}),
		x: i * 500,
		y: 0,
		width: 400,
		height: 300,
		locked: false,
	};
}

function Harness({
	pages,
	pinned = false,
	design = true,
	onPreview,
}: {
	pages: CanvasPage[];
	pinned?: boolean;
	design?: boolean;
	onPreview: (p: DesignPreview) => void;
}) {
	const p = useDesignPreview({
		artifactId: "art_01JAAAAAAAAAAAAAAAAAAAAAAA",
		design,
		pinned,
		pages,
		view: { x: 0, y: 0, width: 800, height: 600 },
	});
	onPreview(p);
	return <DesignBar preview={p} />;
}

function wrap(ui: React.ReactNode) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

describe("设计画布实时预览（T6）", () => {
	beforeEach(() => {
		localStorage.clear();
		mocks.design = {
			design: true,
			autoNotes: true,
			direction: null,
			live: { threadId: "T-live", port: 5179 },
		};
		// 仅提供 usePortalTemplate 读取的最小线程字段
		const thread = {
			id: "T-live",
			title: "设计",
			portalUrlTemplate: TEMPLATE,
		} as unknown as IndexThread;
		indexStore.setState({ threads: { "T-live": thread } });
	});

	afterEach(() => {
		vi.useRealTimers();
		vi.restoreAllMocks();
	});

	it("live 帧至多 6 个、只取带 source 的画框，钉旧版全部退回静态", () => {
		const pages = [
			...Array.from({ length: 8 }, (_, i) =>
				makePage(i, `src/frames/f${i}.tsx`),
			),
			makePage(99), // 普通 HTML 画框，没有 source
		];
		let latest!: DesignPreview;
		const { rerender } = render(
			wrap(<Harness pages={pages} onPreview={(p) => (latest = p)} />),
		);
		expect(latest.mode).toBe("live");
		expect(latest.liveIds.size).toBe(MAX_LIVE);
		// 视口在 x=[0,800]，最近的 6 个是 p0..p5；p6、p7、普通 HTML 页 p99 都不走 live
		for (let i = 0; i < 6; i++) {
			const page = pages[i];
			expect(page && latest.liveContent(page)?.kind).toBe("live");
		}
		const far = pages[6];
		const plain = pages[8];
		expect(far && latest.liveContent(far)).toBeNull();
		expect(plain && latest.liveContent(plain)).toBeNull();

		// 钉旧版 → 退回静态，liveIds 为空
		rerender(
			wrap(<Harness pages={pages} pinned onPreview={(p) => (latest = p)} />),
		);
		expect(latest.mode).toBe("static");
		expect(latest.liveIds.size).toBe(0);
		const p0 = pages[0];
		expect(p0 && latest.liveContent(p0)).toBeNull();
	});

	it("徽标切换 live/static 并存本地偏好；实时不可用时无切换钮", () => {
		const pages = [makePage(0, "src/frames/f0.tsx")];
		let latest!: DesignPreview;
		const { rerender } = render(
			wrap(<Harness pages={pages} onPreview={(p) => (latest = p)} />),
		);
		expect(
			screen.getByTestId("canvas-design-mode").getAttribute("data-mode"),
		).toBe("live");
		fireEvent.click(screen.getByTestId("canvas-design-toggle"));
		expect(
			screen.getByTestId("canvas-design-mode").getAttribute("data-mode"),
		).toBe("static");
		expect(localStorage.getItem(PREVIEW_PREF_KEY)).toBe("static");
		expect(latest.liveIds.size).toBe(0);

		fireEvent.click(screen.getByTestId("canvas-design-toggle"));
		expect(
			screen.getByTestId("canvas-design-mode").getAttribute("data-mode"),
		).toBe("live");
		expect(localStorage.getItem(PREVIEW_PREF_KEY)).toBeNull();

		// dev 服务不在跑：徽标为静态、没有切换钮
		mocks.design = {
			design: true,
			autoNotes: true,
			direction: null,
			live: null,
		};
		rerender(wrap(<Harness pages={pages} onPreview={(p) => (latest = p)} />));
		expect(screen.queryByTestId("canvas-design-toggle")).toBeNull();
	});

	it("WebKit 兜底：LiveFrame 5 s 无 ready 回调 onTimeout，退回静态并可新窗口授权回焦点重建", () => {
		vi.useFakeTimers();
		const onTimeout = vi.fn();
		const target = liveFrameTarget(TEMPLATE, 5179, "login");
		expect(target.origin).toBe("https://t-abc-p5179.orb.example");
		render(
			<LiveFrame
				pageId="p0"
				src={target.src}
				origin={target.origin}
				title="登录"
				mode={null}
				accent="#0f766e"
				pins={[]}
				onLink={() => undefined}
				onTimeout={onTimeout}
			/>,
		);
		act(() => vi.advanceTimersByTime(LIVE_READY_MS - 100));
		expect(onTimeout).not.toHaveBeenCalled();
		act(() => vi.advanceTimersByTime(150));
		expect(onTimeout).toHaveBeenCalledTimes(1);

		// 集成到 useDesignPreview + DesignBar：超时后出现授权按钮，点击开窗，focus 后恢复 live 且 nonce 递增
		const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
		const p0 = makePage(0, "src/frames/f0.tsx");
		let latest!: DesignPreview;
		render(wrap(<Harness pages={[p0]} onPreview={(p) => (latest = p)} />));
		const first = latest.liveContent(p0);
		expect(first?.kind === "live" ? first.nonce : -1).toBe(0);

		act(() => latest.onTimeout());
		expect(latest.mode).toBe("static");
		expect(latest.blocked).toBe(true);
		const authBtn = screen.getByTestId("canvas-design-authorize");
		fireEvent.click(authBtn);
		expect(openSpy).toHaveBeenCalledWith(
			"https://t-abc-p5179.orb.example/__oryn/login?next=%2F",
			"_blank",
			"noopener",
		);

		act(() => {
			window.dispatchEvent(new Event("focus"));
		});
		expect(latest.mode).toBe("live");
		const after = latest.liveContent(p0);
		expect(after?.kind === "live" ? after.nonce : -1).toBe(1);
	});

	it("FrameLink 对 portal 帧发消息把 targetOrigin 写成 portal 源", () => {
		const iframe = document.createElement("iframe");
		const postMessage = vi.fn();
		Object.defineProperty(iframe, "contentWindow", {
			value: { postMessage },
		});
		const link = new FrameLink(iframe, "https://t-abc-p5179.orb.example");
		link.post({ kind: "design-reload" });
		expect(postMessage).toHaveBeenCalledWith(
			expect.objectContaining({ kind: "design-reload" }),
			"https://t-abc-p5179.orb.example",
		);
		link.dispose();
	});

	it("selection 带 loc 进入 anchorOf、selectionPrompt 与 handoffPrompt", () => {
		const sel: PageSelection = {
			path: "dist/login.html",
			src: null,
			tag: "button",
			text: "立即注册",
			textOnly: true,
			style: {
				color: "",
				"background-color": "",
				"font-size": "",
				"font-weight": "",
				"text-align": "",
				padding: "",
				"border-radius": "",
			},
			loc: "src/frames/login.tsx:18:5",
		};
		const anchor = anchorOf(sel, {
			version: 3,
			path: "dist/login.html",
			page: { number: 1, name: "登录" },
		});
		expect(anchor.loc).toBe("src/frames/login.tsx:18:5");
		const sp = selectionPrompt(
			"改按钮颜色",
			{
				artifactId: "art_01JAAAAAAAAAAAAAAAAAAAAAAA",
				title: "产品落地页",
				version: 3,
				anchor,
			},
			i18n.t,
		);
		expect(sp).toContain("source src/frames/login.tsx:18:5");

		const hp = handoffPrompt(
			{ id: "art_01JAAAAAAAAAAAAAAAAAAAAAAA", title: "产品落地页" },
			3,
			[makePage(0, "src/frames/login.tsx")],
			[],
			i18n.t,
		);
		expect(hp).toContain("source src/frames/login.tsx");
	});

	it("带 source 的画框不能删除也不能复制，同选的普通标注不受影响", () => {
		const doc: CanvasDoc = {
			format: CANVAS_FORMAT_DESIGN,
			design: { engine: 1, start: "p0" },
			canvases: [{ id: "c1", name: "主画布" }],
			pages: [makePage(0, "src/frames/login.tsx"), makePage(1)],
			notes: [
				{
					id: "n1",
					canvas: "c1",
					kind: "sticky",
					x: 10,
					y: 10,
					width: 120,
					text: "改这里",
					color: "yellow",
					done: false,
					locked: false,
					by: null,
				},
			],
		};
		expect(deleteOps(doc, ["p0"])).toEqual([]);
		expect(deleteOps(doc, ["p0", "n1"])).toEqual([{ op: "remove", id: "n1" }]);
		expect(duplicateOps(doc, ["p0"]).ops).toEqual([]);
		expect(duplicateOps(doc, ["p0", "p1"]).ops).toHaveLength(1);
	});
});
