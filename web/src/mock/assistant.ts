/**
 * mock 的全局助手设置（M19，与控制面同形）：`GET / PUT /settings/assistant` 与 `GET /assistant/mcp-servers`。
 * 全局 `mcp.json` 用内置的四台服务器代替：两台可用（HTTP / SSE），一台 stdio、一台需要 OAuth；
 * `enabled` 随禁用表变化。PUT 的校验与去重同服务端。主线备忘 `GET / PUT /assistant/main/notes` 也在这里（上限同服务端）。
 */
import type { Schemas } from "../api/client";

type Settings = Schemas["AssistantSettings"];
type McpServer = Schemas["AssistantMcpServer"];

const MAX_DISABLED = 200;
const MAX_NAME = 200;
const MAX_NOTES = 1500;

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
	/** 主线备忘（M19）：`GET / PUT /assistant/main/notes`，上限 1500 个 Unicode 字符。 */
	notes: Schemas["AssistantNotes"] = {
		text: "- 主人早上 9 点前不看消息，简报放到 9 点后\n- 周五提醒写周报",
		updatedAt: Date.now() - 2 * 3600_000,
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
		if (p === "assistant/main/notes") {
			if (m === "GET") return json(this.notes);
			if (m !== "PUT") return null;
			const b = (await req.json()) as Schemas["AssistantNotes"];
			const text = typeof b.text === "string" ? b.text : "";
			if ([...text].length > MAX_NOTES)
				return invalid(`notes must be at most ${MAX_NOTES} characters`);
			this.notes = { text, updatedAt: Date.now() };
			return json(this.notes);
		}
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
