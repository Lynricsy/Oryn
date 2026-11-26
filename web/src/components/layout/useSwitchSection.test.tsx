/**
 * useSwitchSection：导航已发出（URL 已变）但路由树还没提交新路径时（transition 挂起，目标页渲染慢），
 * 再切回原分区不能被当成「已在该分区」丢掉（M9.6 验收 R6：连按 Ctrl+2…4 再按 Ctrl+1 无效）。
 * navigate 换成只记录、不改路由的替身，精确模拟「提交还没发生」。
 */
import { act, render } from "@testing-library/react";
import type * as ReactRouter from "react-router";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SectionId } from "../../lib/sections";
import { uiStore } from "../../state/ui";
import { useSwitchSection } from "./useSection";

const navigated: string[] = [];

vi.mock("react-router", async (importOriginal) => ({
	...(await importOriginal<typeof ReactRouter>()),
	useNavigate: () => (to: string) => {
		navigated.push(to);
	},
}));

let sw: (s: SectionId) => void = () => undefined;
function Switcher() {
	sw = useSwitchSection();
	return null;
}

beforeEach(() => {
	navigated.length = 0;
	uiStore.setState({
		sectionPaths: {
			threads: "/t/W1",
			assistant: "/assistant/A1",
			projects: "/projects/P1",
			more: "/settings/secrets",
		},
	});
});

describe("useSwitchSection：导航还没提交", () => {
	it("连按 2、3、4、1：每一下都导航，1 回到线程分区记住的路径", () => {
		render(
			<MemoryRouter initialEntries={["/t/W1"]}>
				<Switcher />
			</MemoryRouter>,
		);
		act(() => {
			sw("assistant");
			sw("projects");
			sw("more");
			sw("threads");
		});
		expect(navigated).toEqual([
			"/assistant/A1",
			"/projects/P1",
			"/settings/secrets",
			"/t/W1",
		]);
	});

	it("已提交的就是该分区、之后没有发出过导航：不导航", () => {
		render(
			<MemoryRouter initialEntries={["/settings/account"]}>
				<Switcher />
			</MemoryRouter>,
		);
		act(() => sw("more"));
		expect(navigated).toEqual([]);
	});

	it("连按同一个分区两次：第二次视为已在该分区", () => {
		render(
			<MemoryRouter initialEntries={["/t/W1"]}>
				<Switcher />
			</MemoryRouter>,
		);
		act(() => {
			sw("assistant");
			sw("assistant");
		});
		expect(navigated).toEqual(["/assistant/A1"]);
	});
});
