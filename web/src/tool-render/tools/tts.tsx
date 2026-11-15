/**
 * `tts` — 文本转语音，音频写到 `output_path`。结果形状见 oryn-core `tools/tts.ts`：
 * details 为 `{ bytes, voiceId, codec, backend }`；没有可用语音模型或全部候选失败时 isError、无 details。
 * 实际保存路径只在结果正文里（`Saved <n> bytes to <path> (…)`）：本地后端没有 MP3 编码器，
 * 会把非 `.wav` 的请求改写成同名 `.wav`，并在正文末尾说明替换原因。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatBytes } from "../../lib/format";
import { InvalidArg, Kv, KvGrid, PathText, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";

/** Core 成功正文的开头：`Saved <bytes> bytes to <displayPath> (model=…` / `(voice=…`。 */
const SAVED_RE = /^Saved \d+ bytes to (.+?) \((?:model|voice)=/;

/** 成功结果里 Core 实际写入的路径；没有结果、出错或正文不是该形状时为 null。 */
function savedPath(result: ToolResultLike | undefined): string | null {
	if (!result || result.isError) return null;
	return SAVED_RE.exec(resultTextOf(result).trim())?.[1] ?? null;
}

function Summary({ args, result }: ToolRenderProps): ReactNode {
	const path = savedPath(result) ?? str(args.output_path);
	const text = str(args.text);
	return (
		<>
			{path ? <PathText path={path} /> : <InvalidArg what="output_path" />}
			{text && (
				<span className="tv-trunc tv-muted">
					{" "}
					“{truncate(normalizeWs(text), 80)}”
				</span>
			)}
		</>
	);
}

/** 右侧读数：生成的音频大小。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const bytes = num(detailsRecord(result)?.bytes);
	return bytes === null ? null : <span>{formatBytes(bytes)}</span>;
}

function Body({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const details = detailsRecord(result);
	const text = str(args.text);
	const requested = str(args.output_path);
	const saved = savedPath(result);
	const output = saved ?? requested;
	const sampleRate = num(args.sample_rate);
	const bitRate = num(args.bit_rate);
	const codec = str(details?.codec);
	return (
		<>
			{/* 头部会被省略号截断：路径与全文都在这里完整给出 */}
			{text && <div className="tv-prose">{text}</div>}
			<KvGrid>
				<Kv k={t("toolRender.tts.output")}>
					{output && <PathText path={output} />}
				</Kv>
				{/* 实际路径与请求不同（本地后端把 .mp3 改写成 .wav）时，请求路径单独列出 */}
				{saved && requested && saved !== requested && (
					<Kv k={t("toolRender.tts.requested")}>
						<PathText path={requested} />
					</Kv>
				)}
				<Kv k={t("toolRender.tts.voice")}>
					{str(details?.voiceId) ?? str(args.voice_id)}
				</Kv>
				<Kv k={t("toolRender.tts.language")}>{str(args.language)}</Kv>
				<Kv k={t("toolRender.tts.format")}>
					{[
						codec,
						sampleRate === null ? null : `${sampleRate} Hz`,
						bitRate === null ? null : `${bitRate} bps`,
					]
						.filter(Boolean)
						.join(" · ")}
				</Kv>
				<Kv k={t("toolRender.tts.backend")}>{str(details?.backend)}</Kv>
			</KvGrid>
			{/* 成功正文同样给出：实际路径、本地采样率与格式替换说明只在这里 */}
			<ResultText result={result} maxLines={6} />
		</>
	);
}

export const ttsRenderer: ToolRenderer = { Summary, Meta, Body };
