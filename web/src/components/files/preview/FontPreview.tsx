/**
 * 字体预览（ttf / otf / woff / woff2）：用 FontFace 把文件注册成一个仅本组件使用的唯一字族，展示可编辑的样张、
 * 多个字号的样张行与 A–Z a–z 0–9 字形表。元数据（字族、样式、版本、设计者、字形数）从 OpenType `name` / `maxp` 表读取：
 * TrueType / CFF 直接读，WOFF 1.0 按需 zlib 解压对应表；WOFF2 为 Brotli 压缩，不解析，退回用文件名。
 */
import { unzlibSync } from "fflate";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ErrorText, Spinner, TextInput } from "../../ui/primitives";
import type { BlobPreviewProps } from "./types";

const DEFAULT_SAMPLE =
	"The quick brown fox jumps over the lazy dog 敏捷的棕色狐狸跳过了懒狗 0123456789";
const SIZES = [12, 16, 24, 36, 48, 72];
const GLYPHS = [
	..."ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	..."abcdefghijklmnopqrstuvwxyz",
	..."0123456789",
];

/** 每次加载分配新字族名，避免与页面字体及其他预览实例冲突。 */
let familySeq = 0;

type FontFormat = "truetype" | "opentype" | "woff" | "woff2" | "unknown";

interface FontMeta {
	format: FontFormat;
	family?: string;
	style?: string;
	version?: string;
	designer?: string;
	glyphs?: number;
}

type State =
	| { status: "loading" }
	| { status: "error" }
	| { status: "ready"; family: string; bytes: Uint8Array };

export default function FontPreview({
	blob,
	name,
}: BlobPreviewProps): ReactNode {
	const { t, i18n } = useTranslation();
	const [state, setState] = useState<State>({ status: "loading" });
	const [sample, setSample] = useState(DEFAULT_SAMPLE);

	useEffect(() => {
		let cancelled = false;
		let face: FontFace | null = null;
		setState({ status: "loading" });
		(async () => {
			const buf = await blob.arrayBuffer();
			const family = `oryn-font-preview-${++familySeq}`;
			// FontFace 会复制传入的数据，原缓冲区仍可用于读取元数据
			const f = new FontFace(family, buf);
			face = f;
			await f.load();
			if (cancelled) return;
			document.fonts.add(f);
			setState({ status: "ready", family, bytes: new Uint8Array(buf) });
		})().catch(() => {
			if (!cancelled) setState({ status: "error" });
		});
		return () => {
			cancelled = true;
			if (face) document.fonts.delete(face);
		};
	}, [blob]);

	const bytes = state.status === "ready" ? state.bytes : null;
	const meta = useMemo(
		() => (bytes ? parseFontMeta(bytes, i18n.language) : null),
		[bytes, i18n.language],
	);

	if (state.status === "loading") {
		return (
			<div className="flex h-full items-center justify-center text-faint">
				<Spinner />
			</div>
		);
	}
	if (state.status === "error") {
		return (
			<div className="p-3">
				<ErrorText error={new Error(t("files.preview.font.invalid"))} />
			</div>
		);
	}

	const fontStyle = { fontFamily: `"${state.family}"` };
	const title = meta?.family || name.replace(/\.[^.]+$/, "");
	const details = [
		meta?.style,
		meta && meta.format !== "unknown"
			? t(`files.preview.font.format.${meta.format}`)
			: undefined,
		meta?.version,
		meta?.glyphs !== undefined
			? t("files.preview.font.glyphCount", { count: meta.glyphs })
			: undefined,
	].filter(Boolean);

	return (
		<div className="h-full min-h-0 overflow-y-auto">
			<div className="flex flex-col gap-5 px-4 py-4">
				<header className="flex flex-col gap-1">
					<h2
						className="break-words text-3xl text-fg leading-tight"
						style={fontStyle}
					>
						{title}
					</h2>
					{details.length > 0 && (
						<p className="text-muted text-xs">{details.join(" · ")}</p>
					)}
					{meta?.designer && (
						<p className="text-faint text-xs">
							{t("files.preview.font.designer", { name: meta.designer })}
						</p>
					)}
				</header>

				<TextInput
					value={sample}
					onChange={(e) => setSample(e.target.value)}
					placeholder={t("files.preview.font.samplePlaceholder")}
					aria-label={t("files.preview.font.sample")}
				/>

				<section className="flex flex-col divide-y divide-border">
					{SIZES.map((px) => (
						<div key={px} className="flex items-baseline gap-3 py-2">
							<span className="w-10 shrink-0 text-right text-2xs text-faint tabular-nums">
								{px}px
							</span>
							<p
								className="min-w-0 flex-1 break-words text-fg leading-tight"
								style={{ ...fontStyle, fontSize: px }}
							>
								{sample || DEFAULT_SAMPLE}
							</p>
						</div>
					))}
				</section>

				<section className="flex flex-col gap-2">
					<h3 className="text-faint text-xs">
						{t("files.preview.font.glyphs")}
					</h3>
					<div className="grid grid-cols-[repeat(auto-fill,minmax(2.75rem,1fr))] overflow-hidden rounded-md border border-border bg-border gap-px">
						{GLYPHS.map((ch) => (
							<div
								key={ch}
								className="flex aspect-square items-center justify-center bg-inset text-2xl text-fg"
								style={fontStyle}
							>
								{ch}
							</div>
						))}
					</div>
				</section>
			</div>
		</div>
	);
}

// ─────────────────────────── OpenType 元数据 ───────────────────────────

/** 读出指定表的原始字节；WOFF 1.0 压缩过的表在这里解压。只取元数据要用的表。 */
function readTables(
	b: Uint8Array,
	wanted: readonly string[],
): { format: FontFormat; tables: Map<string, Uint8Array> } {
	const v = new DataView(b.buffer, b.byteOffset, b.byteLength);
	const tables = new Map<string, Uint8Array>();
	const tagAt = (off: number) =>
		String.fromCharCode(
			b[off] ?? 0,
			b[off + 1] ?? 0,
			b[off + 2] ?? 0,
			b[off + 3] ?? 0,
		);
	const sig = tagAt(0);

	if (sig === "wOF2") return { format: "woff2", tables };
	if (sig === "wOFF") {
		const n = v.getUint16(12);
		for (let i = 0; i < n; i++) {
			const rec = 44 + i * 20;
			const tag = tagAt(rec);
			if (!wanted.includes(tag)) continue;
			const off = v.getUint32(rec + 4);
			const compLen = v.getUint32(rec + 8);
			const origLen = v.getUint32(rec + 12);
			const raw = b.subarray(off, off + compLen);
			tables.set(tag, compLen < origLen ? unzlibSync(raw) : raw);
		}
		return { format: "woff", tables };
	}

	const isTrueType = sig === "\0\x01\0\0" || sig === "true";
	if (!isTrueType && sig !== "OTTO") return { format: "unknown", tables };
	const n = v.getUint16(4);
	for (let i = 0; i < n; i++) {
		const rec = 12 + i * 16;
		const tag = tagAt(rec);
		if (!wanted.includes(tag)) continue;
		const off = v.getUint32(rec + 8);
		const len = v.getUint32(rec + 12);
		tables.set(tag, b.subarray(off, off + len));
	}
	return { format: isTrueType ? "truetype" : "opentype", tables };
}

/** 界面语言对应的 Windows 语言 ID 优先级（中文界面先简体、再繁体，最后英语）；其余情况取美式英语。 */
function preferredLangIds(lang: string): number[] {
	return lang.startsWith("zh") ? [0x0804, 0x0404, 0x0c04, 0x0409] : [0x0409];
}

const utf16be = new TextDecoder("utf-16be");
const macRoman = new TextDecoder("macintosh");

/**
 * `name` 表：按 nameID 取一条最合适的记录。Windows 平台（3）优先、按界面语言排序；其次 Unicode 平台（0）；
 * 最后 Mac Roman（1/0）。前两者为 UTF-16BE。
 */
function parseNames(t: Uint8Array, lang: string): Map<number, string> {
	const v = new DataView(t.buffer, t.byteOffset, t.byteLength);
	const count = v.getUint16(2);
	const strOff = v.getUint16(4);
	const langs = preferredLangIds(lang);
	const best = new Map<number, { score: number; text: string }>();
	for (let i = 0; i < count; i++) {
		const rec = 6 + i * 12;
		const platform = v.getUint16(rec);
		const encoding = v.getUint16(rec + 2);
		const language = v.getUint16(rec + 4);
		const id = v.getUint16(rec + 6);
		const len = v.getUint16(rec + 8);
		const off = v.getUint16(rec + 10);
		let score: number;
		if (platform === 3) {
			const rank = langs.indexOf(language);
			score = rank >= 0 ? rank : 50;
		} else if (platform === 0) score = 100;
		else if (platform === 1 && encoding === 0) score = 200;
		else continue;
		const prev = best.get(id);
		if (prev && prev.score <= score) continue;
		const raw = t.subarray(strOff + off, strOff + off + len);
		const text = (platform === 1 ? macRoman : utf16be).decode(raw).trim();
		if (text) best.set(id, { score, text });
	}
	return new Map([...best].map(([id, { text }]) => [id, text]));
}

/** 解析失败（截断、非标准结构）时只返回已知的格式，元数据本就是可选展示。 */
function parseFontMeta(b: Uint8Array, lang: string): FontMeta {
	let format: FontFormat = "unknown";
	try {
		const r = readTables(b, ["name", "maxp"]);
		format = r.format;
		const meta: FontMeta = { format };
		const name = r.tables.get("name");
		if (name) {
			const names = parseNames(name, lang);
			// 16/17 为排版用字族/样式名（多字重字族的真实分组），缺省时用 1/2
			meta.family = names.get(16) ?? names.get(1);
			meta.style = names.get(17) ?? names.get(2);
			meta.version = names.get(5);
			meta.designer = names.get(9);
		}
		const maxp = r.tables.get("maxp");
		if (maxp && maxp.length >= 6) {
			meta.glyphs = new DataView(
				maxp.buffer,
				maxp.byteOffset,
				maxp.byteLength,
			).getUint16(4);
		}
		return meta;
	} catch {
		return { format };
	}
}
