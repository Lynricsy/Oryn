/**
 * 配对二维码（M7 计划 4.8 主人追加）：「设置 → 设备」显示 `oryn://pair?server=<encoded origin>`，
 * 手机 App 的连接页扫码后只填服务器地址（批准仍要在电脑上输入配对码）。
 * 二维码由 `uqr` 编码，这里只把模块矩阵合并成横向连续段，便于渲染成少量 SVG `<rect>`。
 */
import { encode } from "uqr";

const PAIR_PREFIX = "oryn://pair";

/** 二维码内容：`oryn://pair?server=<encodeURIComponent(origin)>`。 */
export function pairUri(origin: string): string {
	return `${PAIR_PREFIX}?server=${encodeURIComponent(origin)}`;
}

/** 扫到的内容里的服务器地址：`oryn://pair?server=` 取 server 参数，其余原样返回（普通地址）。 */
export function scannedServer(content: string): string {
	const s = content.trim();
	if (!s.toLowerCase().startsWith(PAIR_PREFIX)) return s;
	try {
		return new URL(s).searchParams.get("server") ?? s;
	} catch {
		return s;
	}
}

/** 一行里连续的深色模块：从 (x, y) 起宽 w 个模块。 */
export interface QrRun {
	x: number;
	y: number;
	w: number;
}

export interface QrModules {
	/** 码区边长（模块数，不含静区）。 */
	size: number;
	runs: QrRun[];
}

/** 编码并把每行连续的深色模块合并成段；不含静区，渲染时自己留白。 */
export function qrModules(text: string): QrModules {
	const { data, size } = encode(text, { border: 0 });
	const runs: QrRun[] = [];
	data.forEach((row, y) => {
		let start = -1;
		for (let x = 0; x <= row.length; x++) {
			const on = x < row.length && row[x] === true;
			if (on && start < 0) start = x;
			else if (!on && start >= 0) {
				runs.push({ x: start, y, w: x - start });
				start = -1;
			}
		}
	});
	return { size, runs };
}
