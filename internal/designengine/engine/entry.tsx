// 帧入口（M29）：每帧生成的入口模块（.oryn-build/<id>.entry.tsx）调用 mount 渲染该帧。
// 有 src/frames/_layout.tsx 时用它的默认导出包一层（props：frame、children）。
// 两个钩子，都只认同源脚本、缺文件时静默：
// - dev：先加载 ./runtime.js（dev server 的 /frames/runtime.js，即页面运行时 sandbox/runtime.js），再调用
//   __orynStart({v:1, mark:"", path:"dist/<id>.html", root:"", runtime:true, design:{frame, links}})，然后渲染；
//   design 取自入口 HTML 里的 <script id="oryn-design" type="application/json">。
// - ?__oryn_probe=1：首次渲染提交后加载 ./probe.js（探针脚本自己决定何时写结果）。
// 帧属于带主题的方案（src/variants.json，M34）时，渲染前在 <html> 上写 data-oryn-theme，方案主题的作用域规则据此生效。

import "./styles.css";
import { type ComponentType, type ReactNode, StrictMode, useEffect } from "react";
import { createRoot } from "react-dom/client";

type FrameModule = { default?: ComponentType };
type LayoutModule = {
	default?: ComponentType<{ frame: string; children?: ReactNode }>;
};

export interface MountOptions {
	/** 帧 id（src/frames/<id>.tsx 的文件名）。 */
	id: string;
	frame: FrameModule;
	layout?: LayoutModule;
	/** 帧所属方案的主题名（src/themes/<名>.css）；不属于带主题的方案时省略。 */
	theme?: string;
}

export interface MountHandle {
	/** 帧或外壳模块热替换后重渲染；未变的一方传 undefined。 */
	update(frame?: FrameModule, layout?: LayoutModule): void;
}

/** 加载同源脚本；失败（含 404）时静默返回 false。 */
function loadScript(rel: string): Promise<boolean> {
	const { promise, resolve } = Promise.withResolvers<boolean>();
	const s = document.createElement("script");
	s.src = new URL(rel, location.href).href;
	s.async = false;
	s.onload = () => resolve(true);
	s.onerror = () => {
		s.remove();
		resolve(false);
	};
	document.head.appendChild(s);
	return promise;
}

const wantsProbe =
	new URLSearchParams(location.search).get("__oryn_probe") === "1";
let probeRequested = false;

function Probe() {
	useEffect(() => {
		if (!wantsProbe || probeRequested) return;
		probeRequested = true;
		void loadScript("./probe.js");
	}, []);
	return null;
}

function Missing({ id }: { id: string }) {
	return (
		<p style={{ padding: 24, fontFamily: "system-ui" }}>
			src/frames/{id}.tsx 没有默认导出组件。
		</p>
	);
}

/** dev 入口 HTML 里引擎写入的帧信息；缺失或不合法时为 null。 */
function designInfo(): { frame: string; links: string[] } | null {
	const el = document.getElementById("oryn-design");
	if (!el?.textContent) return null;
	try {
		const v: unknown = JSON.parse(el.textContent);
		if (typeof v !== "object" || v === null) return null;
		const frame: unknown = Reflect.get(v, "frame");
		const links: unknown = Reflect.get(v, "links");
		if (typeof frame !== "string" || !Array.isArray(links)) return null;
		return { frame, links: links.filter((l) => typeof l === "string") };
	} catch {
		return null;
	}
}

async function startRuntime(id: string): Promise<void> {
	if (!(await loadScript("./runtime.js"))) return;
	const start: unknown = Reflect.get(window, "__orynStart");
	if (typeof start !== "function") return;
	start({
		v: 1,
		mark: "",
		path: `dist/${id}.html`,
		root: "",
		runtime: true,
		design: designInfo() ?? { frame: id, links: [] },
	});
}

export function mount(opts: MountOptions): MountHandle {
	let frame = opts.frame;
	let layout = opts.layout;
	const el = document.getElementById("root");
	if (!el) throw new Error("oryn design: 缺少 #root");
	const root = createRoot(el);
	if (opts.theme) document.documentElement.dataset.orynTheme = opts.theme;
	else delete document.documentElement.dataset.orynTheme;

	const render = () => {
		const Frame = frame.default;
		const Layout = layout?.default;
		const body = Frame ? <Frame /> : <Missing id={opts.id} />;
		root.render(
			<StrictMode>
				{Layout ? <Layout frame={opts.id}>{body}</Layout> : body}
				<Probe />
			</StrictMode>,
		);
	};

	if (import.meta.env.DEV) {
		void startRuntime(opts.id).finally(render);
	} else {
		render();
	}

	return {
		update(nextFrame, nextLayout) {
			if (nextFrame) frame = nextFrame;
			if (nextLayout) layout = nextLayout;
			render();
		},
	};
}
