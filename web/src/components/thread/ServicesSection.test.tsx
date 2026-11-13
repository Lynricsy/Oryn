/**
 * 信息面板的「服务」小节：状态点颜色、失败原因、按状态给启动 / 停止、动作调对应接口、「在 Portal 中打开」
 * 切到 Portal 标签并选中端口、orb 不就绪时禁用与置灰，以及日志对话框每 3s 以 `bg=1` 轮询。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Orb, Schemas, Thread } from "../../api/client";
import i18n from "../../i18n";
import { realtime } from "../../realtime/socket";
import { getThreadStore } from "../../state/stores";
import { uiStore } from "../../state/ui";
import { InfoPanel } from "./InfoPanel";
import { SERVICE_LOG_POLL_MS, ServicesSection } from "./ServicesSection";

const ORB = "orb-svc000000000000000000001";
const THREAD = "T-svc-1";
let calls: { method: string; url: URL }[];
let logText: string;

const SERVICES: Schemas["OrbService"][] = [
	{
		name: "web",
		source: "file",
		command: "bun run dev",
		port: 5173,
		links: [{ title: "web", path: "/" }],
		status: "running",
		restarts: 2,
	},
	{
		name: "worker",
		source: "dynamic",
		command: "bun run worker",
		port: 20000,
		links: [],
		status: "failed",
		restarts: 5,
		error: "exited 5 times in a row within 1m0s (last exit code 1)",
	},
	{
		name: "db",
		source: "file",
		command: "postgres",
		links: [],
		status: "starting",
		restarts: 0,
	},
];

function orb(state: Orb["state"] = "ready"): Orb {
	return {
		id: ORB,
		state,
		stateChangedAt: 1,
		threadIds: [THREAD],
		services: SERVICES,
		portals: [{ port: 5173, service: "web", links: SERVICES[0]?.links ?? [] }],
	} as Orb;
}

function client() {
	return new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
}

function mount(o: Orb) {
	return render(
		<QueryClientProvider client={client()}>
			<ServicesSection threadId={THREAD} orb={o} />
		</QueryClientProvider>,
	);
}

const row = (name: string) => {
	const el = document.querySelector<HTMLElement>(`li[data-service="${name}"]`);
	if (!el) throw new Error(`no row ${name}`);
	return el;
};
/** 行里状态点的填充色类名。 */
const dot = (name: string) =>
	within(row(name)).getAllByRole("img")[0]?.lastElementChild?.className ?? "";

beforeEach(() => {
	calls = [];
	logText = "[orbd] starting web (port 5173)\n";
	uiStore.setState({ portalPort: {}, panelTab: "info" });
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			const req =
				input instanceof Request
					? input
					: new Request(new URL(String(input), location.origin), init);
			const url = new URL(req.url);
			calls.push({ method: req.method, url });
			if (url.pathname.endsWith("/log"))
				return new Response(logText, {
					headers: { "Content-Type": "text/plain; charset=utf-8" },
				});
			if (url.pathname.includes("/services/"))
				return Response.json({ ...SERVICES[0], status: "stopped" });
			return Response.json(
				{ code: "not_found", message: "x" },
				{ status: 404 },
			);
		}),
	);
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
	vi.restoreAllMocks();
});

describe("服务小节", () => {
	it("状态点按状态着色；失败行给出原因；端口与重启次数", () => {
		mount(orb());
		expect(dot("web")).toContain("bg-ok");
		expect(dot("worker")).toContain("bg-err");
		expect(dot("db")).toContain("bg-warn");
		expect(row("web")).toHaveTextContent(":5173");
		expect(row("web")).toHaveTextContent(
			i18n.t("services.restarts", { count: 2 }),
		);
		expect(row("worker")).toHaveTextContent(
			"exited 5 times in a row within 1m0s (last exit code 1)",
		);
		// 运行中的不再写状态文字（点已表达），其余写
		expect(row("web")).not.toHaveTextContent(i18n.t("services.status.running"));
		expect(row("db")).toHaveTextContent(i18n.t("services.status.starting"));
	});

	it("按状态二选一给启动 / 停止，点击调对应接口", async () => {
		mount(orb());
		expect(
			within(row("worker")).queryByRole("button", {
				name: i18n.t("services.stop", { name: "worker" }),
			}),
		).toBeNull();
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("services.stop", { name: "web" }),
			}),
		);
		await act(async () => {});
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("services.start", { name: "worker" }),
			}),
		);
		await act(async () => {});
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("services.restart", { name: "db" }),
			}),
		);
		await act(async () => {});
		expect(calls.map((c) => `${c.method} ${c.url.pathname}`)).toEqual([
			`POST /api/v1/orbs/${ORB}/services/web/stop`,
			`POST /api/v1/orbs/${ORB}/services/worker/start`,
			`POST /api/v1/orbs/${ORB}/services/db/restart`,
		]);
	});

	it("有 portal 的服务可在 Portal 中打开：选中端口并切到 Portal 标签", () => {
		mount(orb());
		expect(
			within(row("worker")).queryByRole("button", {
				name: i18n.t("services.open", { name: "worker" }),
			}),
		).toBeNull();
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("services.open", { name: "web" }),
			}),
		);
		expect(uiStore.getState().panelTab).toBe("portal");
		expect(uiStore.getState().portalPort[THREAD]).toBe(5173);
	});

	it("orb 不是 ready：动作禁用、状态点置灰", () => {
		mount(orb("frozen"));
		for (const name of ["web", "worker", "db"]) {
			expect(dot(name)).toContain("bg-ghost");
			for (const b of within(row(name)).getAllByRole("button"))
				if (b.getAttribute("aria-label")?.includes("Portal")) continue;
				else expect(b).toBeDisabled();
		}
	});

	it("日志对话框：打开即拉取、每 3s 以 bg=1 重新拉取，关闭后停止", async () => {
		vi.useFakeTimers();
		mount(orb());
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("services.log", { name: "web" }),
			}),
		);
		await act(() => vi.advanceTimersByTimeAsync(0));
		expect(screen.getByTestId("service-log")).toHaveTextContent(
			"[orbd] starting web (port 5173)",
		);
		logText += "[orbd] web is ready on port 5173\n";
		// 多给 50ms：请求落地后读响应体与查询通知还要走几轮定时器（离下一次轮询还远）
		await act(() => vi.advanceTimersByTimeAsync(SERVICE_LOG_POLL_MS + 50));
		expect(screen.getByTestId("service-log")).toHaveTextContent(
			"web is ready on port 5173",
		);
		const logs = () =>
			calls.filter((c) => c.url.pathname.endsWith("/services/web/log"));
		expect(logs().length).toBe(2);
		expect(logs().every((c) => c.url.searchParams.get("bg") === "1")).toBe(
			true,
		);

		fireEvent.click(
			within(screen.getByRole("dialog")).getAllByRole("button", {
				name: i18n.t("common.close"),
			})[0] as HTMLElement,
		);
		await act(() => vi.advanceTimersByTimeAsync(3 * SERVICE_LOG_POLL_MS));
		expect(logs().length).toBe(2);
	});

	it("信息面板的 orb 卡在「环境」之下列出服务；没有服务时不出现", () => {
		vi.spyOn(realtime, "cmd").mockResolvedValue({ subagents: [] } as never);
		getThreadStore(THREAD)
			.getState()
			.patchLocal({ thread: { id: THREAD, orbId: ORB } as Thread, orb: orb() });
		const { unmount } = render(
			<QueryClientProvider client={client()}>
				<InfoPanel threadId={THREAD} />
			</QueryClientProvider>,
		);
		const section = screen.getByTestId("orb-services");
		expect(section).toHaveTextContent(i18n.t("services.title"));
		expect(
			screen.getByText(i18n.t("env.title")).compareDocumentPosition(section) &
				Node.DOCUMENT_POSITION_FOLLOWING,
		).toBeTruthy();
		unmount();
		getThreadStore("T-svc-2")
			.getState()
			.patchLocal({
				thread: { id: "T-svc-2", orbId: "orb-svc2" } as Thread,
				orb: { ...orb(), id: "orb-svc2", services: [] },
			});
		render(
			<QueryClientProvider client={client()}>
				<InfoPanel threadId="T-svc-2" />
			</QueryClientProvider>,
		);
		expect(screen.queryByTestId("orb-services")).toBeNull();
	});
});
