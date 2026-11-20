/**
 * 上下文菜单的登记表：组件用 `useContextTarget` 把 spec 挂到自己的元素上，分发器从事件目标向上收集。
 * 用 WeakMap 存，元素卸载后自动释放；spec 存在 ref 里，回调读到的永远是最新 props。
 */
import { useCallback, useRef } from "react";
import { anchorOf } from "./selection";
import { selectionGroups } from "./selection-groups";
import type { ContextRequest, ContextTargetSpec, MenuGroup } from "./types";

const targets = new WeakMap<Element, { current: ContextTargetSpec }>();

export function useContextTarget<T extends Element = HTMLElement>(
	spec: ContextTargetSpec,
): (el: T | null) => void {
	const specRef = useRef(spec);
	specRef.current = spec;
	const elRef = useRef<T | null>(null);
	return useCallback((el: T | null) => {
		if (elRef.current === el) return;
		if (elRef.current && targets.get(elRef.current) === specRef)
			targets.delete(elRef.current);
		elRef.current = el;
		if (el) targets.set(el, specRef);
	}, []);
}

function* specsFrom(from: Element | null): Generator<ContextTargetSpec> {
	for (let el = from; el; el = el.parentElement) {
		const spec = targets.get(el)?.current;
		if (spec) yield spec;
	}
}

/** 从 `from` 向上找第一个声明了 `askThreadId` 的登记；没有则为 null。 */
export function resolveAskThread(from: Element | null): string | null {
	for (const spec of specsFrom(from))
		if (spec.askThreadId !== undefined) return spec.askThreadId;
	return null;
}

export function resolveContext(base: Omit<ContextRequest, "askThreadId">): {
	groups: MenuGroup[];
	suppress: boolean;
} {
	const req: ContextRequest = {
		...base,
		askThreadId: resolveAskThread(base.target),
	};
	let selection: MenuGroup[] = [];
	if (req.selection) {
		const sel = req.selection;
		let claimed = false;
		for (const spec of specsFrom(sel.root)) {
			if (!spec.selection) continue;
			claimed = true;
			const ctx = spec.selection(sel, req);
			if (ctx) selection = selectionGroups(ctx);
			break;
		}
		if (!claimed)
			selection = selectionGroups({
				threadId: null,
				text: sel.text,
				before: "",
				after: "",
				source: { kind: "other" },
				anchor: anchorOf(sel, req.x, req.y),
			});
	}
	let objects: MenuGroup[] = [];
	for (const spec of specsFrom(req.target)) {
		const got = spec.items?.(req);
		if (got === "suppress") return { groups: [], suppress: true };
		if (got) objects.push(...got);
	}
	// 用户此刻操作的是选区：对象组只留链接与图片这类和选区并存的
	if (selection.length > 0)
		objects = objects.filter((g) => g.key === "link" || g.key === "image");
	const seen = new Set<string>();
	const groups: MenuGroup[] = [];
	for (const g of [...selection, ...objects]) {
		if (seen.has(g.key) || g.items.length === 0) continue;
		seen.add(g.key);
		groups.push(g);
	}
	return { groups, suppress: false };
}
