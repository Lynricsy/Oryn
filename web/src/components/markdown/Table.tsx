/**
 * Markdown 表格：圆角描边的横向滚动框，外面再包一层 flex 居中容器。
 * 容器上给了 `--md-table-max`（转录里的助手正文）时，表格框可按内容需要放宽到这个宽度、两侧对称伸出正文列；
 * 没给时与正文同宽。悬停 / 聚焦时右上角出现三个复制按钮：CSV、Markdown 表格、图片（PNG）。
 */
import {
	createContext,
	type ReactNode,
	useContext,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { copyToClipboard } from "../../lib/clipboard";
import { showToast } from "../../state/toasts";
import { Icon, type IconName } from "../ui/icons";
import {
	rowsToCsv,
	rowsToMarkdown,
	type SourcePosition,
	sliceMarkdown,
	tableRows,
	tableToPng,
} from "./table-export";

/** 交给 react-markdown 解析的原文（hast 位置偏移相对于它），供「复制为 Markdown」切出原样的表格源码。 */
export const MarkdownSourceContext = createContext("");

function ToolButton({
	icon,
	label,
	run,
}: {
	icon: IconName;
	label: string;
	run: () => Promise<boolean>;
}): ReactNode {
	const { t } = useTranslation();
	const [done, setDone] = useState(false);
	return (
		<button
			type="button"
			className={`inline-flex size-[26px] items-center justify-center rounded-md border border-border bg-panel transition-colors duration-150 hover:bg-hover ${done ? "text-ok" : "text-muted hover:text-fg"}`}
			aria-label={label}
			title={label}
			onClick={() =>
				void run().then((ok) => {
					if (!ok) {
						showToast({ text: t("contextMenu.copyFailed"), tone: "err" });
						return;
					}
					setDone(true);
					setTimeout(() => setDone(false), 1200);
				})
			}
		>
			<Icon
				name={done ? "check" : icon}
				size={14}
				weight={done ? "bold" : "regular"}
			/>
		</button>
	);
}

export function MdTable({
	children,
	position,
}: {
	children?: ReactNode;
	position?: SourcePosition;
}): ReactNode {
	const { t } = useTranslation();
	const source = useContext(MarkdownSourceContext);
	const frame = useRef<HTMLDivElement | null>(null);
	const table = useRef<HTMLTableElement | null>(null);
	const canCopyImage =
		typeof ClipboardItem !== "undefined" &&
		typeof navigator.clipboard?.write === "function";
	return (
		<div className="md-table-wrap">
			{/* 可点按聚焦（不进 Tab 顺序）：触屏点一下表格即显出按钮 */}
			<div className="md-table-box" tabIndex={-1}>
				<div ref={frame} className="md-table">
					<table ref={table}>{children}</table>
				</div>
				<div className="md-table-tools" data-ctx-touch="">
					<ToolButton
						icon="fileCsv"
						label={t("markdownTable.copyCsv")}
						run={() =>
							copyToClipboard(
								table.current ? rowsToCsv(tableRows(table.current)) : "",
							)
						}
					/>
					<ToolButton
						icon="markdown"
						label={t("markdownTable.copyMarkdown")}
						run={() =>
							copyToClipboard(
								sliceMarkdown(source, position) ??
									(table.current
										? rowsToMarkdown(tableRows(table.current))
										: ""),
							)
						}
					/>
					{canCopyImage && (
						<ToolButton
							icon="image"
							label={t("markdownTable.copyImage")}
							run={async () => {
								const f = frame.current;
								const el = table.current;
								if (!f || !el) return false;
								// Safari 要求在用户手势里同步调用 clipboard.write：先交出 Promise<Blob>，画好再兑现
								try {
									await navigator.clipboard.write([
										new ClipboardItem({ "image/png": tableToPng(f, el) }),
									]);
									return true;
								} catch {
									return false;
								}
							}}
						/>
					)}
				</div>
			</div>
		</div>
	);
}
