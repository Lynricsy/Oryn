/**
 * `tts` — 文本转语音，音频写到 `output_path`。结果形状见 oryn-core `tools/tts.ts`：
 * details 为 `{ bytes, voiceId, codec, backend }`；没有可用语音模型或全部候选失败时 isError、无 details。
 * 实际保存路径只在结果正文里（`Saved <n> bytes to <path> (…)`）：本地后端没有 MP3 编码器，
 * 会把非 `.wav` 的请求改写成同名 `.wav`，并在正文末尾说明替换原因。
 * 头部：朗读文本 + 输出文件，读数是音频大小与格式。输入段是引文样式的全文与音色、语言、格式徽标；
 * 输出段是播放器（音频在线程 orb 上、路径是绝对路径且宿主给了 `orbFileUrl` 时；`preload="none"`，
 * 点播放才取文件，不唤醒冻结的 orb），只读分享页只给文件路径；后端、音色、编码收在次要行。
 */
import { type CSSProperties, type ReactNode, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../../components/ui/icons";
import { formatBytes } from "../../lib/format";
import {
	Badge,
	ErrorBlock,
	InvalidArg,
	Note,
	Output,
	PathText,
} from "../parts";
import type { ToolRenderer, ToolRenderProps, ToolResultLike } from "../types";
import {
	detailsRecord,
	normalizeWs,
	num,
	resultTextOf,
	str,
	truncate,
} from "../util";
import { formatClock } from "./desktop-record";

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
			{text && (
				<span className="tv-muted">“{truncate(normalizeWs(text), 80)}”</span>
			)}
			{path ? <PathText path={path} /> : <InvalidArg what="output_path" />}
		</>
	);
}

/** 右侧读数：音频大小与编码。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const details = detailsRecord(result);
	const bytes = num(details?.bytes);
	const codec = str(details?.codec);
	return (
		<>
			{codec && <span>{codec.toUpperCase()}</span>}
			{bytes !== null && <span>{formatBytes(bytes)}</span>}
		</>
	);
}

function Input({ args, result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const text = str(args.text);
	const requested = str(args.output_path);
	// 实际写入路径与请求不同（本地后端把 .mp3 改写成 .wav）时，这里标为请求路径，实际路径在输出段
	const saved = savedPath(result);
	const redirected =
		saved !== null && requested !== null && saved !== requested;
	const voice = str(args.voice_id);
	const language = str(args.language);
	const sampleRate = num(args.sample_rate);
	const bitRate = num(args.bit_rate);
	const chips = [
		voice && t("toolRender.tts.voiceIs", { voice }),
		language,
		sampleRate === null ? null : `${sampleRate} Hz`,
		bitRate === null ? null : `${bitRate} bps`,
	].filter((c): c is string => Boolean(c));
	return (
		<>
			{/* 头部会被省略号截断：全文与路径都在这里完整给出 */}
			{text && (
				<blockquote className="tv-tts-quote tv-prose">{text}</blockquote>
			)}
			<div className="tv-tts-out">
				<span className="tv-faint">
					{t(redirected ? "toolRender.tts.requested" : "toolRender.tts.output")}
				</span>
				{requested ? (
					<PathText path={requested} />
				) : (
					<InvalidArg what="output_path" />
				)}
			</div>
			{chips.length > 0 && (
				<span className="tv-badges">
					{chips.map((c) => (
						<Badge key={c}>{c}</Badge>
					))}
				</span>
			)}
		</>
	);
}

/** 播放器：播放 / 暂停、可拖动的进度、已播 / 总时长。加载失败时换成 `failed`。 */
function AudioPlayer({
	src,
	failed: failedView,
}: {
	src: string;
	failed: ReactNode;
}): ReactNode {
	const { t } = useTranslation();
	const ref = useRef<HTMLAudioElement>(null);
	const [playing, setPlaying] = useState(false);
	const [time, setTime] = useState(0);
	const [duration, setDuration] = useState<number | null>(null);
	const [failed, setFailed] = useState(false);
	if (failed) return failedView;
	const toggle = () => {
		const el = ref.current;
		if (!el) return;
		if (el.paused) void el.play().catch(() => setFailed(true));
		else el.pause();
	};
	const pct = duration ? Math.min(100, (time / duration) * 100) : 0;
	return (
		<div className="tv-tts-player" data-playing={playing ? "" : undefined}>
			{/* biome-ignore lint/a11y/useMediaCaption: 合成语音的原文就在输入段，没有单独的字幕轨 */}
			<audio
				ref={ref}
				src={src}
				preload="none"
				data-testid="tts-audio"
				onPlay={() => setPlaying(true)}
				onPause={() => setPlaying(false)}
				onEnded={() => setPlaying(false)}
				onTimeUpdate={(e) => setTime(e.currentTarget.currentTime)}
				onLoadedMetadata={(e) =>
					setDuration(
						Number.isFinite(e.currentTarget.duration)
							? e.currentTarget.duration
							: null,
					)
				}
				onError={() => setFailed(true)}
			/>
			<button
				type="button"
				className="tv-tts-play"
				aria-label={
					playing ? t("toolRender.tts.pause") : t("toolRender.tts.play")
				}
				onClick={toggle}
			>
				<Icon name={playing ? "pause" : "play"} size={13} />
			</button>
			<input
				type="range"
				className="tv-tts-seek"
				min={0}
				max={duration ?? 0}
				step={0.05}
				value={time}
				disabled={duration === null}
				aria-label={t("toolRender.tts.seek")}
				style={{ "--tv-tts-p": `${pct}%` } as CSSProperties}
				onChange={(e) => {
					const el = ref.current;
					const v = Number(e.currentTarget.value);
					if (el) el.currentTime = v;
					setTime(v);
				}}
			/>
			<span className="tv-tts-time">
				{formatClock(time * 1000)}
				<span className="tv-faint">
					{" / "}
					{duration === null ? "–:––" : formatClock(duration * 1000)}
				</span>
			</span>
		</div>
	);
}

function Result({ args, result, host }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return null;
	const text = resultTextOf(result).trim();
	if (result.isError)
		return <ErrorBlock text={text || t("toolRender.generic.failed")} />;
	const details = detailsRecord(result);
	const saved = savedPath(result);
	const path = saved ?? str(args.output_path);
	const src =
		path?.startsWith("/") && host?.orbFileUrl ? host.orbFileUrl(path) : null;
	const voiceId = str(details?.voiceId);
	const facts = [
		str(details?.backend),
		voiceId && voiceId !== str(args.voice_id)
			? t("toolRender.tts.voiceIs", { voice: voiceId })
			: null,
	].filter(Boolean);
	// 正文去掉 `Saved <n> bytes to <path>` 之后的部分：括号里的模型、音色与编码已在次要行；
	// 括号之后还有说明（本地后端的采样率、格式替换原因）时连括号一起给出
	const rest = saved
		? text.replace(/^Saved \d+ bytes to .+? (?=\((?:model|voice)=)/, "")
		: text;
	const extra = saved && !/\)\.?\s*\S/.test(rest) ? "" : rest.trim();
	return (
		<>
			{src && (
				<AudioPlayer
					key={src}
					src={src}
					failed={
						<>
							<Note tone="err">{t("toolRender.tts.loadFailed")}</Note>
							{host?.openOrbFile && path && (
								<div>
									<button
										type="button"
										className="tv-badge tv-badge--accent tv-agent-link"
										onClick={() => host.openOrbFile?.(path)}
									>
										{t("toolRender.desktopRecord.openInFiles")}
									</button>
								</div>
							)}
						</>
					}
				/>
			)}
			{/* 实际路径只在与请求不同（本地后端改存成 .wav）时列出；请求路径在输入段 */}
			{((saved && saved !== str(args.output_path)) || facts.length > 0) && (
				<div className="tv-tts-file">
					<Icon name="waveform" size={12} />
					{saved && saved !== str(args.output_path) && (
						<>
							<Badge tone="warn">{t("toolRender.tts.redirected")}</Badge>
							<PathText path={saved} />
						</>
					)}
					{facts.length > 0 && (
						<span className="tv-faint">{facts.join(" · ")}</span>
					)}
				</div>
			)}
			{extra && <Output text={extra} maxLines={6} bare />}
		</>
	);
}

export const ttsRenderer: ToolRenderer = { Summary, Meta, Input, Result };
