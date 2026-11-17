/**
 * mock 的全局助手设置（M19，与控制面同形）：`GET / PUT /settings/assistant` 与 `GET /assistant/mcp-servers`。
 * 全局 `mcp.json` 用内置的四台服务器代替：两台可用（HTTP / SSE），一台 stdio、一台需要 OAuth；
 * `enabled` 随禁用表变化。PUT 的校验与去重同服务端。
 */
import type { Schemas } from "../api/client";

type Settings = Schemas["AssistantSettings"];
type McpServer = Schemas["AssistantMcpServer"];

const MAX_DISABLED = 200;
const MAX_NAME = 200;

/** 全局 `mcp.json` 解析后的服务器（按名字排序，同服务端）。 */
export const MOCK_MCP_SERVERS: readonly Omit<McpServer, "enabled">[] = [
	{ name: "filesystem", transport: "stdio", usable: false, reason: "stdio" },
	{
		name: "github",
		transport: "http",
		host: "api.githubcopilot.com",
		usable: true,
	},
	{
		name: "linear",
		transport: "sse",
		host: "mcp.linear.app",
		usable: true,
	},
	{
		name: "notion",
		transport: "http",
		host: "mcp.notion.com",
		usable: false,
		reason: "oauth",
	},
];

function json(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

const invalid = (message: string) =>
	json({ code: "invalid_request", message }, 400);

export class MockAssistant {
	settings: Settings = {
		webAccess: true,
		disabledMcpServers: [],
		computer: {},
	};

	async handle(req: Request, p: string, m: string): Promise<Response | null> {
		if (p === "settings/assistant") {
			if (m === "GET") return json(this.settings);
			if (m === "PUT") return this.put((await req.json()) as Settings);
			return null;
		}
		if (p === "assistant/mcp-servers" && m === "GET")
			return json({
				servers: MOCK_MCP_SERVERS.map((s) => ({
					...s,
					enabled: !this.settings.disabledMcpServers.includes(s.name),
				})),
			});
		return null;
	}

	private put(b: Settings): Response {
		const names = b.disabledMcpServers ?? [];
		if (names.length > MAX_DISABLED)
			return invalid(`at most ${MAX_DISABLED} disabled MCP servers`);
		if (names.some((n) => !n || [...n].length > MAX_NAME))
			return invalid(`MCP server names must be 1–${MAX_NAME} characters`);
		const computer: Settings["computer"] = {};
		if (b.computer?.provider) computer.provider = b.computer.provider;
		if (b.computer?.orbType) computer.orbType = b.computer.orbType;
		this.settings = {
			webAccess: b.webAccess,
			disabledMcpServers: [...new Set(names)],
			computer,
		};
		return json(this.settings);
	}
}
