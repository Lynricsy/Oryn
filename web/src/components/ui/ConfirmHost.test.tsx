/**
 * 应用内确认的宿主：标题 / 说明 / 动作词、初始焦点在「取消」、Esc 与取消结算为 false、确认为 true，
 * 新请求顶掉还开着的旧请求时旧的按取消结算。
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { confirmAction } from "../../lib/confirm";
import { ConfirmHost } from "./ConfirmHost";

describe("ConfirmHost", () => {
	it("显示问句、说明与动作词；初始焦点在取消；确认结算为 true", async () => {
		render(<ConfirmHost />);
		let result: Promise<boolean> = Promise.resolve(false);
		act(() => {
			result = confirmAction({
				title: "删除这个线程？",
				body: "转录和它独占的环境会一并删除。",
				action: "删除",
				danger: true,
			});
		});
		const dialog = screen.getByRole("dialog", { name: "删除这个线程？" });
		expect(dialog).toHaveTextContent("转录和它独占的环境会一并删除。");
		expect(screen.getByTestId("confirm-ok")).toHaveTextContent("删除");
		expect(screen.getByTestId("confirm-cancel")).toHaveFocus();
		await act(async () => {
			fireEvent.click(screen.getByTestId("confirm-ok"));
		});
		await expect(result).resolves.toBe(true);
		expect(screen.queryByRole("dialog")).toBeNull();
	});

	it("Esc 与取消都结算为 false", async () => {
		render(<ConfirmHost />);
		let first: Promise<boolean> = Promise.resolve(true);
		act(() => {
			first = confirmAction({ title: "归档？", action: "归档" });
		});
		await act(async () => {
			fireEvent.keyDown(window, { key: "Escape" });
		});
		await expect(first).resolves.toBe(false);

		let second: Promise<boolean> = Promise.resolve(true);
		act(() => {
			second = confirmAction({ title: "归档？", action: "归档" });
		});
		await act(async () => {
			fireEvent.click(screen.getByTestId("confirm-cancel"));
		});
		await expect(second).resolves.toBe(false);
	});

	it("新请求顶掉还开着的旧请求：旧的按取消结算，对话框换成新的", async () => {
		render(<ConfirmHost />);
		let older: Promise<boolean> = Promise.resolve(true);
		let newer: Promise<boolean> = Promise.resolve(false);
		act(() => {
			older = confirmAction({ title: "旧的？", action: "删除" });
		});
		act(() => {
			newer = confirmAction({ title: "新的？", action: "清除" });
		});
		await expect(older).resolves.toBe(false);
		expect(screen.getByRole("dialog", { name: "新的？" })).toBeInTheDocument();
		await act(async () => {
			fireEvent.click(screen.getByTestId("confirm-ok"));
		});
		await expect(newer).resolves.toBe(true);
	});
});
