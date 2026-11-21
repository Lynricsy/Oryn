/** fork 对话框的环境选项：box 线程（M23）与 runner 线程只有「共用」，普通 orb 线程「共用」「复制」都有。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it } from "vitest";
import type { Orb, Thread } from "../../api/client";
import { makeOrb } from "../../mock/fixtures";
import { getThreadStore, resetThreadStores } from "../../state/stores";
import { ForkDialog } from "./Dialogs";

const T = "T-0193a1b2-0000-7000-8000-0000000000f1";
const ORB = "orb-01jcz8m3w4x5y6z7a8b9c0ffff";

function seed(orb: Partial<Orb>) {
	const thread = {
		id: T,
		title: "fork 源",
		titleSource: "auto",
		state: "idle",
		stateChangedAt: 0,
		pinned: false,
		labels: [],
		unread: false,
		createdAt: 0,
		updatedAt: 0,
		orbId: ORB,
	} as unknown as Thread;
	getThreadStore(T)
		.getState()
		.apply([
			{ type: "thread", thread },
			{ type: "orb", orb: { ...makeOrb(ORB, [T]), ...orb } },
		]);
}

function mount() {
	const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(
		<QueryClientProvider client={qc}>
			<MemoryRouter>
				<ForkDialog threadId={T} onClose={() => undefined} />
			</MemoryRouter>
		</QueryClientProvider>,
	);
	return screen.getAllByRole("radio");
}

afterEach(() => resetThreadStores());

it("普通 orb 线程：共用与复制两个选项", () => {
	seed({});
	expect(mount()).toHaveLength(2);
});

it("box 线程：只有共用", () => {
	seed({ box: true, projectId: undefined });
	expect(mount()).toHaveLength(1);
});

it("runner 线程：只有共用", () => {
	seed({ kind: "runner" });
	expect(mount()).toHaveLength(1);
});
