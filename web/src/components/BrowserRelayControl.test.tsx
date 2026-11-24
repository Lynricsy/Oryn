import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "../i18n";
import { api } from "../api/client";
import {
	BrowserRelayApprovalBanner,
	BrowserRelayChip,
} from "./BrowserRelayControl";

function wrap(ui: React.ReactElement) {
	const qc = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("BrowserRelayControl", () => {
	const getSpy = vi.spyOn(api, "GET");
	const putSpy = vi.spyOn(api, "PUT");
	const delSpy = vi.spyOn(api, "DELETE");

	beforeEach(() => {
		getSpy.mockReset();
		putSpy.mockReset();
		delSpy.mockReset();
	});

	afterEach(() => {
		cleanup();
	});

	it("未绑定且无在线扩展时不渲染按钮", async () => {
		getSpy.mockImplementation(async (path) => {
			if (path === "/api/v1/browser-relay/clients") {
				return { data: { clients: [] }, response: new Response() } as never;
			}
			return {
				data: {
					threadId: "thr_1",
					bound: false,
					online: false,
					sharedTabCount: 0,
					pendingApprovals: 0,
				},
				response: new Response(),
			} as never;
		});

		wrap(<BrowserRelayChip threadId="thr_1" />);
		await waitFor(() => expect(getSpy).toHaveBeenCalledTimes(2));
		expect(screen.queryByTestId("browser-relay-chip")).toBeNull();
	});

	it("有在线扩展时可选择绑定，并展示待审批提示条与解绑操作", async () => {
		let bound = false;
		getSpy.mockImplementation(async (path) => {
			if (path === "/api/v1/browser-relay/clients") {
				return {
					data: {
						clients: [
							{
								deviceId: "dev_ext1",
								name: "Chrome Mac",
								connectedAt: 1000,
								bindings: [],
							},
						],
					},
					response: new Response(),
				} as never;
			}
			return {
				data: {
					threadId: "thr_1",
					bound,
					deviceId: bound ? "dev_ext1" : undefined,
					deviceName: bound ? "Chrome Mac" : undefined,
					online: bound,
					sharedTabCount: bound ? 2 : 0,
					pendingApprovals: bound ? 1 : 0,
				},
				response: new Response(),
			} as never;
		});
		putSpy.mockImplementation(async () => {
			bound = true;
			return {
				data: {
					threadId: "thr_1",
					bound: true,
					deviceId: "dev_ext1",
					deviceName: "Chrome Mac",
					online: true,
					sharedTabCount: 2,
					pendingApprovals: 1,
				},
				response: new Response(),
			} as never;
		});
		delSpy.mockImplementation(async () => {
			bound = false;
			return {
				data: {
					threadId: "thr_1",
					bound: false,
					online: false,
					sharedTabCount: 0,
					pendingApprovals: 0,
				},
				response: new Response(),
			} as never;
		});

		wrap(
			<div>
				<BrowserRelayChip threadId="thr_1" />
				<BrowserRelayApprovalBanner threadId="thr_1" />
			</div>,
		);

		const chip = await screen.findByTestId("browser-relay-chip");
		fireEvent.click(chip);
		const bindItem = await screen.findByText(/Chrome Mac/);
		fireEvent.click(bindItem);

		await waitFor(() => expect(putSpy).toHaveBeenCalledTimes(1));
		const banner = await screen.findByTestId("browser-relay-approval-banner");
		expect(banner.textContent).toContain("Chrome Mac");

		fireEvent.click(screen.getByTestId("browser-relay-chip"));
		const unbindItem = await screen.findByText(/解除绑定|Unbind/);
		fireEvent.click(unbindItem);
		await waitFor(() => expect(delSpy).toHaveBeenCalledTimes(1));
	});
});
