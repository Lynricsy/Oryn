/**
 * 访问弹层：公开要先二次确认再调 `setOrbPortalShare`；公开中续期（同一接口、不再确认）与终止
 * （`endOrbPortalShare`）；剩余时间随时钟每 30s 刷新；外点 / Esc 关闭，桌面弹层打开时焦点移入、Esc 后还给齿轮。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import i18n from "../../i18n";
import { AccessPopover } from "./AccessPopover";
import { formatRemaining } from "./portal-url";

const ORB = "orb-access000000000000000001";
const HOUR = 3_600_000;
let calls: { method: string; path: string; body: unknown }[];

function portal(expiresAt?: number): Schemas["OrbPortal"] {
	return {
		port: 5173,
		service: "web",
		links: [{ title: "web", path: "/" }],
		...(expiresAt ? { public: { expiresAt } } : {}),
	};
}

function mount(p: Schemas["OrbPortal"]) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	const ui = (x: Schemas["OrbPortal"]) => (
		<QueryClientProvider client={client}>
			<AccessPopover orbId={ORB} portal={x} />
		</QueryClientProvider>
	);
	const r = render(ui(p));
	return { ...r, update: (x: Schemas["OrbPortal"]) => r.rerender(ui(x)) };
}

const button = (name: string) => screen.getByRole("button", { name });
const openPopover = () => fireEvent.click(button(i18n.t("portal.access")));
const dur = (d: Schemas["PortalShareDuration"]) =>
	i18n.t(`portal.duration.${d}`);
/** 让 mutation 的请求与回调落地。 */
const flush = () => act(async () => {});

beforeEach(() => {
	calls = [];
	vi.stubGlobal(
		"fetch",
		vi.fn(async (input: RequestInfo | URL) => {
			const req = input as Request;
			const body = req.method === "PUT" ? await req.json() : undefined;
			calls.push({
				method: req.method,
				path: new URL(req.url).pathname,
				body,
			});
			if (req.method === "DELETE") return new Response(null, { status: 204 });
			return Response.json({
				...portal(),
				public: { expiresAt: Date.now() + HOUR },
			});
		}),
	);
});

afterEach(() => {
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

describe("AccessPopover", () => {
	it("未公开：说明仅你可访问；选时长后要确认才发请求，取消则不发", async () => {
		mount(portal());
		openPopover();
		const dialog = screen.getByRole("dialog", {
			name: i18n.t("portal.accessTitle", { port: 5173 }),
		});
		expect(dialog).toHaveTextContent(i18n.t("portal.private"));

		fireEvent.click(button(i18n.t("portal.publishFor", { d: dur("3h") })));
		expect(
			screen.getByText(
				i18n.t("portal.confirmPublish", { port: 5173, d: dur("3h") }),
			),
		).toBeInTheDocument();
		fireEvent.click(button(i18n.t("common.cancel")));
		expect(
			screen.queryByRole("button", { name: i18n.t("portal.confirm") }),
		).toBeNull();
		await flush();
		expect(calls).toEqual([]);

		fireEvent.click(button(i18n.t("portal.publishFor", { d: dur("1h") })));
		fireEvent.click(button(i18n.t("portal.confirm")));
		await flush();
		expect(calls).toEqual([
			{
				method: "PUT",
				path: `/api/v1/orbs/${ORB}/portals/5173/share`,
				body: { duration: "1h" },
			},
		]);
		// 请求成功后确认行收起
		expect(
			screen.queryByRole("button", { name: i18n.t("portal.confirm") }),
		).toBeNull();
	});

	it("公开中：显示到期与剩余时间；续期直接调同一接口，终止调 DELETE", async () => {
		const { update } = mount(portal());
		openPopover();
		update(portal(Date.now() + HOUR));
		expect(screen.getByTestId("portal-access")).toHaveAttribute(
			"data-state",
			"public",
		);
		fireEvent.click(button(i18n.t("portal.renewFor", { d: dur("7d") })));
		await flush();
		fireEvent.click(button(i18n.t("portal.end")));
		await flush();
		expect(calls.map((c) => [c.method, c.path, c.body])).toEqual([
			["PUT", `/api/v1/orbs/${ORB}/portals/5173/share`, { duration: "7d" }],
			["DELETE", `/api/v1/orbs/${ORB}/portals/5173/share`, undefined],
		]);
		// 控制面推送「已终止」后回到未公开
		update(portal());
		expect(screen.getByTestId("portal-access")).toHaveAttribute(
			"data-state",
			"private",
		);
	});

	it("剩余时间每 30s 随时钟刷新，到期即按未公开显示", () => {
		vi.useFakeTimers();
		const start = new Date("2026-11-13T10:00:00Z").getTime();
		vi.setSystemTime(start);
		const expiresAt = start + HOUR + 30_000;
		mount(portal(expiresAt));
		openPopover();
		const remaining = () => screen.getByTestId("portal-remaining").textContent;
		const leftText = (left: number) =>
			i18n.t("portal.remaining", {
				left: formatRemaining(left, i18n.language),
			});
		expect(remaining()).toBe(leftText(HOUR + 30_000));
		// 两次 30s 刷新后跨过整点：1 小时 → 59 分钟
		act(() => vi.advanceTimersByTime(120_000));
		expect(remaining()).toBe(leftText(HOUR - 90_000));
		expect(remaining()).toContain("59");
		act(() => vi.advanceTimersByTime(HOUR));
		expect(screen.getByTestId("portal-access")).toHaveAttribute(
			"data-state",
			"private",
		);
	});

	it("桌面弹层：打开时焦点移到第一个时长；Tab 到别的按钮后 Esc 关闭，焦点还给齿轮按钮", () => {
		mount(portal());
		const trigger = button(i18n.t("portal.access"));
		trigger.focus();
		fireEvent.click(trigger);
		const dialog = screen.getByRole("dialog");
		expect(document.activeElement).toBe(
			button(i18n.t("portal.publishFor", { d: dur("1h") })),
		);
		const other = button(i18n.t("portal.publishFor", { d: dur("7d") }));
		other.focus();
		fireEvent.keyDown(other, { key: "Escape" });
		expect(dialog).not.toBeInTheDocument();
		expect(document.activeElement).toBe(trigger);
	});

	it("Esc 时焦点已在弹层外：关闭但不抢焦点", () => {
		mount(portal());
		const outside = document.createElement("button");
		document.body.append(outside);
		openPopover();
		outside.focus();
		fireEvent.keyDown(outside, { key: "Escape" });
		expect(screen.queryByRole("dialog")).toBeNull();
		expect(document.activeElement).toBe(outside);
		outside.remove();
	});

	it("Esc 与点外面关闭", () => {
		mount(portal());
		openPopover();
		fireEvent.keyDown(window, { key: "Escape" });
		expect(screen.queryByRole("dialog")).toBeNull();
		openPopover();
		fireEvent.mouseDown(document.body);
		expect(screen.queryByRole("dialog")).toBeNull();
	});
});
