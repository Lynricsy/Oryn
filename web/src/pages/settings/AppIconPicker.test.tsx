import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	type AppIconHost,
	type AppIconState,
	host,
	type MobileHost,
} from "../../lib/host";
import { AppIconPicker } from "./AppIconPicker";

const h = host as { mobile: MobileHost | null };

function withIcons(api: AppIconHost | null) {
	h.mobile = { appIcon: api } as unknown as MobileHost;
}

function deferred() {
	let resolve!: () => void;
	let reject!: (e: unknown) => void;
	const p = new Promise<void>((res, rej) => {
		resolve = res;
		reject = rej;
	});
	return { p, resolve, reject };
}

describe("AppIconPicker", () => {
	let state: AppIconState;
	beforeEach(() => {
		state = {
			supported: true,
			current: "default",
			options: ["default", "accent"],
		};
	});
	afterEach(() => {
		h.mobile = null;
	});

	it("宿主不支持（非 iOS 壳 / 旧壳）时不渲染", () => {
		withIcons(null);
		const { container } = render(<AppIconPicker />);
		expect(container).toBeEmptyDOMElement();
	});

	it("壳只带默认图标、或系统不允许换时不渲染", async () => {
		const get = vi.fn(async () => ({ ...state, options: ["default"] }));
		withIcons({ get, set: vi.fn() });
		const { container } = render(<AppIconPicker />);
		await act(async () => {});
		expect(get).toHaveBeenCalled();
		expect(container).toBeEmptyDOMElement();
	});

	it("选中当前图标；换成品牌色要等系统应答才改选中，期间不能再点", async () => {
		const pending = deferred();
		const set = vi.fn(() => pending.p);
		withIcons({ get: async () => state, set });
		render(<AppIconPicker />);
		const mono = await screen.findByRole("radio", { name: "黑白" });
		const accent = screen.getByRole("radio", { name: "品牌色" });
		expect(mono).toHaveAttribute("aria-checked", "true");

		fireEvent.click(accent);
		expect(set).toHaveBeenCalledWith("accent");
		expect(accent).toHaveAttribute("aria-checked", "false");
		expect(mono).toBeDisabled();

		await act(async () => pending.resolve());
		expect(accent).toHaveAttribute("aria-checked", "true");
		expect(mono).toHaveAttribute("aria-checked", "false");
		expect(mono).not.toBeDisabled();
	});

	it("系统拒绝时保持原图标并提示原因", async () => {
		const set = vi.fn(async () => {
			throw new Error("资源不存在");
		});
		withIcons({ get: async () => state, set });
		render(<AppIconPicker />);
		fireEvent.click(await screen.findByRole("radio", { name: "品牌色" }));
		expect(
			await screen.findByText(/图标没有换成：资源不存在/),
		).toBeInTheDocument();
		expect(screen.getByRole("radio", { name: "黑白" })).toHaveAttribute(
			"aria-checked",
			"true",
		);
	});
});
