import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Schemas } from "../../api/client";
import { identityKeys } from "../../api/identity-queries";
import { AvatarTile } from "./AvatarTile";
import { AssistantAvatar, IdentityAvatar } from "./IdentityAvatar";

const PNG = "data:image/png;base64,AA==";
let fetches: string[];

beforeEach(() => {
	fetches = [];
	vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
		fetches.push(String(input instanceof Request ? input.url : input));
		return new Response(null, { status: 404 });
	});
});

afterEach(() => {
	vi.unstubAllGlobals();
});

const host = () => screen.getByTestId("host").firstElementChild as HTMLElement;

function inHost(node: React.ReactNode, qc = new QueryClient()) {
	return render(
		<QueryClientProvider client={qc}>
			<div data-testid="host">{node}</div>
		</QueryClientProvider>,
	);
}

describe("AvatarTile 形状", () => {
	it.each([
		["图片", { avatarImage: PNG }, "image"],
		["字形", { avatarIcon: "🦊" }, "icon"],
		["未设置", undefined, "default"],
	] as const)("%s：缺省圆角方块，circle 为正圆", (_, identity, kind) => {
		const tile = inHost(
			<AvatarTile
				of="role"
				identity={identity}
				size={28}
				tone="accent"
				fallback="F"
			/>,
		);
		expect(host().getAttribute("data-avatar")).toBe(kind);
		expect(host().getAttribute("data-avatar-of")).toBe("role");
		expect(host().className).toContain("rounded-md");
		expect(host().className).not.toContain("rounded-full");
		tile.unmount();

		inHost(
			<AvatarTile
				of="assistant"
				identity={identity}
				size={20}
				tone="accent"
				fallback="F"
				shape="circle"
			/>,
		);
		expect(host().getAttribute("data-avatar-shape")).toBe("circle");
		expect(host().className).toContain("rounded-full");
		expect(host().className).not.toContain("rounded-md");
	});
});

describe("IdentityAvatar", () => {
	it("未设置时是品牌标；working 时光标闪", () => {
		inHost(<IdentityAvatar identity={undefined} of="agent" working />);
		expect(host().getAttribute("data-avatar")).toBe("default");
		expect(
			host().querySelector("[data-brand-caret]")?.getAttribute("class"),
		).toBe("caret-blink");
	});

	it("role 身份（带额外字段）按头像渲染，of 原样写出", () => {
		const face = { key: "k", role: "Mare", avatarIcon: "🌊" };
		inHost(<IdentityAvatar identity={face} of="role" size={28} />);
		expect(host().getAttribute("data-avatar")).toBe("icon");
		expect(host().getAttribute("data-avatar-of")).toBe("role");
		expect(host().textContent).toBe("🌊");
		expect(host().querySelector("[data-brand-caret]")).toBeNull();
	});
});

describe("AssistantAvatar 回落链", () => {
	function seeded(
		assistant: Schemas["AgentIdentity"],
		agent: Schemas["AgentIdentity"],
	) {
		const qc = new QueryClient();
		qc.setQueryData(identityKeys.assistant, assistant);
		qc.setQueryData(identityKeys.agent, agent);
		return qc;
	}

	it("助手自己的头像优先", () => {
		inHost(
			<AssistantAvatar shape="circle" />,
			seeded({ avatarImage: PNG }, { avatarIcon: "🦊" }),
		);
		expect(host().getAttribute("data-avatar")).toBe("image");
		expect(host().getAttribute("data-avatar-of")).toBe("assistant");
		expect(host().className).toContain("rounded-full");
	});

	it("助手没设头像 → agent 的", () => {
		inHost(
			<AssistantAvatar />,
			seeded({ name: "小助手" }, { avatarIcon: "🦊" }),
		);
		expect(host().textContent).toBe("🦊");
		expect(host().className).toContain("rounded-md");
	});

	it("都没设 → 品牌标", () => {
		inHost(<AssistantAvatar />, seeded({}, {}));
		expect(host().getAttribute("data-avatar")).toBe("default");
		expect(host().querySelector("[data-brand-caret]")).not.toBeNull();
		expect(fetches).toEqual([]); // 缓存新鲜，不重复请求
	});
});
