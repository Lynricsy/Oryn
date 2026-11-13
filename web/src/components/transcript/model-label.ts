/**
 * 转录里的模型名：界面上一律写短名（与输入坞的模型胶囊同一规则），完整 id 放进悬停提示。
 * 两种输入形状：助手消息带原始模型 id 与单独的 provider 字段（id 本身可能带命名空间，如 `qwen/qwen3-max`）；
 * model_change 条目是 `provider/modelId` 路径（只有第一段是 provider）。
 * id 与当前会话模型相同时借用它的显示名，这样头像行、模型切换提示和输入坞写法一致（`Sonnet 5`）。
 */
import { modelBrand, shortModelName } from "../../lib/model-brand";

export interface ModelLabel {
	/** 界面上显示的短名 */
	short: string;
	/** 悬停提示：显示名 +（provider/id） */
	full: string;
}

/** 当前会话模型（omp session state 的 `model`）里用到的字段。 */
export interface KnownModel {
	id: string;
	name?: string;
	provider?: string;
}

/** `id`：原始模型 id（整段都是 id）+ 可选 provider；`path`：`provider/modelId`。 */
export type ModelRef = { id: string; provider?: string } | { path: string };

export function modelLabel(
	ref: ModelRef,
	known?: KnownModel,
): ModelLabel | null {
	let id: string;
	let provider: string;
	if ("path" in ref) {
		const path = ref.path.trim();
		const slash = path.indexOf("/");
		provider = slash < 0 ? "" : path.slice(0, slash);
		id = slash < 0 ? path : path.slice(slash + 1);
	} else {
		id = ref.id.trim();
		provider = ref.provider?.trim() ?? "";
	}
	if (!id) return null;
	const same = known?.id === id;
	const prov = provider || (same ? (known?.provider ?? "") : "");
	const name = same && known?.name ? known.name : id;
	const path = prov ? `${prov}/${id}` : id;
	return {
		short: shortModelName(name, modelBrand(path, prov || undefined)),
		full: name === id ? path : `${name} (${path})`,
	};
}
