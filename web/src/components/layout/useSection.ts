/**
 * 分区（M9.6）的 React 接入：当前分区 = f(路由)（`sectionOf`），切分区 = 导航到该分区记住的路径（`sectionTarget`），
 * 路由变化后由 `useSectionTracker` 记下路径（存 sessionStorage，见 `uiStore.sectionPaths`）。规则全部在 `lib/sections.ts`。
 */
import { useCallback, useEffect, useLayoutEffect, useRef } from "react";
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

/**
 * `/t/<id>` 的种类还判定不了：索引尚未到达且其中没有该线程。此时线程页还不知道要不要重定向到助手 / Space，
 * 不能先记下它（会覆盖线程分区里上次真正的工作线程）。索引到达后仍没有它 = 线程页按工作线程显示，照常记。
 */
function kindPending(path: string): boolean {
	const m = /^\/t\/([^/?#]+)\/?(?:[?#]|$)/.exec(path);
	if (!m?.[1]) return false;
	const s = indexStore.getState();
	return !s.loaded && s.threads[m[1]]?.kind === undefined;
}

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
 * 「此刻在哪个分区」的依据（所有 useSwitchSection 共享）：`committed` = 最近一次提交的路由路径（布局副作用里更新）；
 * `pending` = 提交之后发出的最近一次分区导航的目标。路由更新走 transition：目标页渲染慢（懒加载、四个分区 pane
 * 一起渲染）时 URL 已经变了、树还没提交，其间旧树的紧急重渲染（store 更新）拿到的仍是旧路径。若按它判断，连按
 * Ctrl+2…4 再按 Ctrl+1 会被当成「已在线程分区」而丢掉。有 pending 以它为准；已提交路径一变（含点链接）就清掉。
 */
let committed: string | undefined;
let pending: string | null = null;

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
	useLayoutEffect(() => {
		committed = pathname;
		pending = null;
	}, [pathname]);
	const nav = useRef(navigate);
	nav.current = navigate;
	return useCallback((section, opts) => {
		const here = pending ?? committed ?? "/";
		if (sectionOf(here) === section) {
			const ui = uiStore.getState();
			if (opts?.toggleSink && ui.sidebarOpen && isSinkLayout())
				ui.setSidebar(false);
			return;
		}
		const to = sectionTarget(
			section,
			uiStore.getState().sectionPaths[section],
			kindOf,
		);
		pending = to;
		nav.current(to);
	}, []);
}

/**
 * 路由变化后记下该分区的路径。延后一个宏任务（setTimeout 0）并在路由再次变化时取消：同一拍里的重定向
 * （`/assistant` → `/assistant/<id>`、`/t/<助手对话>` → `/assistant/<id>`、`/projects/<id>/settings` → `…/settings/overview`）不留痕；
 * `settles` 为假的（索引里已知是协调对话的 `/t/<id>`）不记。冷启动时 `/t/<id>` 的种类要等索引到达才判定
 * （`kindPending`）：订阅索引，判定后再按 `settles` 决定；期间路由变化则取消。
 */
export function useSectionTracker(): void {
	const { pathname, search } = useLocation();
	const path = pathname + search;
	useEffect(() => {
		let unsubscribe: (() => void) | undefined;
		const note = () => {
			if (!settles(path, kindOf)) return;
			uiStore.getState().noteSectionPath(sectionOf(path), path);
		};
		const timer = setTimeout(() => {
			if (!kindPending(path)) return note();
			unsubscribe = indexStore.subscribe(() => {
				if (kindPending(path)) return;
				unsubscribe?.();
				unsubscribe = undefined;
				note();
			});
		}, 0);
		return () => {
			clearTimeout(timer);
			unsubscribe?.();
		};
	}, [path]);
}
