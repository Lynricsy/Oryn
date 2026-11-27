// Office 查看器（docx / xlsx / pptx / pdf）的共同入口：装 worker 垫片、读配置、取入口文件的字节、交给对应的视图组件。
// 组件本身在 src/lib/artifacts/office/（与应用的文件面板共用）；库与 pdf.js 资源从帧根下的 `_oryn/viewer/ooxml/`、
// `_oryn/viewer/pdfjs/` 加载（见 internal/artifacts/host.go 的 serveViewerAsset）。

import {
	type ComponentType,
	type ReactNode,
	useEffect,
	useMemo,
	useState,
} from "react";
import { createRoot } from "react-dom/client";
import { MAX_PREVIEW_BYTES } from "../../../src/lib/artifacts/office/limits";
import type {
	OfficeAssets,
	OfficeView,
	OfficeViewProps,
} from "../../../src/lib/artifacts/office/types";
import {
	pickLocale,
	readViewerConfig,
	type ViewerConfig,
	viewModeOf,
} from "./config";
import { type ViewerStrings, viewerStrings } from "./strings";
import { installWorkerShim } from "./worker-shim";
import "./shared.css";
import "./office.css";

/** 运行时挂在页面上的本地函数：请父页面进入 / 退出演示。没有运行时（顶层帧、发布页）时不存在。 */
export function requestPresent(on: boolean): void {
	const fn = (window as Window & { __orynPresent?: (on: boolean) => void })
		.__orynPresent;
	if (typeof fn === "function") fn(on);
}

/** 帧里的资源地址（绝对地址，worker 与动态 import 都按它解析）。 */
export function frameAssets(root: string, page: string): OfficeAssets {
	return {
		ooxml: new URL(`${root}_oryn/viewer/ooxml/`, page).href,
		pdfjs: new URL(`${root}_oryn/viewer/pdfjs/`, page).href,
	};
}

/** 入口文件的扩展名（含点），下载原件时接在标题后面。 */
function extOf(entry: string): string {
	const dot = entry.lastIndexOf(".");
	return dot < 0 ? "" : entry.slice(dot);
}

/**
 * 下载原件的地址：入口文件带 `?download=1`，服务端回 `Content-Disposition: attachment` 并从内容存储流式输出
 * （internal/artifacts/host.go 的 setDownload）。不透明源里光靠 `download` 属性不可靠，也不先 fetch 成 Blob（文件可达 256 MiB）。
 */
export function downloadHref(entry: string): string {
	return `${entry}?download=1`;
}

type Fetched =
	| { kind: "loading" }
	| { kind: "failed" }
	| { kind: "ready"; bytes: ArrayBuffer };

function OfficeApp({
	config,
	view,
	strings,
	View,
}: {
	config: ViewerConfig;
	view: OfficeView;
	strings: ViewerStrings;
	View: ComponentType<OfficeViewProps>;
}): ReactNode {
	const tooLarge = config.size > MAX_PREVIEW_BYTES;
	const [file, setFile] = useState<Fetched>({ kind: "loading" });
	const assets = useMemo(
		() => frameAssets(config.root, location.href),
		[config.root],
	);

	useEffect(() => {
		if (tooLarge) return;
		let alive = true;
		// 入口按页面地址相对解析：读到的是页面所在的那个版本
		fetch(new URL(config.entry, location.href), { credentials: "omit" })
			.then((r) => {
				if (!r.ok) throw new Error(String(r.status));
				return r.arrayBuffer();
			})
			.then(
				(bytes) => alive && setFile({ kind: "ready", bytes }),
				() => alive && setFile({ kind: "failed" }),
			);
		return () => {
			alive = false;
		};
	}, [config.entry, tooLarge]);

	let body: ReactNode;
	if (tooLarge)
		body = <p className="oryn-placeholder">{strings.office.tooLarge}</p>;
	else if (file.kind === "failed")
		body = <p className="oryn-placeholder">{strings.loadFailed}</p>;
	else if (file.kind === "loading")
		body = <p className="oryn-placeholder">{strings.loading}</p>;
	else
		body = (
			<View
				bytes={file.bytes}
				view={view}
				assets={assets}
				strings={strings.office}
				onReady={() => {
					document.documentElement.dataset.ready = "true";
				}}
			/>
		);

	return (
		<div
			className={`oryn-office oryn-office-${view}${config.standalone ? " oryn-office-standalone" : ""}`}
		>
			{body}
			{config.standalone && view === "full" ? (
				<a
					className="oryn-btn oryn-office-download"
					href={downloadHref(config.entry)}
					download={`${config.title || "untitled"}${extOf(config.entry)}`}
				>
					{strings.download}
				</a>
			) : null}
		</div>
	);
}

/** 挂载 Office 查看器；View 是对应格式的视图组件。 */
export function mountOffice(View: ComponentType<OfficeViewProps>): void {
	installWorkerShim(window);
	const config = readViewerConfig(document);
	const strings = viewerStrings(pickLocale(config?.locale));
	const root = document.getElementById("root");
	if (!root) return;
	createRoot(root).render(
		config ? (
			<OfficeApp
				config={config}
				view={viewModeOf(location.search) ?? "full"}
				strings={strings}
				View={View}
			/>
		) : (
			<p className="oryn-placeholder">{strings.loadFailed}</p>
		),
	);
}
