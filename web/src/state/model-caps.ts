/**
 * 模型能力：思考档位取中央模型目录（`api/model-catalog-queries.ts`）的 `thinking` 字段，
 * 首页新线程、定时对话框还没有会话时据此列出可选级别。目录里没有的模型（或目录还没读到）就是「未知」，
 * 不假装全部可用。
 */
import { useMemo } from "react";
import {
	findCatalogModel,
	type ModelCatalog,
	useModelCatalog,
} from "../api/model-catalog-queries";

export interface ModelCaps {
	/** 支持的思考级别（不含 auto、off）；空数组表示不能调节思考 */
	thinking: string[];
	/**
	 * 模型自带的默认思考档位（新会话起始优先于 defaultThinkingLevel；切模型时只在会话没有档位时用）：字符串 = 有，
	 * null = 已知没有，undefined = 目录来自不报告这一项的旧版 Core，未知
	 */
	defaultLevel: string | null | undefined;
}

/** 目录里某个模型的能力；目录还没读到、模型为空或目录里没有时是 undefined。 */
export function modelCapsOf(
	catalog: ModelCatalog | undefined,
	model: string,
): ModelCaps | undefined {
	const m = findCatalogModel(catalog, model);
	if (!m) return undefined;
	return {
		thinking: m.thinking ?? [],
		defaultLevel:
			m.thinkingDefault === undefined ? undefined : m.thinkingDefault || null,
	};
}

/** 某个模型的已知能力（读中央目录）。 */
export function useModelCaps(model: string): ModelCaps | undefined {
	const { data } = useModelCatalog();
	return useMemo(() => modelCapsOf(data, model), [data, model]);
}
