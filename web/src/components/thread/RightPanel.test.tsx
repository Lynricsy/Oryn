/** 右侧面板：换线程时 Changes 面板按线程重建，不把上一个线程的范围（所选 commit）带到新 orb。 */
import {
	act,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Thread } from "../../api/client";
import i18n from "../../i18n";
import { handleOrbFetch, resetOrbFs } from "../../mock/orbfs";
import { getThreadStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { RightPanel } from "./RightPanel";

vi.mock("../../state/terminals", () => ({
	terminalHub: { onExit: vi.fn(() => () => {}) },
	useTerminals: () => false,
}));

const ORB_A = "orb-rp00000000000000000000a1";
const ORB_B = "orb-rp00000000000000000000b2";
const SHA = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b";
let statusQueries: { orb: string; query: URLSearchParams }[];

beforeEach(() => {
	resetOrbFs();
	localStorage.clear();
	statusQueries = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			if (url.pathname.endsWith("/git/status"))
				statusQueries.push({
					orb: url.pathname.split("/")[4] ?? "",
					query: url.searchParams,
				});
			return (await handleOrbFetch(req)) ?? new Response(null, { status: 404 });
		}),
	);
	for (const [id, orbId] of [
		["T-rpA", ORB_A],
		["T-rpB", ORB_B],
	] as const)
		getThreadStore(id)
			.getState()
			.patchLocal({ thread: { id, orbId } as Thread });
	uiStore.getState().setPanelTab("changes");
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("RightPanel", () => {
	it("在线程 A 选了 commit，切到线程 B：B 的请求按自己的默认范围，不带 A 的 commit", async () => {
		const { rerender } = render(<RightPanel threadId="T-rpA" />);
		const picker = await screen.findByRole(
			"combobox",
			{ name: i18n.t("changes.scope.label") },
			{ timeout: 5000 },
		);
		await waitFor(() =>
			expect(
				screen.getByRole("option", { name: /^1a2b3c4 docs: 更新 README/ }),
			).toBeInTheDocument(),
		);
		fireEvent.change(picker, { target: { value: `commit:${SHA}` } });
		await waitFor(() =>
			expect(statusQueries.at(-1)?.query.get("commit")).toBe(SHA),
		);

		await act(async () => {
			rerender(<RightPanel threadId="T-rpB" />);
		});
		await waitFor(() => expect(statusQueries.at(-1)?.orb).toBe(ORB_B));
		const forB = statusQueries.filter((q) => q.orb === ORB_B);
		expect(forB.map((q) => q.query.get("scope"))).toEqual(["all"]);
		expect(forB.some((q) => q.query.has("commit"))).toBe(false);
		expect(
			screen.getByRole("combobox", { name: i18n.t("changes.scope.label") }),
		).toHaveValue("all");
	});
});
