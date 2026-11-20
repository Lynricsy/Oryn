/** 首页执行位置选择器（M11a）：预热池 orb 不出现在「已有 orb」里；带 Docker 的类型与 orb 显示「Docker」徽标。 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Orb, Provider } from "../api/client";
import { HomePage } from "./HomePage";

const PROVIDER: Provider = {
	id: "prov_1",
	name: "local",
	driver: "docker",
	capabilities: { freeze: true, snapshot: true },
	orbTypes: [
		{ name: "medium", cpus: 4, memoryMB: 8192 },
		{ name: "medium-docker", cpus: 4, memoryMB: 8192, features: ["docker"] },
	],
	defaultType: "medium",
	online: true,
	createdAt: 1,
} as Provider;

function orb(over: Partial<Orb> & Pick<Orb, "id">): Orb {
	return {
		providerId: "prov_1",
		type: "medium",
		cpus: 4,
		memoryMB: 8192,
		image: "oryn-orb:dev",
		state: "ready",
		stateChangedAt: 1,
		createdAt: 1,
		threadIds: [],
		...over,
	};
}

const ORBS: Orb[] = [
	orb({ id: "orb-work0000000000000001", threadIds: ["T-1"] }),
	orb({
		id: "orb-dock0000000000000002",
		threadIds: ["T-2"],
		type: "medium-docker",
		nestedDocker: true,
	}),
	orb({ id: "orb-pool0000000000000003", pool: "idle" }),
	orb({ id: "orb-warm0000000000000004", pool: "warming", state: "spawning" }),
];

beforeEach(() => {
	vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
		const url = new URL(
			typeof input === "object" && "url" in input ? input.url : String(input),
			"http://x",
		);
		if (url.pathname === "/api/v1/providers")
			return Response.json({ providers: [PROVIDER] });
		if (url.pathname === "/api/v1/projects")
			return Response.json({ projects: [] });
		if (url.pathname === "/api/v1/orbs") return Response.json({ orbs: ORBS });
		return Response.json({ code: "not_found", message: "" }, { status: 404 });
	});
});

afterEach(() => vi.unstubAllGlobals());

it("预热池 orb 不在「已有 orb」里；Docker 类型与 orb 带徽标", async () => {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<HomePage />
			</MemoryRouter>
		</QueryClientProvider>,
	);
	// 唯一的 provider：胶囊直接显示它和它的 defaultType
	fireEvent.click(
		await screen.findByRole("button", { name: "local · medium" }),
	);
	// 普通 orb 与 Docker orb 都在
	const work = await screen.findByRole("button", {
		name: /orb-work00000000/,
	});
	const dock = screen.getByRole("button", { name: /orb-dock00000000/ });
	expect(work).not.toHaveTextContent("Docker");
	expect(dock).toHaveTextContent("Docker");
	// 池 orb（idle / warming）都不出现
	expect(screen.queryByRole("button", { name: /orb-pool/ })).toBeNull();
	expect(screen.queryByRole("button", { name: /orb-warm/ })).toBeNull();
	// 新 orb 的类型：只有声明了 docker 特性的类型带徽标
	const types = screen
		.getAllByRole("button", { name: /^local/ })
		.map((b) => b.textContent ?? "");
	expect(types.filter((s) => s.includes("Docker"))).toHaveLength(1);
	expect(types.find((s) => s.includes("medium-docker"))).toContain("Docker");
});
