// 幻灯片查看器的入口（构建成 dist/sandbox/viewers/slides.{js,css}）：读页面里的配置，挂到 #root。

import type { ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { useViewerBridge } from "../shared/bridge";
import {
	pickLocale,
	readViewerConfig,
	type ViewerConfig,
	type ViewMode,
	viewModeOf,
} from "../shared/config";
import { useViewerContent } from "../shared/content";
import { type ViewerStrings, viewerStrings } from "../shared/strings";
import { SlidesViewer } from "./SlidesViewer";
import "../shared/shared.css";
import "./slides.css";

function App({
	config,
	view,
	strings,
}: {
	config: ViewerConfig;
	view: ViewMode;
	strings: ViewerStrings;
}): ReactNode {
	const content = useViewerContent(config, view);
	const comments = useViewerBridge();
	return (
		<SlidesViewer
			content={content}
			view={view}
			strings={strings}
			comments={comments}
		/>
	);
}

const config = readViewerConfig(document);
const strings = viewerStrings(pickLocale(config?.locale));
const root = document.getElementById("root");
if (root)
	createRoot(root).render(
		config ? (
			<App
				config={config}
				view={viewModeOf(location.search)}
				strings={strings}
			/>
		) : (
			<p className="oryn-placeholder">{strings.loadFailed}</p>
		),
	);
