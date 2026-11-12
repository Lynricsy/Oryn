/**
 * 终端输出续接（计划 M3 2.5）：omp 的 `terminal_output.seq` = 该终端累计发出的字符数（含本帧 `data`），
 * `terminal_snapshot.seq` 覆盖快照 `data` 的末尾。据此判断一帧是旧帧、与已写内容部分重叠、首尾相接还是中间有缺口。
 */
export interface OutputChunk {
	seq: number;
	data: string;
}

export type ApplyResult = { write: string; lastSeq: number } | "drop" | "gap";

/** `lastSeq` 为本地已写到的位置；返回要写入 xterm 的部分与新位置，或 `drop`（全部已写过）/ `gap`（缺帧，须重同步）。 */
export function applyOutput(lastSeq: number, chunk: OutputChunk): ApplyResult {
	const start = chunk.seq - chunk.data.length;
	if (chunk.seq <= lastSeq) return "drop";
	if (start === lastSeq) return { write: chunk.data, lastSeq: chunk.seq };
	if (start < lastSeq)
		return { write: chunk.data.slice(lastSeq - start), lastSeq: chunk.seq };
	return "gap";
}
