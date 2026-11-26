/**
 * 设置 → 输入与快捷键：发送键（本机偏好，真实改变 composerKeyAction 的判定，快捷键一览随之更新）、
 * 输入预测两个开关写 config.yml、快捷键一览取自清单。jsdom 不是 mac：组合键显示 Ctrl。
 */
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
	composerKeyAction,
	SEND_KEY_STORAGE,
	setSendKey,
} from "../../lib/composer";
import { readConfigDoc } from "../../lib/omp-config";
import {
	agentFetch,
	home,
	renderPage,
	resetHome,
} from "./agent/agent-config-mock";
import InputSettings from "./InputSettings";

beforeEach(() => {
	resetHome({});
	setSendKey("enter");
	localStorage.clear();
	vi.stubGlobal("fetch", agentFetch);
});

const region = (name: string) => screen.getByRole("region", { name });

/** 某个分组里标题为 `title` 的那一行（开关的读屏名称与标题同字，取第一个即标题）。 */
function row(group: string, title: string): HTMLElement {
	const li = within(region(group)).getAllByText(title)[0]?.closest("li");
	if (!li) throw new Error(`no row ${title}`);
	return li;
}

describe("发送键", () => {
	it("默认 Enter 发送；改成 Ctrl+Enter 后存进本机、Enter 换行、Ctrl+Enter 发送，快捷键一览跟着变", async () => {
		renderPage(<InputSettings />);
		const select = within(region("输入框")).getByRole("combobox", {
			name: "发送键",
		});
		expect(select).toHaveTextContent("Enter 发送");
		expect(row("快捷键", "发送")).toHaveTextContent("Enter");
		expect(row("快捷键", "换行")).toHaveTextContent("Shift+Enter");

		fireEvent.click(select);
		fireEvent.click(screen.getByRole("option", { name: "Ctrl+Enter 发送" }));

		expect(select).toHaveTextContent("Ctrl+Enter 发送");
		expect(localStorage.getItem(SEND_KEY_STORAGE)).toBe("mod");
		// 输入框不传发送键，读的就是这项偏好
		expect(composerKeyAction({ key: "Enter" }, false, null, 0).action).toBe(
			"newline",
		);
		expect(
			composerKeyAction({ key: "Enter", ctrlKey: true }, false, null, 0).action,
		).toBe("prompt");
		expect(
			composerKeyAction({ key: "Enter", metaKey: true }, true, null, 0).action,
		).toBe("steer");
		expect(row("快捷键", "发送")).toHaveTextContent("Ctrl+Enter");
		expect(row("快捷键", "换行").textContent).not.toContain("Shift");
		expect(row("快捷键", "排队，回合结束后发送")).toHaveTextContent(
			"Ctrl+Shift+Enter",
		);
	});
});

describe("输入预测", () => {
	it("两个开关写 config.yml（预测缺省开启）；关掉预测后「提供多个候选」不可用，说明里写明是 omp 配置", async () => {
		renderPage(<InputSettings />);
		const predictions = within(region("输入框")).getByRole("switch", {
			name: "预测我的下一条消息",
		});
		const alternatives = within(region("输入框")).getByRole("switch", {
			name: "提供多个候选",
		});
		await waitFor(() => expect(predictions).toBeEnabled());
		expect(predictions).toBeChecked();
		expect(alternatives).toBeEnabled();
		expect(row("输入框", "预测我的下一条消息")).toHaveTextContent(
			"omp 配置：写进 config.yml",
		);

		fireEvent.click(alternatives);
		await waitFor(() => expect(home.puts).toHaveLength(1));
		expect(readConfigDoc(home.puts[0]?.body ?? "").toJS()).toEqual({
			composer: { predictionAlternatives: true },
		});

		fireEvent.click(predictions);
		await waitFor(() => expect(home.puts).toHaveLength(2));
		expect(home.puts.map((p) => p.path)).toEqual(["config.yml", "config.yml"]);
		expect(readConfigDoc(home.puts[1]?.body ?? "").toJS()).toEqual({
			composer: { predictions: false, predictionAlternatives: true },
		});
		await waitFor(() => expect(alternatives).toBeDisabled());
		expect(row("输入框", "提供多个候选")).toHaveTextContent("先开启消息预测。");
	});
});

describe("快捷键一览", () => {
	it("应用级与线程页组合键来自清单，非 mac 显示 Ctrl；输入框按键带上忙时说明", () => {
		renderPage(<InputSettings />);
		expect(row("快捷键", "命令面板")).toHaveTextContent("Ctrl+K");
		expect(row("快捷键", "新对话")).toHaveTextContent("Ctrl+Shift+O");
		expect(row("快捷键", "收起 / 展开侧边栏")).toHaveTextContent(
			"焦点不在输入框时",
		);
		expect(
			row("快捷键", "切换分区（线程 / 助手 / 项目 / 更多）"),
		).toHaveTextContent(/Ctrl\+1.*Ctrl\+4/);
		expect(row("快捷键", "上一个角色模型")).toHaveTextContent("Ctrl+Shift+P");
		expect(row("快捷键", "中断回合")).toHaveTextContent("回合进行中");
		expect(row("快捷键", "中断回合")).toHaveTextContent("EscEsc");
	});
});
