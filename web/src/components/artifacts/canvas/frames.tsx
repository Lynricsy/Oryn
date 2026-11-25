/**
 * 画布上的画框（计划 M14 6.3）：每个画框是一个绝对定位的 `<iframe>`（属性同 `PageFrame`），整个编辑器共用一个帧令牌。
 * 地址是 `<帧根>_oryn/v/<n>/<file>`，`n` 取「这个文件或任何非 HTML 文件最近一次变化」的版本：新版本到来时只有内容变了的
 * 画框才重载。同时最多 `MAX_LOADED` 个画框保持加载，其余显示名字与尺寸的占位。
 * 设计画布（M29）的画框在实时预览开着时换成 portal 上的 dev 帧（`LiveFrame.tsx`）。
 */
import { type ReactNode, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type {
	SelectMode,
	SelectPin,
} from "../../../../sandbox/runtime/messages";
import type { ArtifactDetail } from "../../../api/artifact-queries";
import type { Box, CanvasPage } from "../../../lib/artifacts/canvas-doc";
import type { FrameLink } from "../frame-link";
import { ARTIFACT_SANDBOX } from "../PageFrame";
import { LiveFrame } from "./LiveFrame";
import { useFrameLink } from "./use-frame-link";

/** 同时保持加载的画框数。 */
export const MAX_LOADED = 48;

/** 新画框在 agent 写内容之前的空白页（与 internal/artifacts/live.go 的 BlankPage 逐字节相同）。 */
export const BLANK_PAGE =
	'<!doctype html>\n<html>\n<head>\n<meta charset="utf-8">\n</head>\n<body></body>\n</html>\n';
const BLANK_BYTES = new TextEncoder().encode(BLANK_PAGE).byteLength;

let blankSha: string | null = null;
// 安全上下文才有 crypto.subtle；没有时只按大小判断
if (typeof crypto !== "undefined" && crypto.subtle)
	void crypto.subtle
		.digest("SHA-256", new TextEncoder().encode(BLANK_PAGE))
		.then((d) => {
			blankSha = Array.from(new Uint8Array(d), (b) =>
				b.toString(16).padStart(2, "0"),
			).join("");
		})
		.catch(() => undefined);

type ArtifactFile = ArtifactDetail["files"][number];

/** 文件是不是空白页（agent 还没写内容的画框）。 */
export function isBlankPage(f: ArtifactFile): boolean {
	return f.size === BLANK_BYTES && (blankSha === null || f.sha256 === blankSha);
}

/** 各文件该用哪个版本的地址。 */
export interface FileVersions {
	head: number;
	files: readonly ArtifactFile[];
	/** 路径 → 版本号。 */
	n: ReadonlyMap<string, number>;
}

const HTML = /\.html?$/i;

/**
 * 从上一份（`prev`）推出 head 版本下各文件的版本号：HTML 文件内容变了（或新出现）取 head，否则沿用；
 * 任何非 HTML 文件（共享的样式、脚本、图片）变了、增删了，全部取 head。没有上一份时全部取 head。
 */
export function fileVersions(
	prev: FileVersions | null,
	head: number,
	files: readonly ArtifactFile[],
): FileVersions {
	if (!prev || prev.head > head)
		return { head, files, n: new Map(files.map((f) => [f.path, head])) };
	if (prev.head === head) return prev;
	const before = new Map(prev.files.map((f) => [f.path, f.sha256]));
	const now = new Set(files.map((f) => f.path));
	const assets =
		files.some((f) => !HTML.test(f.path) && before.get(f.path) !== f.sha256) ||
		prev.files.some((f) => !HTML.test(f.path) && !now.has(f.path));
	const n = new Map<string, number>();
	for (const f of files) {
		const old = prev.n.get(f.path);
		n.set(
			f.path,
			assets || old === undefined || before.get(f.path) !== f.sha256
				? head
				: old,
		);
	}
	return { head, files, n };
}

/** 画框文件在帧根下的地址：逐段编码，文件名里的 #、?、% 与空格不会变成片段、查询或转义。 */
export function pageUrl(root: string, n: number, file: string): string {
	const base = root.endsWith("/") ? root : `${root}/`;
	return `${base}_oryn/v/${n}/${file.split("/").map(encodeURIComponent).join("/")}`;
}

/** 离视口最近的 `max` 个画框（视口里的都算距离 0）。 */
export function nearestPages(
	pages: readonly CanvasPage[],
	view: Box,
	max = MAX_LOADED,
): Set<string> {
	if (pages.length <= max) return new Set(pages.map((p) => p.id));
	const dist = (p: CanvasPage) => {
		const dx = Math.max(
			view.x - (p.x + p.width),
			0,
			p.x - (view.x + view.width),
		);
		const dy = Math.max(
			view.y - (p.y + p.height),
			0,
			p.y - (view.y + view.height),
		);
		return Math.hypot(dx, dy);
	};
	return new Set(
		pages
			.map((p) => ({ id: p.id, d: dist(p) }))
			.toSorted((a, b) => a.d - b.d)
			.slice(0, max)
			.map((x) => x.id),
	);
}

/**
 * 画框的内容状态：可加载的地址、设计画布的实时帧（portal 地址与帧源；`nonce` 变了就重建）、还没有文件或是空白页
 * （agent 还没写）、超出加载上限。
 */
export type PageContent =
	| { kind: "frame"; src: string }
	| { kind: "live"; src: string; origin: string; nonce: number }
	| { kind: "empty" }
	| { kind: "parked" };

export interface CanvasFrameProps {
	page: CanvasPage;
	content: PageContent;
	/** 帧的标题（读屏）。 */
	title: string;
	/** 进入了画框内部：iframe 接管指针。 */
	inside: boolean;
	mode: SelectMode;
	accent: string;
	pins: SelectPin[];
	/** 画框的通道建立 / 拆除（null）。 */
	onLink(pageId: string, link: FrameLink | null): void;
	/** 帧加载完成后 `READY_WAIT_MS` 内没收到 `ready`：帧令牌可能已失效。 */
	onStale(): void;
	onReady(): void;
	/** 实时帧 `LIVE_READY_MS` 内没收到 `ready`（WebKit 下 portal 握手被带到主站登录页）。 */
	onLiveTimeout(): void;
	/** 只在跟随最新版本时检测失效（旧版本的帧不带运行时）。 */
	detectStale: boolean;
	/** 每次变化都把画框重载一次（同意请求落定后页面重新 `use`）。 */
	reloadNonce: number;
}

/** 等运行时 `ready` 的上限（同 `PageFrame`）。 */
const READY_WAIT_MS = 1000;

export function CanvasFrame({
	page,
	content,
	title,
	inside,
	mode,
	accent,
	pins,
	onLink,
	onStale,
	onReady,
	onLiveTimeout,
	detectStale,
	reloadNonce,
}: CanvasFrameProps): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			className="canvas-page"
			data-page={page.id}
			data-inside={inside ? "" : undefined}
			style={{
				left: page.x,
				top: page.y,
				width: page.width,
				height: page.height,
			}}
		>
			{content.kind === "live" ? (
				<LiveFrame
					key={content.nonce}
					pageId={page.id}
					src={content.src}
					origin={content.origin}
					title={title}
					mode={mode}
					accent={accent}
					pins={pins}
					onLink={onLink}
					onTimeout={onLiveTimeout}
				/>
			) : content.kind === "frame" ? (
				<FrameIframe
					pageId={page.id}
					src={content.src}
					title={title}
					mode={mode}
					accent={accent}
					pins={pins}
					onLink={onLink}
					onStale={onStale}
					onReady={onReady}
					detectStale={detectStale}
					reloadNonce={reloadNonce}
				/>
			) : (
				<div className="canvas-placeholder" data-testid="canvas-placeholder">
					<span>
						{content.kind === "empty"
							? t("artifacts.canvas.emptyFrame")
							: page.name}
					</span>
					<span className="tabular-nums">
						{page.width} × {page.height}
					</span>
				</div>
			)}
		</div>
	);
}

function FrameIframe({
	pageId,
	src,
	title,
	mode,
	accent,
	pins,
	onLink,
	onStale,
	onReady,
	detectStale,
	reloadNonce,
}: {
	pageId: string;
	src: string;
	title: string;
	mode: SelectMode;
	accent: string;
	pins: SelectPin[];
	onLink(pageId: string, link: FrameLink | null): void;
	onStale(): void;
	onReady(): void;
	detectStale: boolean;
	reloadNonce: number;
}): ReactNode {
	const ref = useRef<HTMLIFrameElement>(null);
	const cb = useRef({ onStale, detectStale });
	cb.current = { onStale, detectStale };
	const ready = useRef(false);
	const timer = useRef<number | undefined>(undefined);
	useEffect(() => () => clearTimeout(timer.current), []);
	useFrameLink(ref, {
		pageId,
		mode,
		accent,
		pins,
		onLink,
		onReady: () => {
			ready.current = true;
			clearTimeout(timer.current);
			onReady();
		},
		onLoaded: () => {
			ready.current = false;
			clearTimeout(timer.current);
			if (cb.current.detectStale)
				timer.current = window.setTimeout(() => {
					if (!ready.current) cb.current.onStale();
				}, READY_WAIT_MS);
		},
	});

	// 只在 reloadNonce 变化时重载（挂载时不算）；地址变化由 src 属性本身处理
	const seenNonce = useRef(reloadNonce);
	// biome-ignore lint/correctness/useExhaustiveDependencies: 见上
	useEffect(() => {
		const el = ref.current;
		if (reloadNonce === seenNonce.current || !el) return;
		seenNonce.current = reloadNonce;
		el.src = src;
	}, [reloadNonce]);

	return (
		<iframe
			ref={ref}
			data-testid="canvas-frame"
			title={title}
			src={src}
			sandbox={ARTIFACT_SANDBOX}
			allow=""
			referrerPolicy="no-referrer"
		/>
	);
}
