/**
 * 首屏 role 胶囊（M13 5.3）：`allowAssistant=false` 时没有全局助手项（R17）；选 role、选全局助手、指定模型（目录与自由输入）
 * 三种 onChange 的取值；role 行给出按中央目录解析出的模型；窄视口下渲染为底部抽屉。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RoleCapsule, type Target } from "./RoleCapsule";

const chat = (provider: string, id: string, name: string) => ({
	provider,
	id,
	selector: `${provider}/${id}`,
	name,
	kind: "chat",
});

/** `GET settings/role-identities` 的 roles（按 KnownRoles：cycleOrder 在前，其余已知 role inCycle=false）。 */
let roleList: unknown[];
/** 响应里的 `defaultThinkingLevel`；undefined = 不带（旧控制面） */
let defaultThinking: string | undefined;

beforeEach(() => {
	window.innerWidth = 1280;
	roleList = [
		{ role: "smol", inCycle: true, selector: "openai/gpt-lite" },
		{ role: "default", inCycle: true, selector: "openai/gpt-x" },
		{ role: "Cyrene", name: "Cyrene", inCycle: true },
		{ role: "old", inCycle: false, avatarIcon: "x" },
	];
	defaultThinking = undefined;
	vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
		const url = new URL(
			typeof input === "object" && "url" in input ? input.url : String(input),
			"http://x",
		);
		if (url.pathname === "/api/v1/settings/role-identities")
			return Response.json({
				roles: roleList,
				...(defaultThinking ? { defaultThinkingLevel: defaultThinking } : {}),
			});
		if (url.pathname === "/api/v1/models/catalog")
			return Response.json({
				// thinkingDefault：空串 = 已知没有自带默认档位（新版 Core）；不带 = 旧版 Core 不报告
				models: [
					{
						...chat("openai", "gpt-x", "GPT X"),
						thinking: ["low", "high"],
						thinkingDefault: "",
					},
					{ ...chat("openai", "gpt-lite", "GPT Lite"), thinkingDefault: "" },
					{
						...chat("anthropic", "claude-opus-5-5", "Claude Opus 5.5"),
						thinking: ["low", "medium", "high", "xhigh"],
						thinkingDefault: "",
					},
					{
						...chat("factory-droid", "claude-opus-5-5", "Droid Opus 5.5"),
						thinking: ["low", "medium", "high"],
						thinkingDefault: "medium",
					},
					{
						...chat("openai", "gpt-low", "GPT Low"),
						thinking: ["low"],
						thinkingDefault: "",
					},
					{
						...chat("legacy", "old-x", "Old X"),
						thinking: ["low", "high"],
					},
				],
				fetchedAt: 0,
				stale: false,
			});
		return Response.json({ code: "not_found", message: "" }, { status: 404 });
	});
});

afterEach(() => {
	vi.unstubAllGlobals();
	window.innerWidth = 1024;
});

/** 受控宿主：记下每次 onChange 的取值并回写。 */
function Host({
	allowAssistant,
	initial,
	changes,
}: {
	allowAssistant: boolean;
	initial: Target;
	changes: Target[];
}) {
	const [value, setValue] = useState(initial);
	return (
		<RoleCapsule
			value={value}
			allowAssistant={allowAssistant}
			onChange={(t) => {
				changes.push(t);
				setValue(t);
			}}
		/>
	);
}

function mount(allowAssistant: boolean, initial: Target) {
	const changes: Target[] = [];
	render(
		<QueryClientProvider
			client={
				new QueryClient({ defaultOptions: { queries: { retry: false } } })
			}
		>
			<Host
				allowAssistant={allowAssistant}
				initial={initial}
				changes={changes}
			/>
		</QueryClientProvider>,
	);
	return changes;
}

const openCapsule = () =>
	fireEvent.click(screen.getByRole("button", { name: /^对象：/ }));

describe("RoleCapsule", () => {
	it("allowAssistant=false：没有全局助手项；只列 cycleOrder 里的 role", async () => {
		mount(false, { kind: "role", role: "default" });
		openCapsule();
		await screen.findByRole("button", { name: /^Cyrene/ });
		expect(screen.queryByRole("button", { name: /全局助手/ })).toBeNull();
		expect(screen.getByRole("button", { name: /^快速/ })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /^old/ })).toBeNull();
	});

	it("role 行给出按中央目录解析出的模型", async () => {
		mount(true, { kind: "role", role: "default" });
		openCapsule();
		expect(
			await screen.findByRole("button", { name: /^默认.*X/ }),
		).toBeInTheDocument();
		expect(
			await screen.findByRole("button", { name: /^快速.*Lite/ }),
		).toBeInTheDocument();
	});

	it("default role 的胶囊显示解析出的模型，而不是「默认」", async () => {
		mount(true, { kind: "role", role: "default" });
		await vi.waitFor(() =>
			expect(
				screen.getByRole("button", { name: /^对象：/ }),
			).toHaveAccessibleName(/^对象：.*X$/),
		);
		expect(
			screen.getByRole("button", { name: /^对象：/ }),
		).not.toHaveAccessibleName(/默认/);
	});

	it("选 role、选全局助手、指定模型（目录 / 自由输入）的 onChange 取值", async () => {
		const changes = mount(true, { kind: "role", role: "default" });
		openCapsule();
		fireEvent.click(await screen.findByRole("button", { name: /^快速/ }));
		expect(changes.at(-1)).toEqual({ kind: "role", role: "smol" });

		openCapsule();
		fireEvent.click(screen.getByRole("button", { name: /^全局助手/ }));
		expect(changes.at(-1)).toEqual({ kind: "assistant" });

		// 全局助手下指定模型：仍是助手对象，带上模型
		openCapsule();
		fireEvent.click(screen.getByRole("button", { name: /^指定模型/ }));
		fireEvent.click(
			await vi.waitFor(() => {
				const el = document.querySelector<HTMLElement>(
					'[data-model="openai/gpt-x"]',
				);
				if (!el) throw new Error("模型列表未就绪");
				return el;
			}),
		);
		expect(changes.at(-1)).toEqual({
			kind: "assistant",
			model: { provider: "openai", id: "gpt-x", name: "GPT X" },
		});

		// 普通对象下指定模型：自由输入目录里没有的 provider/id
		openCapsule();
		fireEvent.click(screen.getByRole("button", { name: /^快速/ }));
		openCapsule();
		fireEvent.click(screen.getByRole("button", { name: /^指定模型/ }));
		fireEvent.change(screen.getByLabelText("搜索或输入 provider/模型 id"), {
			target: { value: "acme/custom-1" },
		});
		fireEvent.click(screen.getByRole("button", { name: "使用 acme/custom-1" }));
		expect(changes.at(-1)).toEqual({
			kind: "model",
			model: { provider: "acme", id: "custom-1" },
		});
	});

	it("思考强度：选中实际生效的一档，不写「默认」；选别的档对象带上 thinking", async () => {
		defaultThinking = "high";
		const changes = mount(true, {
			kind: "model",
			model: { provider: "openai", id: "gpt-x" },
		});
		openCapsule();
		const group = await screen.findByRole("radiogroup", { name: "思考级别" });
		await vi.waitFor(() =>
			expect(
				within(group)
					.getAllByRole("radio")
					.map((r) => r.textContent),
			).toEqual(["自动", "低", "高"]),
		);
		// 与 default role 同一个模型、它没有自带默认档位：就是配置的 high
		expect(within(group).getByRole("radio", { name: "高" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		fireEvent.click(within(group).getByRole("radio", { name: "低" }));
		expect(changes.at(-1)).toEqual({
			kind: "model",
			model: { provider: "openai", id: "gpt-x" },
			thinking: "low",
		});
		// 弹层不关，选中项随之更新
		expect(within(group).getByRole("radio", { name: "低" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
	});

	it("窄视口：渲染为底部抽屉（对话框），不是锚在胶囊上的弹层", async () => {
		window.innerWidth = 390;
		mount(true, { kind: "assistant" });
		openCapsule();
		const dialog = await screen.findByRole("dialog");
		expect(within(dialog).getByTestId("role-capsule-drawer")).toBeTruthy();
		expect(screen.queryByTestId("role-capsule-popover")).toBeNull();
		expect(
			within(dialog).getByRole("button", { name: /^全局助手/ }),
		).toBeInTheDocument();
	});

	it("主人的配置形状（default: '@Cyrene'，default 不在 cycleOrder）：胶囊显示昔涟，弹层直接选中昔涟，不另列「默认」", async () => {
		roleList = [
			{
				role: "Cyrene",
				name: "昔涟",
				inCycle: true,
				selector: "anthropic/claude-opus-5-5:high",
			},
			{ role: "Mare", inCycle: true, selector: "openai/gpt-lite" },
			{ role: "default", inCycle: false, selector: "@Cyrene" },
			{ role: "smol", inCycle: false },
		];
		const changes = mount(true, { kind: "role", role: "default" });
		await vi.waitFor(() =>
			expect(
				screen.getByRole("button", { name: /^对象：/ }),
			).toHaveAccessibleName("对象：昔涟"),
		);
		openCapsule();
		const row = await screen.findByRole("button", { name: /^昔涟.*Opus/ });
		// 选中项：Option 选中时带实底 bg-hover（未选中只有 hover:bg-hover）
		expect(row.className.split(/\s+/)).toContain("bg-hover");
		expect(
			screen.getByRole("button", { name: /^Mare/ }).className.split(/\s+/),
		).not.toContain("bg-hover");
		expect(screen.queryByRole("button", { name: /^默认/ })).toBeNull();
		// 不在 cycleOrder 的其它 role 不列出
		expect(screen.queryByRole("button", { name: /^快速/ })).toBeNull();
		// 点已选中的昔涟：对象不变（仍按 default 发）
		fireEvent.click(row);
		expect(changes).toEqual([]);
		// 换到 Mare 再点昔涟：才变成 Cyrene
		openCapsule();
		fireEvent.click(screen.getByRole("button", { name: /^Mare/ }));
		openCapsule();
		fireEvent.click(screen.getByRole("button", { name: /^昔涟/ }));
		expect(changes).toEqual([
			{ kind: "role", role: "Mare" },
			{ kind: "role", role: "Cyrene" },
		]);
	});

	/** 弹层里某个 role 行画的思考级别（没有图形时 undefined）。 */
	const rowLevel = (name: RegExp) =>
		screen
			.getByRole("button", { name })
			.querySelector("[data-level]")
			?.getAttribute("data-level") ?? undefined;

	it("role 行画出各自实际生效的思考级别：显式级别优先，其余跟 default 的显式级别并按模型能力收窄", async () => {
		roleList = [
			{
				role: "Cyrene",
				name: "昔涟",
				inCycle: true,
				selector: "anthropic/claude-opus-5-5:xhigh",
			},
			{ role: "Mare", inCycle: true, selector: "openai/gpt-x" },
			{ role: "default", inCycle: false, selector: "@Cyrene" },
		];
		defaultThinking = "low";
		mount(true, { kind: "role", role: "default" });
		openCapsule();
		await vi.waitFor(() => expect(rowLevel(/^昔涟/)).toBe("xhigh"));
		// 新会话从 default（@Cyrene → xhigh）起步，gpt-x 只到 high
		expect(rowLevel(/^Mare/)).toBe("high");
		// 胶囊不写级别文字，右下角角标画 default 的级别
		expect(
			document
				.querySelector("[role=img][data-level].absolute")
				?.getAttribute("data-level"),
		).toBe("xhigh");
	});

	it("default 没带显式级别：其余 role 从 config.yml 的 defaultThinkingLevel 起步", async () => {
		roleList = [
			{ role: "Mare", inCycle: true, selector: "openai/gpt-x" },
			{ role: "default", inCycle: true, selector: "anthropic/claude-opus-5-5" },
		];
		defaultThinking = "low";
		mount(true, { kind: "role", role: "default" });
		openCapsule();
		await vi.waitFor(() => expect(rowLevel(/^Mare/)).toBe("low"));
		expect(rowLevel(/^默认/)).toBe("low");
	});

	it("切到 role 的模型时沿用起始档位并按能力收窄（Core 19）：模型自带的默认档位不覆盖已有档位", async () => {
		roleList = [
			{
				role: "Mare",
				inCycle: true,
				selector: "factory-droid/claude-opus-5-5",
			},
			{ role: "Old", inCycle: true, selector: "legacy/old-x" },
			{ role: "Low", inCycle: true, selector: "openai/gpt-low" },
			{ role: "Fixed", inCycle: true, selector: "legacy/old-x:low" },
			{ role: "default", inCycle: true, selector: "openai/gpt-x" },
		];
		defaultThinking = "high";
		mount(true, { kind: "role", role: "Mare" });
		openCapsule();
		// 自带 medium，但起始档位 high 它也支持
		await vi.waitFor(() => expect(rowLevel(/^Mare/)).toBe("high"));
		// 目录不报告默认档位也推得出：起始档位有值时用不到它
		expect(rowLevel(/^Old/)).toBe("high");
		expect(rowLevel(/^Low/)).toBe("low");
		expect(rowLevel(/^Fixed/)).toBe("low");
		expect(rowLevel(/^默认/)).toBe("high");
		const group = screen.getByRole("radiogroup", { name: "思考级别" });
		expect(within(group).getByRole("radio", { name: "高" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
	});

	it("default 模型不能调节思考时会话没有档位：切过去用模型自带的默认档位，不报告默认档位的不画", async () => {
		roleList = [
			{
				role: "Mare",
				inCycle: true,
				selector: "factory-droid/claude-opus-5-5",
			},
			{ role: "Old", inCycle: true, selector: "legacy/old-x" },
			{ role: "Opus", inCycle: true, selector: "anthropic/claude-opus-5-5" },
			{ role: "default", inCycle: true, selector: "openai/gpt-lite" },
		];
		defaultThinking = "high";
		mount(true, { kind: "role", role: "Old" });
		openCapsule();
		await vi.waitFor(() => expect(rowLevel(/^Mare/)).toBe("medium"));
		expect(rowLevel(/^Old/)).toBeUndefined();
		// 已知没有自带默认档位：会话照旧没有档位
		expect(rowLevel(/^Opus/)).toBeUndefined();
		const group = screen.getByRole("radiogroup", { name: "思考级别" });
		expect(
			within(group)
				.getAllByRole("radio")
				.filter((r) => r.getAttribute("aria-checked") === "true"),
		).toEqual([]);
		expect(
			document.querySelector("[role=img][data-level].absolute"),
		).toBeNull();
	});

	it("起始档位先落到 default 模型上：全局 high、default 只支持 low，切到支持 low/high 且无自带默认的模型时沿用 low", async () => {
		roleList = [
			{ role: "Mare", inCycle: true, selector: "openai/gpt-x" },
			{ role: "default", inCycle: true, selector: "openai/gpt-low" },
		];
		defaultThinking = "high";
		mount(true, { kind: "model", model: { provider: "openai", id: "gpt-x" } });
		openCapsule();
		await vi.waitFor(() => expect(rowLevel(/^Mare/)).toBe("low"));
		expect(rowLevel(/^默认/)).toBe("low");
		const group = screen.getByRole("radiogroup", { name: "思考级别" });
		expect(within(group).getByRole("radio", { name: "低" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
		expect(within(group).getByRole("radio", { name: "高" })).toHaveAttribute(
			"aria-checked",
			"false",
		);
	});
});
