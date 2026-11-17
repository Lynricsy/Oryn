/**
 * mock 的全局助手设置（M19，与控制面同形）：`GET / PUT /settings/assistant` 与 `GET /assistant/mcp-servers`。
 * 全局 `mcp.json` 用内置的四台服务器代替：两台可用（HTTP / SSE），一台 stdio、一台需要 OAuth；
 * `enabled` 随禁用表变化。PUT 的校验与去重同服务端。主线备忘 `GET / PUT /assistant/main/notes` 也在这里（上限同服务端）。
 * 助手的电脑（M19 步骤 4.3）：`GET /assistant/computer` 与 `POST …/wake | pause | reset`。唤醒时没有则按设置里的
 * provider 与类型建一台带桌面、不属于任何线程的 orb（`computer: true`），直接到 ready；暂停是冻结（没有 → 404）；
 * 重置删除它（没有时无操作）。orb 经 `deps.setOrb` 写进 server 的 orb 表并推送，文件与桌面接口照常可用。
 */
import type { Orb, Schemas } from "../api/client";
import { makeOrb } from "./fixtures";

type Settings = Schemas["AssistantSettings"];
type McpServer = Schemas["AssistantMcpServer"];
type Computer = Schemas["AssistantComputer"];

/** server 的 orb 表（电脑是其中的一台）。 */
export interface AssistantDeps {
	orbs: Map<string, Orb>;
	setOrb(orb: Orb): void;
}

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
	/** 助手的电脑：orb id（没有时为空）；工具定义第一次唤醒后即视为已缓存，重置不清。 */
	private computerId: string | undefined;
	private toolsReady = false;
	private seq = 0;
	private readonly deps: AssistantDeps | undefined;
	/** 主线备忘（M19）：`GET / PUT /assistant/main/notes`，上限 1500 个 Unicode 字符。 */
	notes: Schemas["AssistantNotes"] = {
		text: "- 主人早上 9 点前不看消息，简报放到 9 点后\n- 周五提醒写周报",
		updatedAt: Date.now() - 2 * 3600_000,
	};

	constructor(deps?: AssistantDeps) {
		this.deps = deps;
	}

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
		if (p === "assistant/computer" && m === "GET")
			return json(this.computerView());
		const action = /^assistant\/computer\/(wake|pause|reset)$/.exec(p);
		if (action && m === "POST") return this.computerAction(action[1] ?? "");
		return null;
	}

	private computerOrb(): Orb | undefined {
		const orb = this.computerId
			? this.deps?.orbs.get(this.computerId)
			: undefined;
		return orb && orb.state !== "deleted" ? orb : undefined;
	}

	private computerView(): Computer {
		const orb = this.computerOrb();
		return {
			toolsReady: this.toolsReady,
			...(orb
				? {
						orbId: orb.id,
						state: orb.state,
						providerName: orb.providerName,
						type: orb.type,
					}
				: {}),
		};
	}

	private computerAction(action: string): Response {
		if (!this.deps)
			return json({ code: "coordinator_unavailable", message: "mock" }, 503);
		const orb = this.computerOrb();
		const now = Date.now();
		if (action === "wake") {
			if (orb) {
				if (orb.state !== "ready")
					this.deps.setOrb({ ...orb, state: "ready", stateChangedAt: now });
			} else {
				const id = `orb-computer${(this.seq++).toString(16).padStart(14, "0")}`;
				this.computerId = id;
				this.deps.setOrb({
					...makeOrb(id, []),
					projectId: undefined,
					providerName: this.settings.computer.provider ?? "local",
					type: this.settings.computer.orbType ?? "medium",
					computer: true,
					createdAt: now,
					stateChangedAt: now,
					desktop: { displayReady: true, browser: "stopped" },
				});
			}
			this.toolsReady = true;
			return json(this.computerView());
		}
		if (action === "pause") {
			if (!orb)
				return json(
					{ code: "not_found", message: "the assistant has no computer yet" },
					404,
				);
			this.deps.setOrb({ ...orb, state: "frozen", stateChangedAt: now });
			return json(this.computerView());
		}
		if (orb) {
			this.deps.setOrb({ ...orb, state: "deleted", stateChangedAt: now });
			this.deps.orbs.delete(orb.id);
		}
		this.computerId = undefined;
		return json(this.computerView());
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
