/**
 * `desktop_record` — 录制线程 orb 的桌面（M25 3.3，工具在 `internal/orbdrt/assets/oryn-orb.ts`）。参数 `{action: "start" | "stop"}`。
 * start 成功时没有 details，卡片头部是带录制红点的「开始录制桌面」，输出段一行回执；stop 的 details 是 `DesktopRecording`
 * （`{path, bytes, durationMs, width, height, truncated, stoppedBy}`，`stoppedBy ∈ agent | limit | error | control`；
 * control = 用户接管、agent 请求人工操作或回合结束时，为保护人工输入提前结束）。
 * 出错时 isError，details 为 `{tool, error: {code, message}}`。
 * 视频留在 orb 上：输出段是一块按录像宽高比预留的放映框，内嵌 `<video>`，地址取 `host.orbFileUrl(path)`；
 * 宿主不提供（分享页）或返回 null（orb 已删除）时放映框里只给说明，下面是文件名与分辨率。
 * 视频加载失败时给错误文字，宿主有 `openOrbFile` 时再给「在文件中打开」。
 * 读数（时长、大小、未正常收尾）在 Meta。`details` 残缺或类型不对时一律当作没有。
 *
 * 摘要、读数、提示、放映框、文件信息与出错卡片几个部件也给 `computer_record`（`./computer-record`）用。
 */
import { type CSSProperties, type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { formatBytes } from "../../lib/format";
import { ArgsTree } from "../generic";
import { ErrorBlock, InvalidArg, Note, PathText } from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import { detailsRecord, isRecord, num, resultTextOf, str } from "../util";

/** stop 结果里的一段录像。 */
export interface DesktopRecordingCard {
	path: string;
	bytes: number | null;
	durationMs: number | null;
	width: number | null;
	height: number | null;
	truncated: boolean;
	stoppedBy: "agent" | "limit" | "error" | "control" | null;
}

/** 把一个 `DesktopRecording` 对象读成卡片数据；不是对象或缺路径时为 null。 */
export function parseRecording(d: unknown): DesktopRecordingCard | null {
	if (!isRecord(d)) return null;
	const path = str(d.path);
	if (!path) return null;
	const stoppedBy = str(d.stoppedBy);
	return {
		path,
		bytes: num(d.bytes),
		durationMs: num(d.durationMs),
		width: num(d.width),
		height: num(d.height),
		truncated: d.truncated === true,
		stoppedBy:
			stoppedBy === "agent" ||
			stoppedBy === "limit" ||
			stoppedBy === "error" ||
			stoppedBy === "control"
				? stoppedBy
				: null,
	};
}

/** 从成功的 stop 结果里取录像（details 本身就是 `DesktopRecording`）；出错、没有 details 或缺路径时为 null。 */
export function recordingOf(
	result: ToolResultLike | undefined,
): DesktopRecordingCard | null {
	if (!result || result.isError) return null;
	return parseRecording(detailsRecord(result));
}

/** 时长 `m:ss`（与工具给模型的文本同一写法）。 */
export function formatClock(ms: number): string {
	const s = Math.max(0, Math.round(ms / 1000));
	return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** 头部摘要：start 带录制红点；stop 带停止方块与录像路径。 */
export function RecordSummary({
	action,
	rec,
}: {
	action: string | null;
	rec: DesktopRecordingCard | null;
}): ReactNode {
	const { t } = useTranslation();
	if (action === "start")
		return (
			<span className="tv-rec-sum">
				<i className="tv-rec-dot" aria-hidden="true" />
				{t("toolRender.desktopRecord.start")}
			</span>
		);
	if (action !== "stop") return <InvalidArg what="action" />;
	return (
		<>
			<span className="tv-rec-sum">
				<i className="tv-rec-stop" aria-hidden="true" />
				{t("toolRender.desktopRecord.stop")}
			</span>
			{rec && <PathText path={rec.path} />}
		</>
	);
}

/** 右侧读数：时长与大小；没能正常收尾时加一条警告。 */
export function RecordMeta({
	rec,
}: {
	rec: DesktopRecordingCard | null;
}): ReactNode {
	const { t } = useTranslation();
	if (!rec) return null;
	return (
		<>
			{rec.durationMs !== null && <span>{formatClock(rec.durationMs)}</span>}
			{rec.bytes !== null && <span>{formatBytes(rec.bytes)}</span>}
			{rec.truncated && (
				<span className="tv-meta-warn">
					{t("toolRender.desktopRecord.incomplete")}
				</span>
			)}
		</>
	);
}

/** 录像为什么停下（自动停止、出错、让给人工操作）与是否正常收尾的提示。 */
export function RecordNotes({ rec }: { rec: DesktopRecordingCard }): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			{rec.stoppedBy === "limit" && (
				<Note tone="warn">{t("toolRender.desktopRecord.stoppedByLimit")}</Note>
			)}
			{rec.stoppedBy === "error" && (
				<Note tone="warn">{t("toolRender.desktopRecord.stoppedByError")}</Note>
			)}
			{rec.stoppedBy === "control" && (
				<Note tone="warn">
					{t("toolRender.desktopRecord.stoppedByControl")}
				</Note>
			)}
			{rec.truncated && (
				<Note tone="warn">{t("toolRender.desktopRecord.truncated")}</Note>
			)}
		</>
	);
}

/** 放映框最高 15rem（与 `max-h-60` 一致）。 */
const VIDEO_MAX_REM = 15;

/** 放映框的尺寸：知道分辨率时按它预留宽高比（最高 15rem、不超过卡片宽），点播放前后卡片高度不变。 */
function frameStyle(rec: DesktopRecordingCard): CSSProperties | undefined {
	if (!rec.width || !rec.height) return undefined;
	return {
		aspectRatio: `${rec.width} / ${rec.height}`,
		width: `min(100%, ${(VIDEO_MAX_REM * rec.width) / rec.height}rem)`,
	};
}

/**
 * 内嵌播放；加载失败时换成 `failed`。换地址时由调用方按 `src` 重挂，错误状态随之清空。
 * `preload="none"`：取视频会唤醒冻结的 orb，只在用户点播放时才发请求，打开对话不发。
 */
export function RecordingVideo({
	src,
	rec,
	failed: failedView,
}: {
	src: string;
	rec: DesktopRecordingCard;
	failed: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const [failed, setFailed] = useState(false);
	if (failed) return failedView;
	return (
		// biome-ignore lint/a11y/useMediaCaption: 桌面录像没有声音，也没有字幕可配
		<video
			data-testid="record-video"
			className="tv-rec-frame"
			style={frameStyle(rec)}
			src={src}
			controls
			preload="none"
			aria-label={t("toolRender.desktopRecord.video")}
			onError={() => setFailed(true)}
		/>
	);
}

/** 放不了录像时（分享页、环境已删除、超过上限没存）占位的矮放映框：摄像机图标与去哪里找这段录像。 */
export function RecordPoster({ children }: { children: ReactNode }): ReactNode {
	return (
		<div className="tv-rec-frame tv-rec-poster">
			<span className="tv-rec-poster-icon" aria-hidden="true">
				<Icon name="videoCamera" size={20} />
			</span>
			<span>{children}</span>
		</div>
	);
}

/** 放映框下的一行：录像文件与分辨率（时长、大小在头部读数里）。 */
export function RecordFacts({ rec }: { rec: DesktopRecordingCard }): ReactNode {
	return (
		<div className="tv-rec-caption">
			<Icon name="file" size={11} />
			<PathText path={rec.path} />
			{rec.width !== null && rec.height !== null && (
				<span className="tv-faint">
					{rec.width}×{rec.height}
				</span>
			)}
		</div>
	);
}

/** 出错的调用：控制面给的错误信息，没有就用结果文本。 */
export function RecordErrorResult({
	result,
}: {
	result: ToolResultLike;
}): ReactNode {
	const d = detailsRecord(result);
	const text =
		(isRecord(d?.error) ? str(d.error.message) : null) ??
		resultTextOf(result).trim();
	return text ? <ErrorBlock text={text} /> : null;
}

/** start 的回执：录制红点 + 结果文本（写到哪个文件）。 */
export function RecordStarted({ text }: { text: ReactNode }): ReactNode {
	if (!text) return null;
	return (
		<div className="tv-rec-started">
			<i className="tv-rec-dot tv-rec-dot--live" aria-hidden="true" />
			<span>{text}</span>
		</div>
	);
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	return <RecordSummary action={str(args.action)} rec={recordingOf(result)} />;
}

function Meta({ result }: ToolRenderProps): ReactNode {
	return <RecordMeta rec={recordingOf(result)} />;
}

/** 视频加载失败：错误文字，宿主能打开 orb 上的文件时再给「在文件中打开」。 */
function OrbVideoFailed({
	path,
	openOrbFile,
}: {
	path: string;
	openOrbFile?: (path: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			<Note tone="err">{t("toolRender.desktopRecord.loadFailed")}</Note>
			{openOrbFile && (
				<div>
					<button
						type="button"
						className="tv-badge tv-badge--accent tv-agent-link"
						onClick={() => openOrbFile(path)}
					>
						{t("toolRender.desktopRecord.openInFiles")}
						<span className="tv-agent-link-arrow" aria-hidden="true">
							<Icon name="external" size={10} />
						</span>
					</button>
				</div>
			)}
		</>
	);
}

function Result({ args, result, host }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	if (result.isError) return <RecordErrorResult result={result} />;
	const rec = recordingOf(result);
	if (!rec)
		return str(args.action) === "start" ? (
			<RecordStarted text={resultTextOf(result).trim()} />
		) : null;
	const src = host?.orbFileUrl?.(rec.path) ?? null;
	return (
		<>
			<RecordNotes rec={rec} />
			{src ? (
				<RecordingVideo
					key={src}
					src={src}
					rec={rec}
					failed={
						<OrbVideoFailed path={rec.path} openOrbFile={host?.openOrbFile} />
					}
				/>
			) : (
				<RecordPoster>{t("toolRender.desktopRecord.storedInOrb")}</RecordPoster>
			)}
			<RecordFacts rec={rec} />
		</>
	);
}

export const desktopRecordRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input: ArgsTree,
	Result,
};
