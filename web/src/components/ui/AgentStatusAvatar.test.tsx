import { render, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { AgentStatusAvatar, type AgentVisualState } from "./AgentStatusAvatar";

const PNG = "data:image/png;base64,AA==";

function renderState(
	state: AgentVisualState,
	face?: { avatarImage?: string; avatarIcon?: string },
) {
	const { container } = render(
		<AgentStatusAvatar
			face={face}
			fallbackGlyph="S"
			state={state}
			size={20}
			title={`scout · ${state}`}
		/>,
	);
	const root = within(container).getByRole("img");
	return {
		root,
		face: root.querySelector("[data-agent-face]") as HTMLElement,
		badge: root.querySelector("[data-badge]"),
		ring: root.querySelector("[data-ring]"),
	};
}

describe("AgentStatusAvatar", () => {
	it.each([
		["running", null, "running"],
		["pending", null, "pending"],
		["done", "done", null],
		["failed", "failed", null],
		["aborted", "aborted", null],
		["archived", "archived", "archived"],
		["unknown", null, null],
	] as const)("%s：data-state、角标与外圈", (state, badge, ring) => {
		const v = renderState(state);
		expect(v.root.getAttribute("data-state")).toBe(state);
		expect(v.badge?.getAttribute("data-badge") ?? null).toBe(badge);
		expect(v.ring?.getAttribute("data-ring") ?? null).toBe(ring);
	});

	it("title 同时作 tooltip 与 aria-label", () => {
		const { root } = renderState("running");
		expect(root.getAttribute("title")).toBe("scout · running");
		expect(root.getAttribute("aria-label")).toBe("scout · running");
	});

	it("运行中光弧旋转、排队虚线呼吸、存档虚线静止", () => {
		const running = renderState("running").ring;
		expect(running?.getAttribute("class")).toContain("agent-ring-spin");
		// 减少动效时靠 `.agent-ring-arc` 把光弧补成整圈实线
		expect(running?.querySelector(".agent-ring-arc")).not.toBeNull();

		const pending = renderState("pending").ring;
		expect(pending?.getAttribute("class")).toContain("agent-ring-breathe");
		expect(
			pending?.querySelector("circle")?.getAttribute("stroke-dasharray"),
		).toBeTruthy();

		const archived = renderState("archived").ring;
		expect(archived?.getAttribute("class")).not.toMatch(
			/agent-ring-(spin|breathe)/,
		);
		expect(
			archived?.querySelector("circle")?.getAttribute("stroke-dasharray"),
		).toBeTruthy();
	});

	it.each(["failed", "aborted"] as const)("%s：头像去饱和", (state) => {
		expect(renderState(state).face.className).toContain("grayscale");
	});

	it("失败用错误色角标，中止用中性色", () => {
		expect(renderState("failed").badge?.className).toContain("bg-err");
		const aborted = renderState("aborted").badge?.className ?? "";
		expect(aborted).not.toContain("bg-err");
		expect(aborted).toContain("bg-faint");
	});

	it("存档保持原色：头像元素没有灰度 / 去饱和类", () => {
		const { face } = renderState("archived", { avatarImage: PNG });
		for (const el of [face, ...face.querySelectorAll("*")])
			expect(el.getAttribute("class") ?? "").not.toMatch(
				/grayscale|saturate|opacity-/,
			);
		expect(face.querySelector("img")?.getAttribute("src")).toBe(PNG);
	});

	it("与其他头像一致：字形头像与默认字形都经 AvatarTile 渲染", () => {
		const icon = renderState("done", { avatarIcon: "🦊" }).face;
		expect(icon.querySelector('[data-avatar="icon"]')?.textContent).toBe("🦊");
		const plain = renderState("unknown").face;
		const tile = plain.querySelector("[data-avatar-of]");
		expect(tile?.getAttribute("data-avatar")).toBe("default");
		expect(tile?.getAttribute("data-avatar-of")).toBe("agent");
		expect(tile?.textContent).toBe("S");
	});
});
