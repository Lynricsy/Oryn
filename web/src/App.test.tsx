import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";

function renderApp() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<App />
		</QueryClientProvider>,
	);
}

describe("首页", () => {
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("显示 /healthz 返回的版本号", async () => {
		const fetchMock = vi.fn(async () =>
			Response.json({ ok: true, version: "v1.2.3-test" }),
		);
		vi.stubGlobal("fetch", fetchMock);

		renderApp();

		expect(
			await screen.findByRole("heading", { name: "Oryn v1.2.3-test" }),
		).toBeInTheDocument();
		expect(fetchMock).toHaveBeenCalledWith("/healthz", expect.anything());
	});
});
