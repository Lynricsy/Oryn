/**
 * 单个文件的 diff：`git/diff` → 虚拟滚动的 git patch 行（不折叠，只挂载视口附近的行；
 * 1MiB 的 patch 可达十几万行，全部挂载会卡住界面）；二进制 / 过大 / 截断各有说明行。
 * 主体 `PatchView` 与标题 `FileChangeLabel` 不依赖 GitTarget，项目仓库的提交详情（阶段 ②）复用它们。
 */
import { useVirtualizer } from "@tanstack/react-virtual";
import {
	type ReactNode,
	type RefObject,
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { useContextTarget } from "../../lib/context-menu/registry";
import { anchorOf } from "../../lib/context-menu/selection";
import { diffRows } from "../../tool-render/parts";
import "../../tool-render/tool-render.css";
import { Callout } from "../ui/data";
import { FileTypeIcon } from "../ui/FileTypeIcon";
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
	const { orbId, threadId, root, base, scope } = target;
	const scopeKey =
		scope.scope === "commit" ? `commit:${scope.sha}` : scope.scope;
	// 结果按「文件 + 范围 + 基线」记键：换文件 / 范围 / 基线时不显示旧 diff，同键刷新时不闪烁
	const key = `${orbId}|${threadId}|${root ?? ""}|${base ?? ""}|${scopeKey}|${file.oldPath ?? ""}|${file.path}`;
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
		getGitDiff(
			{ orbId, threadId, root, base, scope },
			file,
			bg,
			ctl.signal,
		).then(
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
	const scrollRef = useRef<HTMLDivElement | null>(null);
	// diff 里的选区：以「改动面板里某文件的 diff」为来源接入旁路提问
	const ctxRef = useContextTarget<HTMLDivElement>({
		selection: (sel, req) => ({
			threadId: req.askThreadId,
			text: sel.text,
			before: "",
			after: "",
			source: { kind: "diff", path: file.path },
			anchor: anchorOf(sel, req.x, req.y),
		}),
	});
	// 合并的 ref 必须稳定：每次渲染换新回调会先置空再挂回，子组件（虚拟列表）的布局副作用会读到 null
	const scrollHost = useCallback(
		(el: HTMLDivElement | null) => {
			scrollRef.current = el;
			ctxRef(el);
		},
		[ctxRef],
	);
	return (
		<div className="flex h-full min-h-0 flex-col">
			<div className="flex h-9 shrink-0 items-center gap-1.5 border-border border-b px-2">
				{onBack && (
					<IconButton icon="back" label={t("changes.back")} onClick={onBack} />
				)}
				<FileTypeIcon name={file.path} size={15} className="ml-1" />
				<FileChangeLabel file={file} />
			</div>
			<div ref={scrollHost} className="min-h-0 flex-1 overflow-auto p-2">
				<PatchView diff={diff} error={error} scrollRef={scrollRef} />
			</div>
		</div>
	);
}

/** 文件路径（目录淡色、文件名加粗、重命名带旧路径）与 +x −y；放在 flex 行里，路径占满剩余宽度 */
export function FileChangeLabel({ file }: { file: GitFileChange }): ReactNode {
	const slash = file.path.lastIndexOf("/");
	return (
		<>
			<span
				className="min-w-0 flex-1 truncate font-mono text-xs"
				title={file.oldPath ? `${file.oldPath} → ${file.path}` : file.path}
			>
				{file.oldPath && (
					<span className="text-faint">{`${file.oldPath} → `}</span>
				)}
				<span className="text-faint">{file.path.slice(0, slash + 1)}</span>
				<span className="font-medium text-fg">
					{file.path.slice(slash + 1)}
				</span>
			</span>
			{!file.binary &&
				(file.additions !== undefined || file.deletions !== undefined) && (
					<span className="shrink-0 font-mono text-2xs tabular-nums">
						{file.additions !== undefined && (
							<span className="text-ok">{`+${file.additions}`}</span>
						)}
						{file.deletions !== undefined && (
							<span className="ml-1.5 text-err">{`−${file.deletions}`}</span>
						)}
					</span>
				)}
		</>
	);
}

/**
 * diff 主体：错误 / 加载 / 二进制 / 过大 / 空 / 虚拟 patch + 截断说明。
 * scrollRef 是外层滚动容器（虚拟列表用），调用方把本组件放在该容器里。
 */
export function PatchView({
	diff,
	error,
	scrollRef,
}: {
	diff: GitFileDiff | null;
	error: unknown;
	scrollRef: RefObject<HTMLDivElement | null>;
}): ReactNode {
	const { t } = useTranslation();
	if (error) return <ErrorText error={error} />;
	if (!diff)
		return (
			<div className="p-2">
				<Spinner />
			</div>
		);
	if (diff.kind === "binary")
		return (
			<Callout tone="neutral" icon="info">
				{t("changes.diff.binary")}
			</Callout>
		);
	if (diff.kind === "large")
		return (
			<Callout tone="neutral" icon="info">
				{t("changes.diff.large")}
			</Callout>
		);
	if (diff.patch === "")
		return (
			<Callout tone="neutral" icon="info">
				{t("changes.diff.empty")}
			</Callout>
		);
	return (
		// `.tv-card` 只为提供 `--tv-*` 配色变量；外框交给 `.tv-diff` 的内嵌井，
		// 且不能裁切：长行由外层滚动容器横向滚动
		<div
			className="tv-card flex flex-col gap-2"
			style={{
				margin: 0,
				border: 0,
				background: "transparent",
				overflow: "visible",
			}}
		>
			<VirtualPatch patch={diff.patch} scrollRef={scrollRef} />
			{diff.truncated && (
				<Callout tone="neutral" icon="info">
					{t("changes.diff.truncated")}
				</Callout>
			)}
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
