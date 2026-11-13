/**
 * 转录里的模型名：界面上一律写短名（与输入坞的模型胶囊同一规则），完整 id 放进悬停提示。
 * 消息与 model_change 条目只带 id（可能是 `provider/id` 路径）；id 与当前会话模型相同时借用它的显示名，
 * 这样头像行、模型切换提示和输入坞写法一致（`Sonnet 5`）。
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

export function modelLabel(
	ref: string,
	provider?: string,
	known?: KnownModel,
): ModelLabel | null {
	const segs = ref.trim().split("/").filter(Boolean);
	const id = segs.pop();
	if (!id) return null;
	const same = known?.id === id;
	const prov = provider || segs.join("/") || (same ? known?.provider : "");
	const name = same && known?.name ? known.name : id;
	const path = prov ? `${prov}/${id}` : id;
	return {
		short: shortModelName(name, modelBrand(path, prov || undefined)),
		full: name === id ? path : `${name} (${path})`,
	};
}
