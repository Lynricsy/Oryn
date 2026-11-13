/**
 * 单个文件的 diff：`git/diff` → 虚拟滚动的 git patch 行（不折叠，只挂载视口附近的行；
 * 1MiB 的 patch 可达十几万行，全部挂载会卡住界面）；二进制 / 过大 / 截断各有说明行。
 */
import { useVirtualizer } from "@tanstack/react-virtual";
import {
	type ReactNode,
	type RefObject,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { diffRows } from "../../tool-render/parts";
import "../../tool-render/tool-render.css";
import { ErrorText, IconButton, Spinner } from "../ui/primitives";
import {
	type GitFileChange,
	type GitFileDiff,
	type GitTarget,
	getGitDiff,
	isAbort,
} from "./git";

export function FileDiff({
	target,
	file,
	refresh,
	onBack,
}: {
	target: GitTarget;
	file: GitFileChange;
	/** `n` 变化时重新拉取；`bg` 为真表示由后台轮询 / agent 活动触发 */
	refresh: { n: number; bg: boolean };
	onBack?(): void;
}): ReactNode {
	const { t } = useTranslation();
	const { orbId, root, scope } = target;
	const scopeKey =
		scope.scope === "commit" ? `commit:${scope.sha}` : scope.scope;
	// 结果按「文件 + 范围」记键：换文件 / 范围时不显示旧 diff，同键刷新时不闪烁
	const key = `${orbId}|${root ?? ""}|${scopeKey}|${file.oldPath ?? ""}|${file.path}`;
	const [loaded, setLoaded] = useState<{
		key: string;
		diff: GitFileDiff;
	} | null>(null);
	const [error, setError] = useState<unknown>(null);
	const lastKey = useRef<string | null>(null);

	// biome-ignore lint/correctness/useExhaustiveDependencies: key 涵盖 target 与 file；refresh.n 触发重新拉取
	useEffect(() => {
		let cancelled = false;
		const ctl = new AbortController();
		setError(null);
		// 只有同一文件的后台刷新才用 bg；换文件 / 范围是用户操作
		const bg = refresh.bg && lastKey.current === key;
		lastKey.current = key;
		getGitDiff({ orbId, root, scope }, file, bg, ctl.signal).then(
			(d) => {
				if (!cancelled) setLoaded({ key, diff: d });
			},
			(e) => {
				// 后台刷新的超时不打断已显示的 diff
				if (!cancelled && !(bg && isAbort(e))) setError(e);
			},
		);
		return () => {
			cancelled = true;
			ctl.abort();
		};
	}, [key, refresh.n]);

	const diff = loaded?.key === key ? loaded.diff : null;
	const scrollRef = useRef<HTMLDivElement>(null);
	return (
		<div className="flex h-full min-h-0 flex-col">
			<div className="flex h-9 shrink-0 items-center gap-1 border-border border-b px-2">
				{onBack && (
					<IconButton icon="back" label={t("changes.back")} onClick={onBack} />
				)}
				<span
					className="min-w-0 flex-1 truncate font-mono text-xs"
					title={file.path}
				>
					{file.oldPath ? `${file.oldPath} → ${file.path}` : file.path}
				</span>
			</div>
			<div ref={scrollRef} className="min-h-0 flex-1 overflow-auto p-2">
				{error ? (
					<ErrorText error={error} />
				) : !diff ? (
					<Spinner />
				) : diff.kind === "binary" ? (
					<p className="text-muted text-sm">{t("changes.diff.binary")}</p>
				) : diff.kind === "large" ? (
					<p className="text-muted text-sm">{t("changes.diff.large")}</p>
				) : diff.patch === "" ? (
					<p className="text-muted text-sm">{t("changes.diff.empty")}</p>
				) : (
					<div className="tv-card">
						<VirtualPatch patch={diff.patch} scrollRef={scrollRef} />
						{diff.truncated && (
							<p className="px-2 py-1 text-warn text-xs">
								{t("changes.diff.truncated")}
							</p>
						)}
					</div>
				)}
			</div>
		</div>
	);
}

/** 每行固定高度（与 `.tv-diff` 的 11.5px 字号相配），绝对定位后行高必须确定。 */
export const DIFF_ROW_PX = 18;
const OVERSCAN = 40;

/** git patch 行的虚拟列表：滚动容器是外层的 diff 区域，横向溢出也由它滚动。 */
function VirtualPatch({
	patch,
	scrollRef,
}: {
	patch: string;
	scrollRef: RefObject<HTMLDivElement | null>;
}): ReactNode {
	const { lines, classes } = useMemo(() => diffRows(patch, true), [patch]);
	const virtualizer = useVirtualizer({
		count: lines.length,
		getScrollElement: () => scrollRef.current,
		estimateSize: () => DIFF_ROW_PX,
		overscan: OVERSCAN,
	});
	return (
		<div className="tv-out">
			<div
				className="tv-diff"
				data-testid="diff-rows"
				data-rows={lines.length}
				style={{
					position: "relative",
					overflow: "visible",
					height: virtualizer.getTotalSize() + 10,
				}}
			>
				{virtualizer.getVirtualItems().map((v) => {
					const cls = classes[v.index] ?? "";
					return (
						<div
							key={v.key}
							data-index={v.index}
							className={`tv-diff-row${cls ? ` tv-diff-row${cls}` : ""}`}
							style={{
								position: "absolute",
								top: 5,
								left: 0,
								minWidth: "100%",
								width: "max-content",
								height: DIFF_ROW_PX,
								lineHeight: `${DIFF_ROW_PX}px`,
								transform: `translateY(${v.start}px)`,
							}}
						>
							{lines[v.index] || " "}
						</div>
					);
				})}
			</div>
		</div>
	);
}
