/**
 * 画布上的 agent 在场（M14 6.7）：指针在 focus 画框中心、四种文案、改与画时的高亮框；跟随时镜头跟着 focus 走、
 * 切到别的画布页、用户自己平移即停止。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { type ReactNode, useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../../api/client";
import type { CanvasDoc, CanvasPage } from "../../../lib/artifacts/canvas-doc";
import type {
	ArtifactActivityEvent,
	ArtifactEvent,
} from "../../../realtime/socket";
import { indexStore } from "../../../state/stores";
import type { ThreadActivity } from "../PresenceFaces";
import {
	AgentPointer,
	AgentPointers,
	FollowBar,
	POINTER_STACK,
	pointerSpots,
	useAgentPresence,
	workingBoxes,
} from "./AgentPointer";
import { type Camera, IDENTITY, panBy, toWorld } from "./camera";
import { getFollowing, setFollowing } from "./follow";

const ID = "art_01JDNACANVAS00000000000000";

function page(id: string, canvas: string, x: number, name = id): CanvasPage {
	return {
		id,
		canvas,
		name,
		file: `${id}.html`,
		x,
		y: 0,
		width: 400,
		height: 300,
		locked: false,
	};
}

const DOC: CanvasDoc = {
	format: 1,
	canvases: [
		{ id: "c1", name: "首页" },
		{ id: "c2", name: "详情" },
	],
	pages: [
		page("p1", "c1", 0, "首屏"),
		page("p2", "c1", 3000, "页脚"),
		page("p3", "c2", 6000, "详情页"),
	],
	notes: [],
} as CanvasDoc;

interface FakeActivityClient {
	onArtifact(id: string, cb: (msg: ArtifactEvent) => void): () => void;
	lastArtifactActivity(id: string): ArtifactActivityEvent | undefined;
	emit(threads: ThreadActivity[]): void;
}

function fakeActivityClient(): FakeActivityClient {
	let last: ArtifactActivityEvent | undefined;
	const cbs = new Set<(msg: ArtifactEvent) => void>();
	return {
		onArtifact(_id, cb) {
			cbs.add(cb);
			return () => {
				cbs.delete(cb);
			};
		},
		lastArtifactActivity: () => last,
		emit(threads) {
			last = {
				type: "artifact.activity",
				id: ID,
				viewers: 1,
				threads,
				clients: [],
				visitors: 0,
			};
			const msg = last;
			act(() => {
				for (const cb of [...cbs]) cb(msg);
			});
		},
	};
}

function withQuery(node: ReactNode) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return <QueryClientProvider client={qc}>{node}</QueryClientProvider>;
}

const SIZE = { width: 800, height: 600 };
let shownCam: Camera = IDENTITY;
let shownCanvas = "c1";

function Harness({ client }: { client: FakeActivityClient }): ReactNode {
	const [cam, setCam] = useState<Camera>(IDENTITY);
	const [canvasKey, setCanvasKey] = useState("c1");
	shownCam = cam;
	shownCanvas = canvasKey;
	const presence = useAgentPresence({
		artifactId: ID,
		live: true,
		doc: DOC,
		canvasKey,
		cam,
		size: SIZE,
		setCam,
		switchCanvas: setCanvasKey,
		client,
	});
	return (
		<>
			<button type="button" onClick={() => setCam((c) => panBy(c, 40, 0))}>
				pan
			</button>
			<FollowBar presence={presence} />
			<AgentPointers presence={presence} />
		</>
	);
}

/** 镜头视口中心落在 page 上（世界坐标）。 */
function centerIn(p: CanvasPage): boolean {
	const c = toWorld(shownCam, SIZE.width / 2, SIZE.height / 2);
	return (
		c.x >= p.x && c.x <= p.x + p.width && c.y >= p.y && c.y <= p.y + p.height
	);
}

/** 画框完整在视口里。 */
function visible(p: CanvasPage): boolean {
	const a = toWorld(shownCam, 0, 0);
	const b = toWorld(shownCam, SIZE.width, SIZE.height);
	return (
		p.x >= a.x && p.y >= a.y && p.x + p.width <= b.x && p.y + p.height <= b.y
	);
}

beforeEach(() => {
	vi.stubGlobal(
		"fetch",
		vi.fn(async () => new Response(null, { status: 404 })),
	);
	indexStore
		.getState()
		.setAll(
			[{ id: "T-1", title: "改版", kind: "work" } as unknown as Thread],
			1,
		);
	shownCam = IDENTITY;
	shownCanvas = "c1";
});

afterEach(() => {
	vi.unstubAllGlobals();
	setFollowing(ID, null);
});

describe("pointerSpots / workingBoxes", () => {
	const cam: Camera = { x: 10, y: 20, zoom: 0.5 };

	it("指针在 focus 画框的中心，同一画框上的依次往下错开；别的画布页不画", () => {
		const spots = pointerSpots(
			[
				{
					threadId: "A",
					doing: "read",
					focus: { path: "p1.html", page: "p1" },
				},
				{
					threadId: "B",
					doing: "write",
					focus: { path: "p1.html", page: "p1" },
				},
				{
					threadId: "C",
					doing: "draw",
					focus: { path: "p3.html", page: "p3" },
				},
				{ threadId: "D", doing: "read", focus: { path: "canvas.json" } },
			],
			DOC,
			"c1",
			cam,
		);
		// 中心 (200, 150) → 屏幕 (110, 95)；正在改的 B 在前
		expect(spots.map((s) => [s.activity.threadId, s.x, s.y])).toEqual([
			["B", 110, 95],
			["A", 110, 95 + POINTER_STACK],
		]);
	});

	it("改与画的画框有高亮框，读与挪没有", () => {
		const spots = pointerSpots(
			[
				{
					threadId: "A",
					doing: "move",
					focus: { path: "p1.html", page: "p1" },
				},
				{
					threadId: "B",
					doing: "draw",
					focus: { path: "p2.html", page: "p2" },
				},
			],
			DOC,
			"c1",
			cam,
		);
		expect(workingBoxes(spots, cam)).toEqual([
			{ id: "p2", box: { x: 1510, y: 20, width: 200, height: 150 } },
		]);
	});
});

describe("AgentPointer", () => {
	it("四种文案", () => {
		const p = DOC.pages[0] as CanvasPage;
		const text = {
			read: "在看「首屏」",
			write: "正在改「首屏」",
			draw: "正在画「首屏」",
			move: "正在挪「首屏」",
		} as const;
		for (const doing of ["read", "write", "draw", "move"] as const) {
			const { unmount } = render(
				withQuery(
					<AgentPointer
						spot={{
							activity: { threadId: "T-1", doing },
							page: p,
							x: 12,
							y: 34,
						}}
					/>,
				),
			);
			const el = screen.getByTestId("agent-pointer");
			expect(el).toHaveTextContent(text[doing]);
			expect(el.style.left).toBe("12px");
			expect(el.style.top).toBe("34px");
			expect(el.title).toBe(`改版 · ${text[doing]}`);
			unmount();
		}
	});
});

describe("useAgentPresence 跟随", () => {
	const at = (pageId: string, doing: ThreadActivity["doing"] = "write") => [
		{ threadId: "T-1", doing, focus: { path: `${pageId}.html`, page: pageId } },
	];

	it("镜头跟着 focus 走，其它指针变淡，用户平移即停止", () => {
		const client = fakeActivityClient();
		render(withQuery(<Harness client={client} />));
		client.emit([
			...at("p2"),
			{
				threadId: "T-2",
				doing: "read",
				focus: { path: "p1.html", page: "p1" },
			},
		]);
		expect(screen.getAllByTestId("agent-pointer")).toHaveLength(2);
		expect(visible(DOC.pages[1] as CanvasPage)).toBe(false);

		act(() => setFollowing(ID, "T-1"));
		expect(visible(DOC.pages[1] as CanvasPage)).toBe(true);
		expect(screen.getByTestId("canvas-following")).toHaveTextContent(
			"正在跟随 改版",
		);
		const dimmed = screen
			.getAllByTestId("agent-pointer")
			.map((el) => [el.getAttribute("data-thread"), el.style.opacity]);
		expect(dimmed).toEqual([
			["T-1", "1"],
			["T-2", "0.35"],
		]);

		// focus 换到另一个画框：镜头跟过去
		client.emit(at("p1"));
		expect(visible(DOC.pages[0] as CanvasPage)).toBe(true);
		expect(getFollowing(ID)).toBe("T-1");

		// 用户自己平移：停止跟随，之后 focus 再变镜头不动
		fireEvent.click(screen.getByText("pan"));
		expect(getFollowing(ID)).toBeNull();
		expect(screen.queryByTestId("canvas-following")).toBeNull();
		const before = shownCam;
		client.emit(at("p2"));
		expect(shownCam).toBe(before);
	});

	it("focus 在别的画布页：先切过去再移镜头", () => {
		const client = fakeActivityClient();
		render(withQuery(<Harness client={client} />));
		client.emit(at("p3", "draw"));
		act(() => setFollowing(ID, "T-1"));
		expect(shownCanvas).toBe("c2");
		expect(centerIn(DOC.pages[2] as CanvasPage)).toBe(true);
		expect(getFollowing(ID)).toBe("T-1");
	});

	it("「停止跟随」按钮与线程离开（回合结束）都停止跟随", () => {
		const client = fakeActivityClient();
		render(withQuery(<Harness client={client} />));
		client.emit(at("p2"));
		act(() => setFollowing(ID, "T-1"));
		fireEvent.click(screen.getByRole("button", { name: "停止跟随" }));
		expect(getFollowing(ID)).toBeNull();

		act(() => setFollowing(ID, "T-1"));
		expect(getFollowing(ID)).toBe("T-1");
		client.emit([]);
		expect(getFollowing(ID)).toBeNull();
	});
});
