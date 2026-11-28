/**
 * 转录的入场动画由谁播放（样式见 index.css 的 `.tx-enter`）：只给「此刻新到达」的内容，
 * 历史条目、首轮补发、加载更早的消息与虚拟列表重新挂载的行都不播放。
 * - 行：`ArrivalLog` 记下哪些行键是追加在末尾的新行与到达时刻，行在 ENTER_WINDOW 内挂载才带入场类；
 * - 块：流式助手消息里新出现的思考 / 正文 / 工具卡在挂载时入场（MessageView 的 FlowBlock），
 *   是否启用由 `EnterArmedContext` 给出（转录挂载后的 INTRO_MS 内不启用）。
 */
import { createContext } from "react";

/** 转录挂载后这段时间内不播放入场、跟随滚动直接到位：首轮补发与测高纠偏不该读成一段动画。 */
export const INTRO_MS = 800;
/** 入场标记的有效期（ms），长于动画本身：过期后虚拟列表重新挂载这一行不再重播。 */
export const ENTER_WINDOW = 700;
/** 一次最多几行逐行入场：一次并入更多行（重连补发、切换分支）是整体刷新，不做入场。 */
const MAX_FRESH = 4;

/** 流式块的入场是否启用（转录外的消息渲染默认不启用）。 */
export const EnterArmedContext = createContext(false);

export class ArrivalLog<R extends { key: string }> {
	private rows: readonly R[] | null = null;
	private readonly known = new Set<string>();
	private readonly at = new Map<string, number>();

	/** 行的身份键，须与虚拟列表的行键一致（接替在途消息的条目沿用在途行键，不算新行）。 */
	private readonly keyOf: (row: R) => string;

	constructor(keyOf: (row: R) => string = (row) => row.key) {
		this.keyOf = keyOf;
	}

	/**
	 * 登记这一版显示行（同一个数组只处理一次，渲染期重复调用无副作用）。
	 * `armed` 为假时只记下行键；`animate` 决定一行新行是否自己入场（交接行——在途消息落盘、
	 * 待发气泡换成真实 user 行——画面上早已在场，不再入场）。
	 */
	note(
		rows: readonly R[],
		armed: boolean,
		animate: (row: R) => boolean,
		now: number,
	): void {
		if (rows === this.rows) return;
		this.rows = rows;
		let lastKnown = -1;
		const fresh: R[] = [];
		rows.forEach((row, i) => {
			if (this.known.has(this.keyOf(row))) lastKnown = i;
			else fresh.push(row);
		});
		for (const row of fresh) this.known.add(this.keyOf(row));
		for (const [key, t] of this.at)
			if (now - t >= ENTER_WINDOW) this.at.delete(key);
		if (!armed || fresh.length === 0 || fresh.length > MAX_FRESH) return;
		// 只认追加在末尾的新行：插在已知行之间的（加载更早、重排）不入场
		for (const row of fresh)
			if (rows.indexOf(row) > lastKnown && animate(row))
				this.at.set(this.keyOf(row), now);
	}

	entering(key: string, now: number): boolean {
		const t = this.at.get(key);
		return t !== undefined && now - t < ENTER_WINDOW;
	}
}
