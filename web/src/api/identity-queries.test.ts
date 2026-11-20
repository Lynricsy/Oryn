import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	AGENT_DEFAULT_GLYPHS,
	GENERIC_AGENT_GLYPH,
} from "../lib/avatar-defaults";
import type { Schemas } from "./client";
import {
	agentFace,
	effectiveAssistant,
	identityKeys,
	mergeAgentAvatar,
	mergeRoleIdentity,
	saveAgentAvatar,
	saveComposerPrefs,
	saveIdentity,
	saveRoleAvatar,
	useAgentFace,
	useAssistantIdentity,
	useComposerPrefs,
	useIdentity,
} from "./identity-queries";

type Call = { method: string; path: string; body?: unknown };
let calls: Call[];
/** 路径 → GET 的响应体；PUT 一律回显请求体（role-identities 回显成 RoleIdentity）。 */
let gets: Record<string, unknown>;

beforeEach(() => {
	calls = [];
	gets = {};
	vi.stubGlobal(
		"fetch",
		async (input: RequestInfo | URL, init?: RequestInit) => {
			const req = input instanceof Request ? input : new Request(input, init);
			const path = new URL(req.url).pathname;
			const text = req.method === "GET" ? "" : await req.text();
			const body = text ? JSON.parse(text) : undefined;
			calls.push({ method: req.method, path, body });
			const json = (v: unknown) =>
				new Response(JSON.stringify(v), {
					status: 200,
					headers: { "content-type": "application/json" },
				});
			if (req.method === "GET")
				return path in gets
					? json(gets[path])
					: new Response(null, { status: 404 });
			if (path === "/api/v1/settings/role-identities")
				return json({ ...(body as object), inCycle: body.role !== "gone" });
			return json(body);
		},
	);
});

afterEach(() => {
	vi.unstubAllGlobals();
});

const newClient = () =>
	new QueryClient({ defaultOptions: { queries: { retry: false } } });

function wrapper(qc: QueryClient) {
	return ({ children }: { children: ReactNode }) =>
		createElement(QueryClientProvider, { client: qc }, children);
}

describe("effectiveAssistant", () => {
	const png = { avatarImage: "data:image/png;base64,AA==" };
	it.each([
		["都没设", undefined, { avatar: {} }],
		[
			"只设名字：头像留空（显示处给 sparkle）",
			{ name: "小助手" },
			{ name: "小助手", avatar: {} },
		],
		["只设头像：名字留空（显示处给「全局助手」）", png, { avatar: png }],
		[
			"空白名字与空串字段等同未设置",
			{ name: "  ", avatarIcon: "", avatarImage: "" },
			{ avatar: {} },
		],
	] as const)("%s", (_, assistant, want) => {
		expect(effectiveAssistant(assistant)).toEqual(want);
	});
});

describe("mergeRoleIdentity", () => {
	const list: Schemas["RoleIdentityList"] = {
		roles: [
			{ role: "Mare", inCycle: true },
			{ role: "Cyrene", inCycle: true, avatarIcon: "🌙" },
			{ role: "Old", inCycle: false, avatarIcon: "x" },
		],
		defaultThinkingLevel: "high",
	};
	it("替换同名项，保持顺序与其它字段", () => {
		const out = mergeRoleIdentity(list, {
			role: "Mare",
			inCycle: true,
			avatarIcon: "🌊",
		});
		expect(out.roles.map((r) => r.role)).toEqual(["Mare", "Cyrene", "Old"]);
		expect(out.roles[0]).toEqual({
			role: "Mare",
			inCycle: true,
			avatarIcon: "🌊",
		});
		expect(list.roles[0]).toEqual({ role: "Mare", inCycle: true }); // 不改原对象
	});
	it("清除遗留项的头像 → 从列表删掉；清除 cycle 项 → 保留但没有头像", () => {
		expect(
			mergeRoleIdentity(list, { role: "Old", inCycle: false }).roles.map(
				(r) => r.role,
			),
		).toEqual(["Mare", "Cyrene"]);
		const out = mergeRoleIdentity(list, { role: "Cyrene", inCycle: true });
		expect(out.roles[1]).toEqual({ role: "Cyrene", inCycle: true });
	});
	it("列表里没有的项追加到末尾；清除一个不存在的遗留项不变", () => {
		expect(
			mergeRoleIdentity(list, { role: "New", inCycle: true, avatarIcon: "n" })
				.roles.length,
		).toBe(4);
		expect(mergeRoleIdentity(list, { role: "Nope", inCycle: false })).toBe(
			list,
		);
	});
});

describe("useIdentity", () => {
	it("按归属请求各自的接口；enabled=false 不请求", async () => {
		gets["/api/v1/settings/assistant-identity"] = { name: "小助手" };
		const qc = newClient();
		const off = renderHook(() => useIdentity("agent", false), {
			wrapper: wrapper(qc),
		});
		const on = renderHook(() => useIdentity("assistant"), {
			wrapper: wrapper(qc),
		});
		await waitFor(() =>
			expect(on.result.current.data).toEqual({ name: "小助手" }),
		);
		expect(off.result.current.fetchStatus).toBe("idle");
		expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
			"GET /api/v1/settings/assistant-identity",
		]);
	});
});

describe("saveIdentity", () => {
	it("整体 PUT 并写回缓存；用户身份不发名字", async () => {
		const qc = newClient();
		await saveIdentity(qc, "agent", { name: "Oryn", avatarIcon: "🦊" });
		await saveIdentity(qc, "assistant", {});
		await saveIdentity(qc, "user", { name: "ignored", avatarIcon: "🐱" });
		expect(calls).toEqual([
			{
				method: "PUT",
				path: "/api/v1/settings/agent-identity",
				body: { name: "Oryn", avatarIcon: "🦊" },
			},
			{ method: "PUT", path: "/api/v1/settings/assistant-identity", body: {} },
			{
				method: "PUT",
				path: "/api/v1/settings/user-identity",
				body: { avatarIcon: "🐱" },
			},
		]);
		expect(qc.getQueryData(identityKeys.agent)).toEqual({
			name: "Oryn",
			avatarIcon: "🦊",
		});
		expect(qc.getQueryData(identityKeys.assistant)).toEqual({});
		expect(qc.getQueryData(identityKeys.user)).toEqual({ avatarIcon: "🐱" });
	});
});

describe("saveRoleAvatar", () => {
	it("PUT {role, 头像} 并把结果换进列表缓存；`{}` 只发 role", async () => {
		const qc = newClient();
		qc.setQueryData(identityKeys.roles, {
			roles: [
				{ role: "Mare", inCycle: true },
				{ role: "gone", inCycle: false, avatarIcon: "g" },
			],
		});
		await saveRoleAvatar(qc, "Mare", { avatarIcon: "🌊", avatarImage: "" });
		await saveRoleAvatar(qc, "gone", {});
		expect(calls.map((c) => c.body)).toEqual([
			{ role: "Mare", avatarIcon: "🌊" },
			{ role: "gone" },
		]);
		expect(qc.getQueryData(identityKeys.roles)).toEqual({
			roles: [{ role: "Mare", inCycle: true, avatarIcon: "🌊" }],
		});
	});

	it("列表还没加载时不凭空造缓存", async () => {
		const qc = newClient();
		await saveRoleAvatar(qc, "Mare", { avatarIcon: "🌊" });
		expect(qc.getQueryData(identityKeys.roles)).toBeUndefined();
	});
});

describe("useAssistantIdentity", () => {
	it("只读助手自己的身份，不回落到 Agent 身份", async () => {
		gets["/api/v1/settings/assistant-identity"] = { avatarIcon: "✨" };
		gets["/api/v1/settings/agent-identity"] = {
			name: "Oryn",
			avatarIcon: "🦊",
		};
		const { result } = renderHook(() => useAssistantIdentity(), {
			wrapper: wrapper(newClient()),
		});
		await waitFor(() =>
			expect(result.current).toEqual({ avatar: { avatarIcon: "✨" } }),
		);
		expect(calls.map((c) => c.path)).toEqual([
			"/api/v1/settings/assistant-identity",
		]);
	});

	it("enabled=false 时不发请求，返回空身份", () => {
		const { result } = renderHook(() => useAssistantIdentity(false), {
			wrapper: wrapper(newClient()),
		});
		expect(result.current).toEqual({ avatar: {} });
		expect(calls).toEqual([]);
	});
});

describe("agent 头像", () => {
	const list: Schemas["AgentAvatarList"] = {
		agents: [
			{ name: "task", source: "bundled" },
			{ name: "scout", source: "bundled", avatarIcon: "🔭" },
			{
				name: "writer",
				source: "user",
				avatarImage: "data:image/png;base64,AA==",
			},
			{ name: "gone", source: "orphan", avatarIcon: "👻" },
		],
	};

	it.each([
		[
			"内置 agent 没设头像 → 自己的默认字形",
			"task",
			{ glyph: AGENT_DEFAULT_GLYPHS.task },
		],
		[
			"内置 agent 设了头像 → 自定义头像",
			"scout",
			{ face: { avatarIcon: "🔭" }, glyph: AGENT_DEFAULT_GLYPHS.scout },
		],
		[
			"自定义 agent 设了头像",
			"writer",
			{
				face: { avatarImage: "data:image/png;base64,AA==" },
				glyph: GENERIC_AGENT_GLYPH,
			},
		],
		["未知 agent → 通用字形", "nobody", { glyph: GENERIC_AGENT_GLYPH }],
		["名字区分大小写", "Task", { glyph: GENERIC_AGENT_GLYPH }],
		["原型属性名不当成内置", "constructor", { glyph: GENERIC_AGENT_GLYPH }],
	] as const)("agentFace：%s", (_, name, want) => {
		expect(agentFace(list, name)).toEqual(want);
	});

	it("agentFace：列表未加载时只有默认字形", () => {
		expect(agentFace(undefined, "sonic")).toEqual({
			glyph: AGENT_DEFAULT_GLYPHS.sonic,
		});
	});

	it("mergeAgentAvatar：替换同名项；清除了头像的遗留项删掉；新名字追加", () => {
		expect(
			mergeAgentAvatar(list, {
				name: "task",
				source: "bundled",
				avatarIcon: "🛠",
			}).agents[0],
		).toEqual({ name: "task", source: "bundled", avatarIcon: "🛠" });
		expect(
			mergeAgentAvatar(list, { name: "gone", source: "orphan" }).agents.map(
				(a) => a.name,
			),
		).toEqual(["task", "scout", "writer"]);
		expect(
			mergeAgentAvatar(list, { name: "x", source: "user", avatarIcon: "x" })
				.agents.length,
		).toBe(5);
		expect(mergeAgentAvatar(list, { name: "nope", source: "orphan" })).toBe(
			list,
		);
	});

	it("saveAgentAvatar：PUT {name, 头像} 并写回列表缓存；`{}` 只发 name", async () => {
		const qc = newClient();
		qc.setQueryData(identityKeys.agents, list);
		await saveAgentAvatar(qc, "task", { avatarIcon: "🛠", avatarImage: "" });
		await saveAgentAvatar(qc, "scout", {});
		expect(calls.map((c) => [c.method, c.path, c.body])).toEqual([
			[
				"PUT",
				"/api/v1/settings/agent-avatars",
				{ name: "task", avatarIcon: "🛠" },
			],
			["PUT", "/api/v1/settings/agent-avatars", { name: "scout" }],
		]);
		const cached = qc.getQueryData<Schemas["AgentAvatarList"]>(
			identityKeys.agents,
		);
		expect(cached?.agents[0]).toEqual({ name: "task", avatarIcon: "🛠" });
		expect(cached?.agents[1]).toEqual({ name: "scout" });
	});

	it("useAgentFace：读头像列表；enabled=false 时不发请求，只给默认字形", async () => {
		gets["/api/v1/settings/agent-avatars"] = list;
		const off = renderHook(() => useAgentFace("scout", false), {
			wrapper: wrapper(newClient()),
		});
		expect(off.result.current).toEqual({ glyph: AGENT_DEFAULT_GLYPHS.scout });
		expect(calls).toEqual([]);
		const on = renderHook(() => useAgentFace("scout"), {
			wrapper: wrapper(newClient()),
		});
		await waitFor(() =>
			expect(on.result.current.face).toEqual({ avatarIcon: "🔭" }),
		);
	});
});

describe("新对话输入框偏好", () => {
	it("读取并整体写回，缓存换成服务端返回值", async () => {
		gets["/api/v1/settings/composer"] = { defaultTarget: "role" };
		const qc = newClient();
		const { result } = renderHook(() => useComposerPrefs(), {
			wrapper: wrapper(qc),
		});
		await waitFor(() =>
			expect(result.current.data).toEqual({ defaultTarget: "role" }),
		);
		await saveComposerPrefs(qc, { defaultTarget: "assistant" });
		expect(calls.at(-1)).toEqual({
			method: "PUT",
			path: "/api/v1/settings/composer",
			body: { defaultTarget: "assistant" },
		});
		await waitFor(() =>
			expect(result.current.data).toEqual({ defaultTarget: "assistant" }),
		);
	});
});
