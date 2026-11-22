/**
 * `computer_record` — 录制助手电脑的桌面（M25 3.4，宿主工具在 `internal/coord/computer.go`，只给全局助手）。
 * 参数 `{action: "start" | "stop"}`。start 的 details 是 `{path}`，卡片只有一行「开始录制桌面」；
 * stop 的 details 是 `{recording: DesktopRecording, libraryFileId?}`：视频不超过传输上限时存进文件库，
 * 才带 `libraryFileId`。出错时 isError（`already_recording | not_recording | not_ready | control_user`），同 desktop_record。
 * 有 `libraryFileId` 且宿主给了 `libraryFileUrl` 时内嵌 `<video>` 播放文件库里的那份；否则（超过上限没存、分享页）
 * 只给电脑上的路径，引导去电脑的「文件」页签。展示部件与 `desktop_record` 共用。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { genericRenderer } from "../generic";
import { Note } from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import { detailsRecord, str } from "../util";
import {
	type DesktopRecordingCard,
	parseRecording,
	RecordErrorResult,
	RecordFacts,
	RecordingVideo,
	RecordMeta,
	RecordNotes,
	RecordSummary,
} from "./desktop-record";

interface ComputerRecordingCard {
	rec: DesktopRecordingCard;
	/** 存进文件库时的文件 id；超过传输上限没存时为 null。 */
	libraryFileId: string | null;
}

/** 从成功的 stop 结果里取录像与文件库 id；出错、start 的结果或 details 残缺时为 null。 */
function computerRecordingOf(
	result: ToolResultLike | undefined,
): ComputerRecordingCard | null {
	if (!result || result.isError) return null;
	const d = detailsRecord(result);
	const rec = parseRecording(d?.recording);
	if (!rec) return null;
	return { rec, libraryFileId: str(d?.libraryFileId) || null };
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	return (
		<RecordSummary
			action={str(args.action)}
			rec={computerRecordingOf(result)?.rec ?? null}
		/>
	);
}

function Meta({ result }: ToolRenderProps): ReactNode {
	return <RecordMeta rec={computerRecordingOf(result)?.rec ?? null} />;
}

function Result({ result, host }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (result?.isError) return <RecordErrorResult result={result} />;
	const card = computerRecordingOf(result);
	// start 的结果没有要展示的：头部那一行已经说清楚
	if (!card) return null;
	const src =
		card.libraryFileId && host?.libraryFileUrl
			? host.libraryFileUrl(card.libraryFileId)
			: null;
	return (
		<>
			<RecordNotes rec={card.rec} />
			{src ? (
				<RecordingVideo
					key={src}
					src={src}
					failed={
						<Note tone="err">{t("toolRender.computerRecord.loadFailed")}</Note>
					}
				/>
			) : (
				<Note>{t("toolRender.computerRecord.onComputer")}</Note>
			)}
			<RecordFacts rec={card.rec} />
		</>
	);
}

export const computerRecordRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input: genericRenderer.Input,
	Result,
};
