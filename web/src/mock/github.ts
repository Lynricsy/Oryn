/**
 * mock 后端的 M14（GitHub 集成）部分：连接状态（App、两个身份槽位、安装、自动化身份），格式与 orynd 一致。
 * `MockServer.handleFetch` 在 runner 路由之前调 `handle`，不归这里的请求返回 null。
 * 初始：App 已连接（slug `oryn-mock`）、「我的账号」`@oryn-owner` 已连接、Agent 账号未连接、两个安装、自动化身份 `user`；
 * `localStorage["oryn.mock.github"] = "none"` 时初始为未连接（截图与验收用）。
 * 跳转类接口（创建 App、连接账号）不会真的去 GitHub：创建 App 清掉 `none` 标记并回到 `?created=1`（整页刷新后即为已连接）；
 * 连接账号给出一个 GitHub 授权地址的样子，要在 mock 里连上 Agent 账号用 `connectAgent()`。
 */
import type { Schemas } from "../api/client";

type Json = Record<string, unknown>;
type Status = Schemas["GitHubStatus"];
type Identity = Schemas["GitHubIdentity"];
type Slot = Schemas["GitHubIdentitySlot"];

const MOCK_KEY = "oryn.mock.github";

export interface GitHubDeps {
	/** GitHub 集成状态变了：推送 WS `github.changed` */
	changed(): void;
}

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

function disconnectedMocked(): boolean {
	try {
		return globalThis.localStorage?.getItem(MOCK_KEY) === "none";
	} catch {
		return false;
	}
}

/** 方形字母头像（data URI，mock 不发外网请求）。 */
function avatar(letter: string, hue: number): string {
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="hsl(${hue} 45% 52%)"/><text x="32" y="42" font-family="sans-serif" font-size="30" font-weight="600" fill="#fff" text-anchor="middle">${letter}</text></svg>`;
	return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
}

const MIN = 60_000;

function ownerIdentity(now: number): Identity {
	return {
		login: "oryn-owner",
		githubId: 5_120_001,
		name: "Oryn Owner",
		avatarUrl: avatar("O", 210),
		status: "ok",
		connectedAt: now - 9 * 24 * 60 * MIN,
	};
}

function agentIdentity(now: number): Identity {
	return {
		login: "oryn-bot",
		githubId: 5_120_007,
		name: "Oryn Bot",
		avatarUrl: avatar("B", 150),
		status: "ok",
		connectedAt: now,
	};
}

function connectedStatus(now: number): Status {
	return {
		app: {
			appId: 1_048_576,
			slug: "oryn-mock",
			name: "Oryn oryn.example",
			htmlUrl: "https://github.com/apps/oryn-mock",
			ownerLogin: "oryn-owner",
			public: false,
			installUrl: "https://github.com/apps/oryn-mock/installations/new",
		},
		identities: { user: ownerIdentity(now) },
		installations: [
			{
				id: 51_000_001,
				accountLogin: "oryn-owner",
				accountType: "User",
				repositorySelection: "all",
				suspended: false,
			},
			{
				id: 51_000_002,
				accountLogin: "oryn-demo",
				accountType: "Organization",
				repositorySelection: "selected",
				suspended: false,
			},
		],
		automationIdentity: "user",
		webhookUrl: `${globalThis.location?.origin ?? "https://oryn.example"}/api/v1/public/github/webhook`,
		lastDeliveryAt: now - 2 * MIN,
		lastDeliverySource: "webhook",
	};
}

function emptyStatus(): Status {
	return {
		identities: {},
		installations: [],
		automationIdentity: "user",
		webhookUrl: `${globalThis.location?.origin ?? "https://oryn.example"}/api/v1/public/github/webhook`,
	};
}

export class MockGitHub {
	status: Status;
	private readonly deps: GitHubDeps;

	constructor(deps: GitHubDeps) {
		this.deps = deps;
		this.status = disconnectedMocked()
			? emptyStatus()
			: connectedStatus(Date.now());
	}

	/** 连上 Agent 账号 `@oryn-bot`（测试与截图用）。 */
	connectAgent(): void {
		this.status = {
			...this.status,
			identities: {
				...this.status.identities,
				agent: agentIdentity(Date.now()),
			},
		};
		this.deps.changed();
	}

	/** 让某个槽位的令牌失效（显示「需要重新授权」）。 */
	expire(slot: Slot): void {
		const cur = this.status.identities[slot];
		if (!cur) return;
		this.status = {
			...this.status,
			identities: {
				...this.status.identities,
				[slot]: { ...cur, status: "reauth" },
			},
		};
		this.deps.changed();
	}

	async handle(
		p: string,
		m: string,
		body: () => Promise<unknown>,
		_url: URL,
	): Promise<Response | null> {
		if (p === "github" && m === "GET") return json(this.status);
		if (p === "github/app/manifest" && m === "POST") {
			if (this.status.app)
				return error(409, "conflict", "a GitHub App is already connected");
			await body();
			try {
				globalThis.localStorage?.removeItem(MOCK_KEY);
			} catch {
				// 无 localStorage：刷新后仍按默认（已连接）初始化
			}
			return json({
				url: `${globalThis.location?.origin ?? ""}/settings/github?created=1`,
			});
		}
		if (p === "github/app" && m === "DELETE") {
			this.status = emptyStatus();
			this.deps.changed();
			return new Response(null, { status: 204 });
		}
		const auth = /^github\/identities\/(user|agent)\/authorize$/.exec(p);
		if (auth && m === "POST") {
			if (!this.status.app)
				return error(409, "github_not_connected", "no GitHub App connected");
			const state = `ogs_mock${Date.now().toString(36)}`;
			return json({
				url: `https://github.com/login/oauth/authorize?client_id=Iv1.mock&redirect_uri=${encodeURIComponent(`${globalThis.location?.origin ?? ""}/api/v1/public/github/oauth/callback`)}&state=${state}&prompt=select_account`,
			});
		}
		const ident = /^github\/identities\/(user|agent)$/.exec(p);
		if (ident && m === "DELETE") {
			const slot = ident[1] as Slot;
			const identities = { ...this.status.identities };
			delete identities[slot];
			this.status = {
				...this.status,
				identities,
				automationIdentity:
					slot === "agent" ? "user" : this.status.automationIdentity,
			};
			this.deps.changed();
			return new Response(null, { status: 204 });
		}
		if (p === "github/settings" && m === "PUT") {
			const b = (await body()) as Json;
			const slot = b.automationIdentity;
			if (slot !== "user" && slot !== "agent")
				return error(400, "invalid_request", "automationIdentity 无效");
			if (slot === "agent" && !this.status.identities.agent)
				return error(
					409,
					"github_agent_required",
					"the agent account is not connected",
				);
			this.status = { ...this.status, automationIdentity: slot };
			this.deps.changed();
			return json(this.status);
		}
		if (p === "github/installations/sync" && m === "POST") {
			if (!this.status.app)
				return error(409, "github_not_connected", "no GitHub App connected");
			this.deps.changed();
			return json(this.status);
		}
		return null;
	}
}
