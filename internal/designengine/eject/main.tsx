// 导出工程的入口（Oryn 设计画布导出）：一个页面里用 hash 路由切换全部帧。
//   #/<id>      显示 src/frames/<id>.tsx 的默认导出（有 src/frames/_layout.tsx 时用它包一层，props：frame、children）
//   #/ 或空     显示起始帧（oryn-frames.json 的 start，缺省为第一帧）
//   #/_frames   帧列表
// 帧的顺序与名字取自 oryn-frames.json（导出时按画布写入）；之后新加的帧文件排在后面，名字取 export const frame.name。
// 帧间跳转用 import { Link } from "oryn:design"（vite.config.ts 把它指向 ./oryn-design.tsx）。

import "./styles.css";
import {
	type ComponentType,
	type ReactNode,
	StrictMode,
	useEffect,
	useSyncExternalStore,
} from "react";
import { createRoot } from "react-dom/client";
import meta from "./oryn-frames.json";

type FrameModule = {
	default?: ComponentType;
	frame?: { name?: unknown };
};
type LayoutModule = {
	default?: ComponentType<{ frame: string; children?: ReactNode }>;
};
type FrameInfo = { id: string; name: string; Component?: ComponentType };

const modules = import.meta.glob<FrameModule>("./src/frames/*.tsx", {
	eager: true,
});
const layouts = import.meta.glob<LayoutModule>("./src/frames/_layout.tsx", {
	eager: true,
});
const Layout = Object.values(layouts)[0]?.default;

/** 全部帧：先按 oryn-frames.json 的顺序，再按文件名补上其余的；`_` 开头的文件是共享外壳，不是帧。 */
function collectFrames(): FrameInfo[] {
	const byId = new Map<string, FrameModule>();
	for (const [file, mod] of Object.entries(modules)) {
		const id = file.slice("./src/frames/".length, -".tsx".length);
		if (!id.startsWith("_")) byId.set(id, mod);
	}
	const named = new Map(meta.frames.map((f) => [f.id, f.name]));
	const order = [
		...meta.frames.map((f) => f.id).filter((id) => byId.has(id)),
		...[...byId.keys()].filter((id) => !named.has(id)).sort(),
	];
	return order.map((id) => {
		const mod = byId.get(id);
		const own = mod?.frame?.name;
		return {
			id,
			name: named.get(id) ?? (typeof own === "string" && own ? own : id),
			Component: mod?.default,
		};
	});
}

const frames = collectFrames();
const startId =
	frames.find((f) => f.id === meta.start)?.id ?? frames[0]?.id ?? "";

function subscribe(cb: () => void) {
	window.addEventListener("hashchange", cb);
	return () => window.removeEventListener("hashchange", cb);
}

/** 当前路由：#/<id> 里的 id；空串表示起始帧。 */
function currentRoute(): string {
	return decodeURIComponent(location.hash.replace(/^#\/?/, ""));
}

function FrameList({ missing }: { missing?: string }) {
	return (
		<main
			style={{
				fontFamily: "system-ui, sans-serif",
				maxWidth: 640,
				margin: "48px auto",
				padding: "0 24px",
				lineHeight: 1.6,
			}}
		>
			{missing !== undefined && <p>没有帧「{missing}」。</p>}
			<h1 style={{ fontSize: 20 }}>{meta.title}</h1>
			{frames.length === 0 ? (
				<p>src/frames 下还没有帧：每帧一个 src/frames/&lt;id&gt;.tsx 文件。</p>
			) : (
				<ul>
					{frames.map((f) => (
						<li key={f.id}>
							<a href={`#/${f.id}`}>{f.name}</a>
						</li>
					))}
				</ul>
			)}
		</main>
	);
}

function App() {
	const route = useSyncExternalStore(subscribe, currentRoute);
	const id = route === "" ? startId : route;
	const frame = frames.find((f) => f.id === id);

	useEffect(() => {
		document.title = frame ? `${frame.name} · ${meta.title}` : meta.title;
		window.scrollTo(0, 0);
	}, [frame]);

	if (route === "_frames") return <FrameList />;
	if (!frame) return <FrameList missing={id || undefined} />;
	const { Component } = frame;
	const body = Component ? (
		<Component />
	) : (
		<p style={{ padding: 24, fontFamily: "system-ui" }}>
			src/frames/{frame.id}.tsx 没有默认导出组件。
		</p>
	);
	return Layout ? <Layout frame={frame.id}>{body}</Layout> : body;
}

const el = document.getElementById("root");
if (!el) throw new Error("缺少 #root");
createRoot(el).render(
	<StrictMode>
		<App />
	</StrictMode>,
);
