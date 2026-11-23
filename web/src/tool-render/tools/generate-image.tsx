/**
 * `generate_image` — 结构化的生图提示词与生成的图片（oryn-core `tools/image-gen.ts`）。
 * 生成的图片在 `details.images`（不进模型上下文），这里并进结果再出缩略图。
 * 头部：主体 + 比例 / 编辑徽标，读数是生成的张数。输入段把提示词排成一张「提示词卡」（主体在前，
 * 动作、场景、构图、光线、风格逐行，要渲染的文字、修改项与输入图另列）；输出段是大图画廊，
 * 运行中按请求的比例给一个占位框，provider · model 与保存路径收在次要行。
 */
import type { CSSProperties, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	Badge,
	ErrorBlock,
	InvalidArg,
	PathText,
	ResultImages,
	ResultText,
} from "../parts";
import type {
	ToolRenderer,
	ToolRenderProps,
	ToolResultBlock,
	ToolResultLike,
} from "../types";
import {
	detailsRecord,
	isRecord,
	normalizeWs,
	num,
	resultImagesOf,
	resultTextOf,
	str,
	truncate,
} from "../util";

/** Result with `details.images` (`{data, mimeType}[]`) appended as image blocks. */
function withDetailImages(
	result: ToolResultLike | undefined,
): ToolResultLike | undefined {
	const details = detailsRecord(result);
	if (!result || !details || !Array.isArray(details.images)) return result;
	const extra: ToolResultBlock[] = [];
	for (const img of details.images) {
		if (
			isRecord(img) &&
			typeof img.data === "string" &&
			typeof img.mimeType === "string"
		) {
			extra.push({ type: "image", data: img.data, mimeType: img.mimeType });
		}
	}
	if (extra.length === 0) return result;
	return {
		content: [...result.content, ...extra],
		details: result.details,
		isError: result.isError,
	};
}

function Summary({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const subject = str(args.subject);
	const aspect = str(args.aspect_ratio);
	const changes = Array.isArray(args.changes) ? args.changes.length : 0;
	return (
		<>
			{subject ? (
				<span>{truncate(normalizeWs(subject), 80)}</span>
			) : (
				args.subject !== undefined && <InvalidArg what="subject" />
			)}
			{aspect && <Badge>{aspect}</Badge>}
			{changes > 0 && (
				<Badge>{t("toolRender.generateImage.edits", { count: changes })}</Badge>
			)}
		</>
	);
}

/** 右侧读数：生成的张数。 */
function Meta({ result }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result || result.isError) return null;
	const count =
		num(detailsRecord(result)?.imageCount) ??
		resultImagesOf(withDetailImages(result)).length;
	return count > 0 ? (
		<span>{t("toolRender.generateImage.images", { count })}</span>
	) : null;
}

/** 提示词卡里逐行列出的描述字段（主体单独在最前）。 */
const PROMPT_FIELDS = [
	"action",
	"scene",
	"composition",
	"lighting",
	"style",
] as const;

function Input({ args }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	const subject = str(args.subject);
	const text = str(args.text);
	const changes = Array.isArray(args.changes) ? args.changes : null;
	const inputs = Array.isArray(args.input) ? args.input : null;
	const fields = PROMPT_FIELDS.filter((f) => args[f] !== undefined);
	const chips = [
		str(args.aspect_ratio),
		str(args.image_size),
		str(args.model),
	].filter((c): c is string => c !== null);
	return (
		<div className="tv-gi-prompt">
			{subject ? (
				<div className="tv-gi-subject">{subject}</div>
			) : (
				args.subject !== undefined && <InvalidArg what="subject" />
			)}
			{(fields.length > 0 ||
				text !== null ||
				(changes && changes.length > 0) ||
				(inputs && inputs.length > 0)) && (
				<dl className="tv-gi-fields">
					{fields.map((f) => (
						<div key={f} className="tv-gi-field">
							<dt>{t(`toolRender.generateImage.field.${f}`)}</dt>
							<dd>{str(args[f]) ?? <InvalidArg what={f} />}</dd>
						</div>
					))}
					{text !== null && (
						<div className="tv-gi-field">
							<dt>{t("toolRender.generateImage.field.text")}</dt>
							<dd className="tv-gi-quote">“{text}”</dd>
						</div>
					)}
					{changes && changes.length > 0 && (
						<div className="tv-gi-field">
							<dt>{t("toolRender.generateImage.field.changes")}</dt>
							<dd>
								<ul className="tv-gi-changes">
									{changes.map((change, i) => (
										<li key={i}>
											{typeof change === "string" ? (
												change
											) : (
												<InvalidArg what="change" />
											)}
										</li>
									))}
								</ul>
							</dd>
						</div>
					)}
					{inputs && inputs.length > 0 && (
						<div className="tv-gi-field">
							<dt>{t("toolRender.generateImage.field.input")}</dt>
							<dd className="tv-gi-inputs">
								{inputs.map((input, i) => {
									const path = isRecord(input) ? str(input.path) : null;
									const mime = isRecord(input) ? str(input.mime_type) : null;
									return (
										<div key={i}>
											{!isRecord(input) ? (
												<InvalidArg what="input" />
											) : path ? (
												<PathText path={path} />
											) : (
												<span className="tv-muted">
													{t("toolRender.generateImage.inlineImage")}
													{mime && ` · ${mime}`}
												</span>
											)}
										</div>
									);
								})}
							</dd>
						</div>
					)}
				</dl>
			)}
			{chips.length > 0 && (
				<span className="tv-badges">
					{chips.map((c) => (
						<Badge key={c}>{c}</Badge>
					))}
				</span>
			)}
		</div>
	);
}

/** `16:9` → CSS 的 `16 / 9`；认不出时为 null。 */
function aspectOf(args: Record<string, unknown>): string | null {
	const ratio = /^(\d+):(\d+)$/.exec(str(args.aspect_ratio) ?? "");
	if (ratio) return `${ratio[1]} / ${ratio[2]}`;
	const size = /^(\d+)x(\d+)$/.exec(str(args.image_size) ?? "");
	return size ? `${size[1]} / ${size[2]}` : null;
}

/** 运行中：按请求的比例画一个占位框，里面一道微光缓慢扫过。 */
function Pending({ args }: { args: Record<string, unknown> }): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			className="tv-gi-pending"
			style={{ "--tv-gi-aspect": aspectOf(args) ?? "1 / 1" } as CSSProperties}
			role="img"
			aria-label={t("toolRender.generateImage.generating")}
		>
			<span>{t("toolRender.generateImage.generating")}</span>
		</div>
	);
}

function Result({ args, result, running }: ToolRenderProps): ReactNode {
	const { t } = useTranslation();
	if (!result) return running ? <Pending args={args} /> : null;
	if (result.isError)
		return (
			<ErrorBlock
				text={resultTextOf(result) || t("toolRender.generic.failed")}
				maxLines={10}
			/>
		);
	const details = detailsRecord(result);
	const provider = str(details?.provider);
	const model = str(details?.model);
	const note =
		str(details?.revisedPrompt)?.trim() || str(details?.responseText)?.trim();
	const paths: string[] = [];
	if (details && Array.isArray(details.imagePaths)) {
		for (const p of details.imagePaths) {
			if (typeof p === "string") paths.push(p);
		}
	}
	const merged = withDetailImages(result);
	const hasImages = resultImagesOf(merged).length > 0;
	return (
		<>
			{hasImages && (
				<div className="tv-gi-gallery">
					<ResultImages result={merged} />
				</div>
			)}
			{note && <blockquote className="tv-gi-note">{note}</blockquote>}
			{(provider || model || paths.length > 0) && (
				<div className="tv-gi-foot">
					{(provider || model) && (
						<span className="tv-gi-model">
							{[provider, model].filter(Boolean).join(" · ")}
						</span>
					)}
					{paths.map((p) => (
						<PathText key={p} path={p} />
					))}
				</div>
			)}
			{!hasImages && <ResultText result={result} maxLines={8} />}
		</>
	);
}

export const generateImageRenderer: ToolRenderer = {
	Summary,
	Meta,
	Input,
	Result,
};
