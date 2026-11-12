import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import i18n from "../i18n";
import type { SettingsSection } from "./SettingsPage";
import { SettingsPage } from "./SettingsPage";

// 编辑器换成 textarea：这里测设置页接线，CodeMirror 本身另有测试
vi.mock("../components/editor/CodeEditor", () => ({
	default: ({
		value,
		path,
		onChange,
	}: {
		value: string;
		path: string;
		onChange?(v: string): void;
	}) => (
		<textarea
			aria-label={`editor:${path}`}
			value={value}
			onChange={(e) => onChange?.(e.target.value)}
		/>
	),
}));

interface Call {
	method: string;
	path: string;
	body: unknown;
	csrf: string | null;
}

let identity: { name: string; email: string } | undefined;
let calls: Call[] = [];
let agentFiles: Record<string, string> = {};
let agentPuts: { path: string; body: string }[] = [];

async function agentConfig(req: Request, url: URL): Promise<Response> {
	if (url.pathname === "/api/v1/agent-config/files")
		return Response.json({
			files: Object.entries(agentFiles).map(([path, c]) => ({
				path,
				size: c.length,
				mode: 420,
				sha256: "0".repeat(64),
				updatedAt: 1,
			})),
		});
	const path = decodeURIComponent(
		url.pathname.slice("/api/v1/agent-config/files/".length),
	);
	if (req.method === "PUT") {
		const body = await req.text();
		agentPuts.push({ path, body });
		agentFiles[path] = body;
		return Response.json({
			file: {
				path,
				size: body.length,
				mode: 420,
				sha256: "0".repeat(64),
				updatedAt: 2,
			},
			extractedSecrets: ["omp/models/acme/apiKey"],
			warnings: [
				{ path, line: 3, kind: "possible_secret", message: "looks like a key" },
			],
		});
	}
	const c = agentFiles[path];
	return c === undefined
		? Response.json({ code: "not_found", message: path }, { status: 404 })
		: new Response(c);
}

const fetchMock = vi.fn(
	async (input: RequestInfo | URL, init?: RequestInit) => {
		// rawFetch 传相对路径与 init；Node 的 Request 只收绝对地址
		const req =
			input instanceof Request
				? input
				: new Request(new URL(String(input), "http://x"), init);
		const url = new URL(req.url, "http://x");
		if (url.pathname.startsWith("/api/v1/agent-config/"))
			return agentConfig(req, url);
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
	},
);

function setup(section: SettingsSection = "git-identity") {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>
			<MemoryRouter>
				<SettingsPage section={section} />
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

describe("设置 → agent 配置", () => {
	beforeEach(() => {
		localStorage.clear();
		agentFiles = {
			"AGENTS.md": "# rules\n",
			"models.yml": "providers: {}\n",
			"skills/foo/SKILL.md": "skill",
		};
		agentPuts = [];
		calls = [];
		vi.stubGlobal("fetch", fetchMock);
	});
	afterEach(() => {
		vi.unstubAllGlobals();
	});

	it("用同一个文件浏览器：目录由扁平列表合成，打开、编辑、保存后显示抽取的密钥与警告", async () => {
		setup("agent-config");
		expect(await screen.findByText("skills")).toBeInTheDocument();
		expect(screen.queryByText("SKILL.md")).toBeNull();
		expect(
			screen.queryByRole("button", {
				name: i18n.t("files.actions.newFolder" as "common.save"),
			}),
		).toBeNull();
		fireEvent.click(screen.getByText("models.yml"));
		const editor = await screen.findByLabelText("editor:models.yml");
		expect(editor).toHaveValue("providers: {}\n");
		fireEvent.change(editor, {
			target: { value: "providers:\n  acme: {apiKey: sk-1}\n" },
		});
		fireEvent.click(screen.getByRole("button", { name: "保存" }));
		expect(
			await screen.findByText("已抽取进密钥库：omp/models/acme/apiKey"),
		).toBeInTheDocument();
		expect(
			screen.getByText("models.yml:3 — looks like a key"),
		).toBeInTheDocument();
		expect(agentPuts).toEqual([
			{ path: "models.yml", body: "providers:\n  acme: {apiKey: sk-1}\n" },
		]);
		// 旧的 textarea 编辑器已删除
		expect(screen.queryByRole("textbox", { name: "models.yml" })).toBeNull();
	});
});
