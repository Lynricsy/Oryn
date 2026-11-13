/**
 * 模型能力缓存：omp 只在会话里报告某个模型支持哪些思考级别（`availableThinkingLevels`）。
 * 每次收到会话状态就按 `provider/id` 记下来，存本机（localStorage `oryn.modelCaps`），
 * 首页新线程还没有会话时据此列出可选级别；没见过的模型就是「未知」，不假装全部可用。
 */
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { SessionState } from "../omp/types";

export const MODEL_CAPS_KEY = "oryn.modelCaps";

export interface ModelCaps {
	/** 支持的思考级别（不含 auto）；空数组表示模型不推理 */
	thinking: string[];
}

type CapsMap = Record<string, ModelCaps>;

function load(): CapsMap {
	try {
		const raw = globalThis.localStorage?.getItem(MODEL_CAPS_KEY);
		const v: unknown = raw ? JSON.parse(raw) : null;
		return v && typeof v === "object" && !Array.isArray(v)
			? (v as CapsMap)
			: {};
	} catch {
		return {};
	}
}

export const modelCapsStore = createStore<{ caps: CapsMap }>(() => ({
	caps: load(),
}));

/** 模型选择器里的写法（`provider/id`，可带 omp 的 `:级别` 后缀）→ 缓存键 */
export function modelCapsKey(model: string): string {
	return model.trim().replace(/:[^/]*$/, "");
}

/** 从一份会话状态记下当前模型的能力；与已记的相同时不写。 */
export function recordModelCaps(state: SessionState): void {
	const model = state.model;
	const levels = state.availableThinkingLevels;
	if (!model || !levels) return;
	const key = `${model.provider}/${model.id}`;
	const thinking = model.reasoning === false ? [] : levels.map(String);
	const prev = modelCapsStore.getState().caps[key];
	if (prev && prev.thinking.join() === thinking.join()) return;
	const caps = { ...modelCapsStore.getState().caps, [key]: { thinking } };
	modelCapsStore.setState({ caps });
	try {
		globalThis.localStorage?.setItem(MODEL_CAPS_KEY, JSON.stringify(caps));
	} catch {
		// 存不下（隐私模式、配额）时只留在内存里
	}
}

/** 某个模型的已知能力；没见过或模型为空时是 undefined。 */
export function useModelCaps(model: string): ModelCaps | undefined {
	const key = modelCapsKey(model);
	return useStore(modelCapsStore, (s) => (key ? s.caps[key] : undefined));
}
