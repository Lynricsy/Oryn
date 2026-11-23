/**
 * `computer_record` — 录制助手电脑的桌面（M25 3.4，宿主工具在 `internal/coord/computer.go`，只给全局助手）。
 * 参数 `{action: "start" | "stop"}`。start 的 details 是 `{path}`，头部是带录制红点的「开始录制桌面」，输出段一行回执；
 * stop 的 details 是 `{recording: DesktopRecording, libraryFileId?}`：视频不超过传输上限时存进文件库，
 * 才带 `libraryFileId`。出错时 isError（`already_recording | not_recording | not_ready | control_user`），同 desktop_record。
 * 有 `libraryFileId` 且宿主给了 `libraryFileUrl` 时放映框内嵌 `<video>` 播放文件库里的那份；否则（超过上限没存、分享页）
 * 放映框里只给说明，引导去电脑的「文件」页签，下面是电脑上的路径。展示部件与 `desktop_record` 共用。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ArgsTree } from "../generic";
import { Note, PathText } from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import { detailsRecord, resultTextOf, str } from "../util";
import {
	type DesktopRecordingCard,
	parseRecording,
	RecordErrorResult,
	RecordFacts,
	RecordingVideo,
	RecordMeta,
	RecordNotes,
	RecordPoster,
	RecordStarted,
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

function Result({ args, result, host }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	if (result.isError) return <RecordErrorResult result={result} />;
	const card = computerRecordingOf(result);
	if (!card) {
		if (str(args.action) !== "start") return null;
		// start 的回执：details 里有录像要写到的路径时给路径，否则给结果文本
		const path = str(detailsRecord(result)?.path);
		return (
			<RecordStarted
				text={path ? <PathText path={path} /> : resultTextOf(result).trim()}
			/>
		);
	}
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
					rec={card.rec}
					failed={
						<Note tone="err">{t("toolRender.computerRecord.loadFailed")}</Note>
					}
				/>
			) : (
				<RecordPoster>{t("toolRender.computerRecord.onComputer")}</RecordPoster>
			)}
			<RecordFacts rec={card.rec} />
		</>
	);
}

export const computerRecordRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input: ArgsTree,
	Result,
};
