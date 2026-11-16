/**
 * 在场头像（M14 6.7）：正在改的在前、在读的在后；最多 3 个头像加「+N」；观看者多于 1 时「在 N 处打开」；
 * 点头像打开线程并在右面板显示这个 artifact；画布上点头像弹菜单，「跟随」写进跟随状态。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import type {
	ArtifactActivityEvent,
	ArtifactEvent,
} from "../../realtime/socket";
import { indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { getFollowing, setFollowing } from "./canvas/follow";
import {
	PresenceFaces,
	presenceOrder,
	type ThreadActivity,
} from "./PresenceFaces";

const ID = "art_01JDNACANVAS00000000000000";

interface FakeActivityClient {
	onArtifact(id: string, cb: (msg: ArtifactEvent) => void): () => void;
	lastArtifactActivity(id: string): ArtifactActivityEvent | undefined;
	emit(threads: ThreadActivity[], viewers?: number): void;
}

function fakeActivityClient(): FakeActivityClient {
	let last: ArtifactActivityEvent | undefined;
	const cbs = new Set<(msg: ArtifactEvent) => void>();
	return {
		onArtifact(_id: string, cb: (msg: ArtifactEvent) => void) {
			cbs.add(cb);
			return () => {
				cbs.delete(cb);
			};
		},
		lastArtifactActivity: () => last,
		emit(threads: ThreadActivity[], viewers = 1) {
			last = {
				type: "artifact.activity",
				id: ID,
				viewers,
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

function thread(id: string, title: string): Thread {
	return { id, title, kind: "work" } as unknown as Thread;
}

function mount(client: FakeActivityClient, canFollow = false) {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={qc}>
			<MemoryRouter initialEntries={["/artifacts/x"]}>
				<Routes>
					<Route
						path="/artifacts/x"
						element={
							<PresenceFaces
								artifactId={ID}
								canFollow={canFollow}
								client={client}
								threads={[{ threadId: "T-gone", title: "旧线程" }]}
							/>
						}
					/>
					<Route path="/t/:id" element={<p>thread page</p>} />
				</Routes>
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

function faces(): string[] {
	return screen
		.queryAllByTestId("presence-face")
		.map((el) => el.getAttribute("data-thread") ?? "");
}

beforeEach(() => {
	vi.stubGlobal(
		"fetch",
		vi.fn(async () => new Response(null, { status: 404 })),
	);
	indexStore
		.getState()
		.setAll(
			[
				thread("T-a", "读的"),
				thread("T-b", "写的"),
				thread("T-c", "画的"),
				thread("T-d", "挪的"),
				thread("T-e", "又一个读的"),
			],
			1,
		);
});

afterEach(() => {
	vi.unstubAllGlobals();
	setFollowing(ID, null);
});

describe("presenceOrder", () => {
	it("正在改的在前，同一线程只留一项（取正在改的）", () => {
		const order = presenceOrder([
			{ threadId: "T-a", doing: "read" },
			{ threadId: "T-b", doing: "write" },
			{ threadId: "T-a", doing: "move" },
			{ threadId: "T-c", doing: "read" },
		]);
		// 同类之间按线程第一次出现的顺序
		expect(order.map((a) => [a.threadId, a.doing])).toEqual([
			["T-a", "move"],
			["T-b", "write"],
			["T-c", "read"],
		]);
	});
});

describe("PresenceFaces", () => {
	it("没有在场信息时不渲染", () => {
		const client = fakeActivityClient();
		mount(client);
		expect(screen.queryByTestId("presence-faces")).toBeNull();
		client.emit([], 1);
		expect(screen.queryByTestId("presence-faces")).toBeNull();
	});

	it("正在改的在前并带转圈，在读的在后", () => {
		const client = fakeActivityClient();
		mount(client);
		client.emit([
			{ threadId: "T-a", doing: "read" },
			{ threadId: "T-b", doing: "write", focus: { path: "index.html" } },
		]);
		expect(faces()).toEqual(["T-b", "T-a"]);
		const [b, a] = screen.getAllByTestId("presence-face");
		expect(b?.querySelector("[data-working]")).not.toBeNull();
		expect(a?.querySelector("[data-working]")).toBeNull();
		expect(b).toHaveAccessibleName("写的：正在改");
	});

	it("最多 3 个头像，其余并成「+N」", () => {
		const client = fakeActivityClient();
		mount(client);
		client.emit([
			{ threadId: "T-a", doing: "read" },
			{ threadId: "T-b", doing: "write" },
			{ threadId: "T-c", doing: "draw" },
			{ threadId: "T-d", doing: "move" },
			{ threadId: "T-e", doing: "read" },
		]);
		expect(faces()).toEqual(["T-b", "T-c", "T-d"]);
		expect(screen.getByTestId("presence-more")).toHaveTextContent("+2");
	});

	it("观看者多于 1 时显示「在 N 处打开」", () => {
		const client = fakeActivityClient();
		mount(client);
		client.emit([], 2);
		expect(screen.getByTestId("presence-viewers")).toHaveTextContent(
			"在 2 处打开",
		);
		client.emit([{ threadId: "T-a", doing: "read" }], 1);
		expect(screen.queryByTestId("presence-viewers")).toBeNull();
	});

	it("索引里没有的线程用详情给的标题", () => {
		const client = fakeActivityClient();
		mount(client);
		client.emit([{ threadId: "T-gone", doing: "read" }]);
		expect(screen.getByTestId("presence-face")).toHaveAccessibleName(
			"旧线程：在读",
		);
	});

	it("点头像打开线程，并在右面板显示这个 artifact", () => {
		const client = fakeActivityClient();
		mount(client);
		client.emit([{ threadId: "T-b", doing: "write" }]);
		fireEvent.click(screen.getByTestId("presence-face"));
		expect(screen.getByText("thread page")).toBeInTheDocument();
		const ui = uiStore.getState();
		expect(ui.artifactSel["T-b"]).toBe(ID);
		expect(ui.panelTab).toBe("artifacts");
	});

	it("画布上点头像弹菜单：跟随、停止跟随、打开线程", () => {
		const client = fakeActivityClient();
		mount(client, true);
		client.emit([{ threadId: "T-b", doing: "write" }]);
		fireEvent.click(screen.getByTestId("presence-face"));
		fireEvent.click(screen.getByRole("menuitem", { name: "跟随 写的" }));
		expect(getFollowing(ID)).toBe("T-b");
		fireEvent.click(screen.getByTestId("presence-face"));
		fireEvent.click(screen.getByRole("menuitem", { name: "停止跟随" }));
		expect(getFollowing(ID)).toBeNull();
		fireEvent.click(screen.getByTestId("presence-face"));
		fireEvent.click(screen.getByRole("menuitem", { name: "打开线程" }));
		expect(screen.getByText("thread page")).toBeInTheDocument();
	});
});
