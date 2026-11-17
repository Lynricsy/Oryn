/**
 * 输入区的下一条消息预测（omp `composer.predictions`，帧 `oryn_composer_prediction`），显示规则同 omp TUI 编辑器：
 * - 幽灵字：草稿是某个候选的严格前缀（区分大小写）时，取第一个这样的候选，草稿之后显示它剩下的部分。
 *   只在草稿为单行、光标在草稿末尾时显示；菜单、输入法、agent 运行中等由调用方通过 `enabled` 排除。
 * - 轮换（2 个以上候选）：草稿等于刚从预测插入的那个候选时，不再用更长的候选延伸它，Tab 换成下一个候选（循环）。
 *   插入后的任何其他编辑都会清掉「刚插入」的记录（由调用方维护 `inserted`）。
 * 服务端看不到草稿，是否显示全由这里按草稿决定。
 */

export interface PredictionInput {
	/** 候选，按可能性从高到低 */
	choices: readonly string[];
	draft: string;
	/** 光标位置（selectionStart）；不小于草稿长度即视为在末尾（草稿被程序清空 / 缩短后光标贴到末尾） */
	caret: number;
	/** 菜单未打开、非输入法组字、agent 空闲等外部条件都满足 */
	enabled: boolean;
	/** 最近一次从预测插入的文本；之后有其他编辑时为 null */
	inserted: string | null;
}

export interface PredictionGhost {
	/** 接受后草稿变成的完整候选 */
	choice: string;
	/** 草稿之后显示的灰色部分 */
	suffix: string;
}

export interface PredictionCycle {
	/** 当前草稿在候选里的序号（0 起） */
	index: number;
	total: number;
	/** Tab 换成的下一个候选 */
	next: string;
}

export interface PredictionView {
	ghost: PredictionGhost | null;
	cycle: PredictionCycle | null;
}

const NONE: PredictionView = { ghost: null, cycle: null };

export function predictionView(input: PredictionInput): PredictionView {
	const { choices, draft, caret, enabled, inserted } = input;
	if (!enabled || choices.length === 0) return NONE;
	// 刚插入的候选：不再延伸，有多个候选时可轮换
	if (inserted !== null && draft === inserted) {
		const index = choices.indexOf(draft);
		if (index >= 0) {
			if (choices.length < 2) return NONE;
			return {
				ghost: null,
				cycle: {
					index,
					total: choices.length,
					next: choices[(index + 1) % choices.length] ?? draft,
				},
			};
		}
	}
	if (draft.includes("\n") || caret < draft.length) return NONE;
	const choice = choices.find(
		(c) => c.length > draft.length && c.startsWith(draft),
	);
	if (choice === undefined) return NONE;
	return { ghost: { choice, suffix: choice.slice(draft.length) }, cycle: null };
}
