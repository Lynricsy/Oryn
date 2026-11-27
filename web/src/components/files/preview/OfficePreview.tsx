/**
 * Word / Excel / PowerPoint / PDF 的文件面板预览：与 artifact 查看器共用 lib/artifacts/office 的渲染组件（契约 C4）。
 * 这里只负责把 Blob 读成 ArrayBuffer、给出主站的资源目录与应用语言的文案，并把组件配色接到应用主题。
 * 组件按格式各自 import（不走 office/index.ts），免得把 pdf.js 打进 Office 文档的分包。
 */
import {
	type ComponentType,
	type CSSProperties,
	type ReactNode,
	useEffect,
	useMemo,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import type {
	OfficeAssets,
	OfficeStrings,
	OfficeViewProps,
} from "../../../lib/artifacts/office/types";
import { ErrorText, Spinner } from "../../ui/primitives";

/** 主站的 Office 资源目录（vite.config.ts 发出的 `office/ooxml/` 与 `pdfjs/`），都是以 `/` 结尾的绝对地址。 */
export function mainOfficeAssets(): OfficeAssets {
	const root = new URL(import.meta.env.BASE_URL, window.location.origin).href;
	return { ooxml: `${root}office/ooxml/`, pdfjs: `${root}pdfjs/` };
}

/** 组件配色变量映射到应用主题，跟随明暗切换。 */
const THEME = {
	"--oryn-fg": "var(--text)",
	"--oryn-bg": "var(--bg)",
	"--oryn-raised": "var(--raised)",
	"--oryn-surface": "var(--inset)",
	"--oryn-line": "var(--line)",
	"--oryn-muted": "var(--text-muted)",
	"--oryn-accent": "var(--brand)",
	"--oryn-danger": "var(--bad)",
} as CSSProperties;

/** 用应用 i18n 给出 Office 组件的界面文案。 */
export function useOfficeStrings(): OfficeStrings {
	const { t } = useTranslation();
	return useMemo<OfficeStrings>(
		() => ({
			loading: t("files.preview.office.loading"),
			tooLarge: t("files.preview.office.tooLarge"),
			failed: (reason) => t("files.preview.office.failed", { reason }),
			rendered: (count) => t("files.preview.office.rendered", { count }),
			pageFailed: (n) => t("files.preview.office.pageFailed", { n }),
			tooManyPages: t("files.preview.office.tooManyPages"),
			sheetNoPrint: t("files.preview.office.sheetNoPrint"),
			pageLabel: (n) => t("files.preview.office.pageLabel", { n }),
			slideLabel: (n) => t("files.preview.office.slideLabel", { n }),
			pageNumber: t("files.preview.office.pageNumber"),
			zoomIn: t("files.preview.office.zoomIn"),
			zoomOut: t("files.preview.office.zoomOut"),
			fitWidth: t("files.preview.office.fitWidth"),
			present: t("files.preview.office.present"),
			exitHint: t("files.preview.office.exitHint"),
			prev: t("files.preview.office.prev"),
			next: t("files.preview.office.next"),
			counter: (n, total) => t("files.preview.pageOf", { n, total }),
		}),
		[t],
	);
}

type Bytes =
	| { kind: "loading" }
	| { kind: "error"; error: unknown }
	| { kind: "ready"; bytes: ArrayBuffer };

/** 读出 blob 的字节后交给 View 以普通视图渲染；文件面板里不做评论点选。 */
export function OfficePreview({
	blob,
	View,
}: {
	blob: Blob;
	View: ComponentType<OfficeViewProps>;
}): ReactNode {
	const strings = useOfficeStrings();
	const assets = useMemo(mainOfficeAssets, []);
	const [state, setState] = useState<Bytes>({ kind: "loading" });

	useEffect(() => {
		let alive = true;
		setState({ kind: "loading" });
		blob.arrayBuffer().then(
			(bytes) => alive && setState({ kind: "ready", bytes }),
			(error: unknown) => alive && setState({ kind: "error", error }),
		);
		return () => {
			alive = false;
		};
	}, [blob]);

	if (state.kind === "error")
		return (
			<div className="p-3">
				<ErrorText error={state.error} />
			</div>
		);
	if (state.kind === "loading")
		return (
			<div className="p-3">
				<Spinner />
			</div>
		);
	return (
		<div className="h-full min-h-0 bg-bg text-fg" style={THEME}>
			<View
				bytes={state.bytes}
				view="full"
				assets={assets}
				strings={strings}
				pickable={false}
			/>
		</div>
	);
}
