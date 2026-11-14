/**
 * 分区（M9.6）的 React 接入：当前分区 = f(路由)（`sectionOf`），切分区 = 导航到该分区记住的路径（`sectionTarget`），
 * 路由变化后由 `useSectionTracker` 记下路径（存 sessionStorage，见 `uiStore.sectionPaths`）。规则全部在 `lib/sections.ts`。
 */
import { useCallback, useEffect, useRef } from "react";
import { useLocation, useNavigate } from "react-router";
import {
	type SectionId,
	sectionOf,
	sectionTarget,
	settles,
	type ThreadKindOf,
} from "../../lib/sections";
import { breakpointFor, readSegments } from "../../lib/viewport";
import { indexStore } from "../../state/stores";
import { uiStore } from "../../state/ui";

/** 线程种类一律取索引（索引里还没有的线程视为未知）。 */
const kindOf: ThreadKindOf = (id) => indexStore.getState().threads[id]?.kind;

/** 手机下沉侧栏布局：compact 且不是双段视口（双段时侧栏是悬浮抽屉）。 */
export function isSinkLayout(): boolean {
	return (
		breakpointFor(window.innerWidth) === "compact" &&
		readSegments(window) === null
	);
}

export function useSection(): SectionId {
	return sectionOf(useLocation().pathname);
}

/**
 * 切到某分区：已在该分区时不导航（`toggleSink` 且此刻是打开着的手机下沉侧栏 → 收起它）；
 * 否则导航到 `sectionTarget(...)`（push，不 replace：下沉侧栏的历史标记要留在栈里，返回键才先收侧栏）。
 */
export function useSwitchSection(): (
	section: SectionId,
	opts?: { toggleSink?: boolean },
) => void {
	const navigate = useNavigate();
	const { pathname } = useLocation();
	const here = useRef(pathname);
	here.current = pathname;
	const nav = useRef(navigate);
	nav.current = navigate;
	return useCallback((section, opts) => {
		if (sectionOf(here.current) === section) {
			const ui = uiStore.getState();
			if (opts?.toggleSink && ui.sidebarOpen && isSinkLayout())
				ui.setSidebar(false);
			return;
		}
		nav.current(
			sectionTarget(section, uiStore.getState().sectionPaths[section], kindOf),
		);
	}, []);
}

/**
 * 路由变化后记下该分区的路径。延后一个宏任务（setTimeout 0）并在路由再次变化时取消：同一拍里的重定向
 * （`/assistant` → `/assistant/<id>`、`/t/<助手对话>` → `/assistant/<id>`、`/projects` → `/projects/<id>`）不留痕；
 * `settles` 为假的（索引里已知是协调对话的 `/t/<id>`）不记。
 */
export function useSectionTracker(): void {
	const { pathname, search } = useLocation();
	const path = pathname + search;
	useEffect(() => {
		const timer = setTimeout(() => {
			if (!settles(path, kindOf)) return;
			uiStore.getState().noteSectionPath(sectionOf(path), path);
		}, 0);
		return () => clearTimeout(timer);
	}, [path]);
}
