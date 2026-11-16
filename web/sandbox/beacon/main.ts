// 发布页的访客心跳（构建成单个 IIFE：dist/sandbox/beacon.js）。
//
// orynd 把它内联进发布页的每个 HTML 文件（internal/artifacts/public.go），`<script data-oryn-beat="<发布根>">`
// 给出发布根（带授权段时含授权段）。页面每 20 秒向 `<根>_oryn/beat` 发一次本页随机 id，服务端把 45 秒内
// 出现过的 id 数当作访客数。只在顶层文档里发：概览页内嵌的画框也带这段脚本，不能各算一位访客。
// 页面运行在不可信的沙箱里，这里不 import 任何应用代码。

/** 心跳间隔。 */
export const BEAT_MS = 20_000;

/** 生成本页的随机实例 id（24 位十六进制）。 */
export function visitorId(): string {
	const b = new Uint8Array(12);
	crypto.getRandomValues(b);
	return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

/** 开始心跳：立即发一次，之后每 BEAT_MS 发一次；返回停止函数。 */
export function startBeacon(
	root: string,
	send: (url: string, body: string) => void = (url, body) => {
		navigator.sendBeacon(url, body);
	},
): () => void {
	const url = new URL(`${root}_oryn/beat`, location.href).href;
	const id = visitorId();
	const beat = () => {
		try {
			send(url, id);
		} catch {
			// 心跳只是统计，发不出去就算了
		}
	};
	beat();
	const timer = setInterval(beat, BEAT_MS);
	return () => clearInterval(timer);
}

const root = document.currentScript?.getAttribute("data-oryn-beat");
if (root && window.parent === window) startBeacon(root);
