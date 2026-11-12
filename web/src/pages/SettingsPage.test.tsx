import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SettingsPage } from "./SettingsPage";

interface Call {
	method: string;
	path: string;
	body: unknown;
	csrf: string | null;
}

let identity: { name: string; email: string } | undefined;
let calls: Call[] = [];

const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
	const req = input instanceof Request ? input : new Request(String(input));
	const url = new URL(req.url, "http://x");
	const text = await req.text();
	calls.push({
		method: req.method,
		path: url.pathname,
		body: text ? JSON.parse(text) : undefined,
		csrf: req.headers.get("X-Oryn-CSRF"),
	});
	if (url.pathname === "/api/v1/settings/git-identity") {
		if (req.method === "PUT") {
			identity = JSON.parse(text);
		} else if (req.method === "DELETE") {
			identity = undefined;
			return new Response(null, { status: 204 });
		}
		return Response.json(identity ? { identity } : {});
	}
	return Response.json({ code: "not_found", message: "" }, { status: 404 });
});

function setup() {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<SettingsPage section="git-identity" />
			</MemoryRouter>
		</QueryClientProvider>,
	);
}

const writes = () => calls.filter((c) => c.method !== "GET");

describe("设置 → Git 身份", () => {
	beforeEach(() => {
		identity = undefined;
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
		fetchMock.mockClear();
	});
	afterEach(() => {
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it("未设置时填写并保存：以 PUT 发送去掉首尾空白的名字与邮箱", async () => {
		setup();
		expect(await screen.findByText(/尚未设置/)).toBeInTheDocument();
		const save = screen.getByRole("button", { name: "保存" });
		expect(save).toBeDisabled();
		fireEvent.change(screen.getByLabelText("名字"), {
			target: { value: "  Ada Lovelace " },
		});
		fireEvent.change(screen.getByLabelText("邮箱"), {
			target: { value: "ada@example.com" },
		});
		fireEvent.click(save);
		expect(
			await screen.findByText("当前：Ada Lovelace <ada@example.com>"),
		).toBeInTheDocument();
		expect(writes()).toEqual([
			{
				method: "PUT",
				path: "/api/v1/settings/git-identity",
				body: { name: "Ada Lovelace", email: "ada@example.com" },
				csrf: "1",
			},
		]);
		expect(screen.getByText("已保存")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
	});

	it("已设置时清除：确认后以 DELETE 清除并清空输入框", async () => {
		identity = { name: "Grace", email: "grace@example.com" };
		vi.spyOn(window, "confirm").mockReturnValue(true);
		setup();
		expect(
			await screen.findByText("当前：Grace <grace@example.com>"),
		).toBeInTheDocument();
		expect(screen.getByLabelText("名字")).toHaveValue("Grace");
		fireEvent.click(screen.getByRole("button", { name: "清除" }));
		expect(await screen.findByText(/尚未设置/)).toBeInTheDocument();
		expect(writes()).toEqual([
			{
				method: "DELETE",
				path: "/api/v1/settings/git-identity",
				body: undefined,
				csrf: "1",
			},
		]);
		expect(screen.getByLabelText("名字")).toHaveValue("");
		expect(screen.getByLabelText("邮箱")).toHaveValue("");
		expect(screen.queryByRole("button", { name: "清除" })).toBeNull();
	});

	it("取消确认时不发送 DELETE", async () => {
		identity = { name: "Grace", email: "grace@example.com" };
		vi.spyOn(window, "confirm").mockReturnValue(false);
		setup();
		fireEvent.click(await screen.findByRole("button", { name: "清除" }));
		await waitFor(() => expect(window.confirm).toHaveBeenCalled());
		expect(writes()).toEqual([]);
	});
});
