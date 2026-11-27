// 不透明源里的 worker：帧与发布页没有 allow-same-origin，三种常见写法各有一处过不去——
// - `new Worker(同站地址)` 过不了同源检查；
// - `data:` worker 不在帧策略的 worker-src 里；
// - Chromium 不让不透明源起 module worker（blob: 的也不行，WebKit 可以），classic worker 照常。
// 垫片一律换成 classic 的 blob worker：classic 脚本同站的写 `importScripts("<绝对地址>")`、`data:` 的原样搬进 blob；
// module 脚本由引导脚本 `import("<地址>")` 动态加载（classic worker 里的动态 import 两个引擎都支持），模块求值完之前
// 收到的消息先存着、求值完按原顺序重发，模块自己注册的 message 监听不会漏掉初始化消息。blob worker 继承页面的 CSP 与
// `Origin: null`，服务端对 `Origin: null` 的请求回 `Access-Control-Allow-Origin: null`
// （internal/artifacts/policy.go 的 setFrameHeaders）。

type WorkerHost = { Worker: typeof Worker; location: Location };

/** 把 `data:` 地址解码成 Blob；格式不对时返回 null。 */
function dataToBlob(href: string): Blob | null {
	const comma = href.indexOf(",");
	if (comma < 0) return null;
	const meta = href.slice(5, comma);
	const body = href.slice(comma + 1);
	const type = meta.split(";")[0] || "text/javascript";
	try {
		if (meta.endsWith(";base64")) {
			const bin = atob(body);
			const bytes = new Uint8Array(bin.length);
			for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
			return new Blob([bytes], { type });
		}
		return new Blob([decodeURIComponent(body)], { type });
	} catch {
		return null;
	}
}

function scriptBlob(src: string): string {
	return URL.createObjectURL(new Blob([src], { type: "text/javascript" }));
}

/** module worker 的 classic 引导脚本：动态 import 模块，求值完之前的消息先存着再按序重发。 */
export function moduleBootstrap(url: string): string {
	return (
		"(()=>{const q=[];const hold=(e)=>{e.stopImmediatePropagation();q.push(e)};" +
		'self.addEventListener("message",hold);' +
		`import(${JSON.stringify(url)}).then(()=>{self.removeEventListener("message",hold);` +
		'for(const e of q)self.dispatchEvent(new MessageEvent("message",{data:e.data,ports:[...e.ports]}))},' +
		"(e)=>{setTimeout(()=>{throw e})})})();"
	);
}

/**
 * worker 的脚本地址换成 classic blob worker 的地址。classic 的 blob 地址原样返回；
 * 地址解析不了时原样返回（交给浏览器报错）。
 */
export function workerScriptURL(
	url: string | URL,
	options: WorkerOptions | undefined,
	base: string,
): string | URL {
	let href = url instanceof URL ? url.href : String(url);
	const module = options?.type === "module";
	if (href.startsWith("data:")) {
		const blob = dataToBlob(href);
		if (!blob) return url;
		href = URL.createObjectURL(blob);
		if (!module) return href;
	} else if (href.startsWith("blob:")) {
		if (!module) return url;
	} else {
		try {
			href = new URL(href, base).href;
		} catch {
			return url;
		}
		if (!module) return scriptBlob(`importScripts(${JSON.stringify(href)});`);
	}
	return scriptBlob(moduleBootstrap(href));
}

/** 把 win.Worker 换成改写脚本地址、一律以 classic 方式启动的子类（只装一次）。 */
export function installWorkerShim(win: WorkerHost): void {
	const Native = win.Worker;
	if (typeof Native !== "function" || "orynShim" in Native) return;
	const location = win.location;
	class OrynWorker extends Native {
		static orynShim = true;
		constructor(url: string | URL, options?: WorkerOptions) {
			super(workerScriptURL(url, options, location.href), {
				...options,
				type: "classic",
			});
		}
	}
	win.Worker = OrynWorker;
}
