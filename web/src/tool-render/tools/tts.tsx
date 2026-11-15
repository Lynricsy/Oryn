/**
 * `tts` — 文本转语音，音频写到 `output_path`。结果形状见 oryn-core `tools/tts.ts`：
 * details 为 `{ bytes, voiceId, codec, backend }`；没有可用语音模型或全部候选失败时 isError、无 details。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { formatBytes } from "../../lib/format";
import { InvalidArg, Kv, KvGrid, PathText, ResultText } from "../parts";
import type { ToolRenderer, ToolRenderProps } from "../types";
import { detailsRecord, normalizeWs, num, str, truncate } from "../util";

function Summary({ args }: ToolRenderProps): ReactNode {
	const path = str(args.output_path);
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
	const sampleRate = num(args.sample_rate);
	const bitRate = num(args.bit_rate);
	const codec = str(details?.codec);
	return (
		<>
			{/* 头部会被省略号截断：路径与全文都在这里完整给出 */}
			{text && <div className="tv-prose">{text}</div>}
			<KvGrid>
				<Kv k={t("toolRender.tts.output")}>
					{str(args.output_path) && (
						<PathText path={str(args.output_path) as string} />
					)}
				</Kv>
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
			{result?.isError && <ResultText result={result} maxLines={6} />}
		</>
	);
}

export const ttsRenderer: ToolRenderer = { Summary, Meta, Body };
