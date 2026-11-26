/**
 * Jupyter Notebook（nbformat 4）只读渲染：Markdown 单元走站内 Markdown 渲染，代码单元按内核语言高亮，
 * 输出按 MIME 优先级取一种：位图 / SVG（`data:` URL 的 `<img>`）→ HTML（惰性 iframe）→ Markdown → LaTeX → JSON → 纯文本；
 * 错误输出去掉 ANSI 转义后显示 traceback。
 */
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { CodeBlock, Markdown } from "../../markdown/Markdown";
import { ImageViewer } from "../../ui/ImageViewer";
import { InertFrame } from "./InertFrame";

type MultiLine = string | string[];

interface NbOutput {
	output_type: "stream" | "execute_result" | "display_data" | "error";
	name?: string;
	text?: MultiLine;
	data?: Record<string, MultiLine | object>;
	ename?: string;
	evalue?: string;
	traceback?: string[];
	execution_count?: number | null;
}

interface NbCell {
	cell_type: "markdown" | "code" | "raw";
	source: MultiLine;
	outputs?: NbOutput[];
	execution_count?: number | null;
}

interface Notebook {
	cells: NbCell[];
	metadata?: {
		kernelspec?: { language?: string; display_name?: string };
		language_info?: { name?: string };
	};
}

const ANSI = new RegExp(`${String.fromCharCode(27)}\\[[0-9;?]*[A-Za-z]`, "g");

/** 纯文本输出（stream / text/plain / traceback / raw 单元）的样式。 */
const TEXT_BLOCK =
	"whitespace-pre-wrap break-words rounded-md px-2.5 py-1.5 font-mono text-xs";

function joined(v: MultiLine | undefined): string {
	return Array.isArray(v) ? v.join("") : (v ?? "");
}

function isObject(v: unknown): v is Record<string, unknown> {
	return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** 字符串或字符串数组；其它形状（数字、对象、混入非字符串的数组）一律视为缺省。 */
function multiLine(v: unknown): MultiLine | undefined {
	if (typeof v === "string") return v;
	if (Array.isArray(v) && v.every((s) => typeof s === "string")) return v;
	return undefined;
}

function optString(v: unknown): string | undefined {
	return typeof v === "string" ? v : undefined;
}

function optCount(v: unknown): number | null | undefined {
	return typeof v === "number" || v === null ? v : undefined;
}

function parseOutput(v: unknown): NbOutput | undefined {
	if (!isObject(v)) return undefined;
	const type = v.output_type;
	if (
		type !== "stream" &&
		type !== "execute_result" &&
		type !== "display_data" &&
		type !== "error"
	)
		return undefined;
	const traceback = v.traceback;
	return {
		output_type: type,
		name: optString(v.name),
		text: multiLine(v.text),
		data: isObject(v.data)
			? (v.data as Record<string, MultiLine | object>)
			: undefined,
		ename: optString(v.ename),
		evalue: optString(v.evalue),
		traceback:
			Array.isArray(traceback) && traceback.every((s) => typeof s === "string")
				? traceback
				: undefined,
		execution_count: optCount(v.execution_count),
	};
}

/**
 * 解析并规范化 nbformat 4：文件内容不可信，渲染只依赖这里产出的形状。单元不是对象或类型未知时整本判为无效；
 * 字段形状不对的按缺省处理，无法识别的输出丢弃。
 */
function parseNotebook(text: string): Notebook {
	const raw: unknown = JSON.parse(text);
	if (!isObject(raw) || !Array.isArray(raw.cells))
		throw new Error("not an nbformat 4 notebook");
	const cells = raw.cells.map((c, i): NbCell => {
		if (!isObject(c)) throw new Error(`cell ${i + 1} is not an object`);
		const type = c.cell_type;
		if (type !== "markdown" && type !== "code" && type !== "raw")
			throw new Error(`cell ${i + 1} has an unknown cell_type`);
		return {
			cell_type: type,
			source: multiLine(c.source) ?? "",
			outputs: Array.isArray(c.outputs)
				? c.outputs.flatMap((o) => parseOutput(o) ?? [])
				: undefined,
			execution_count: optCount(c.execution_count),
		};
	});
	const meta = isObject(raw.metadata) ? raw.metadata : {};
	const ks = isObject(meta.kernelspec) ? meta.kernelspec : {};
	const li = isObject(meta.language_info) ? meta.language_info : {};
	return {
		cells,
		metadata: {
			kernelspec: {
				language: optString(ks.language),
				display_name: optString(ks.display_name),
			},
			language_info: { name: optString(li.name) },
		},
	};
}

const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];

function RichOutput({
	data,
	title,
}: {
	data: Record<string, MultiLine | object>;
	title: string;
}): ReactNode {
	const { t } = useTranslation();
	const [viewingSrc, setViewingSrc] = useState<string | null>(null);
	const str = (k: string) => {
		const v = data[k];
		return typeof v === "string" || Array.isArray(v)
			? joined(v as MultiLine)
			: undefined;
	};
	const renderZoomable = (src: string) => (
		<>
			<button
				type="button"
				onClick={() => setViewingSrc(src)}
				aria-label={t("image.lightbox.fullscreen")}
				title={t("image.lightbox.fullscreen")}
				className="block max-w-full cursor-zoom-in rounded-sm focus-visible:outline-2 focus-visible:outline-[var(--ring)] focus-visible:outline-offset-2"
			>
				<img src={src} alt={title} className="block max-w-full bg-white" />
			</button>
			{viewingSrc && (
				<ImageViewer
					src={viewingSrc}
					alt={title}
					caption={title}
					onClose={() => setViewingSrc(null)}
				/>
			)}
		</>
	);
	for (const type of IMAGE_TYPES) {
		const b64 = str(type);
		if (b64)
			return renderZoomable(`data:${type};base64,${b64.replace(/\s+/g, "")}`);
	}
	const svg = str("image/svg+xml");
	if (svg)
		return renderZoomable(
			`data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`,
		);
	const html = str("text/html");
	if (html) return <InertFrame html={html} title={title} autoHeight />;
	const md = str("text/markdown");
	if (md) return <Markdown text={md} />;
	const latex = str("text/latex");
	if (latex) return <Markdown text={latex} />;
	const json = data["application/json"];
	if (json !== undefined)
		return <CodeBlock code={JSON.stringify(json, null, 2)} lang="json" />;
	const plain = str("text/plain");
	if (plain !== undefined)
		return (
			<pre className={`${TEXT_BLOCK} bg-inset`}>{plain.replace(ANSI, "")}</pre>
		);
	return null;
}

function Output({ out, title }: { out: NbOutput; title: string }): ReactNode {
	switch (out.output_type) {
		case "stream":
			return (
				<pre
					className={`${TEXT_BLOCK} ${out.name === "stderr" ? "bg-warn/8 text-warn" : "bg-inset"}`}
				>
					{joined(out.text).replace(ANSI, "")}
				</pre>
			);
		case "error":
			return (
				<pre className={`${TEXT_BLOCK} bg-err/8 text-err`}>
					{(out.traceback?.join("\n") ?? `${out.ename}: ${out.evalue}`).replace(
						ANSI,
						"",
					)}
				</pre>
			);
		default:
			return out.data ? <RichOutput data={out.data} title={title} /> : null;
	}
}

export default function NotebookPreview({ text }: { text: string }): ReactNode {
	const { t } = useTranslation();
	const parsed = useMemo(() => {
		try {
			return { nb: parseNotebook(text) };
		} catch (e) {
			return { error: e };
		}
	}, [text]);
	if (!parsed.nb)
		return (
			<p className="p-3 text-err text-sm">
				{t("files.preview.notebook.invalid", {
					reason: String(parsed.error),
				})}
			</p>
		);
	const { nb } = parsed;
	const lang =
		nb.metadata?.kernelspec?.language ??
		nb.metadata?.language_info?.name ??
		"python";
	return (
		<div className="h-full overflow-y-auto px-4 py-3">
			{nb.metadata?.kernelspec?.display_name && (
				<p className="mb-3 text-faint text-xs">
					{nb.metadata.kernelspec.display_name}
				</p>
			)}
			{nb.cells.length === 0 && (
				<p className="text-faint text-sm">
					{t("files.preview.notebook.empty")}
				</p>
			)}
			<div className="flex flex-col gap-3">
				{nb.cells.map((cell, i) => {
					const src = joined(cell.source);
					const key = `${i}:${cell.cell_type}`;
					if (cell.cell_type === "markdown")
						return (
							<div key={key} className="min-w-0">
								<Markdown text={src} />
							</div>
						);
					if (cell.cell_type === "raw")
						return (
							<pre key={key} className={`${TEXT_BLOCK} bg-inset`}>
								{src}
							</pre>
						);
					const count = cell.execution_count ?? " ";
					return (
						<div key={key} className="flex min-w-0 gap-2">
							<span className="w-12 shrink-0 pt-2 text-right font-mono text-faint text-xs">
								[{count}]
							</span>
							<div className="flex min-w-0 flex-1 flex-col gap-1.5">
								<CodeBlock code={src} lang={lang} />
								{cell.outputs?.map((out, j) => (
									<div
										// biome-ignore lint/suspicious/noArrayIndexKey: 输出没有稳定标识，只读列表顺序不变
										key={j}
										className="min-w-0 overflow-x-auto"
									>
										<Output
											out={out}
											title={t("files.preview.notebook.output", { n: i + 1 })}
										/>
									</div>
								))}
							</div>
						</div>
					);
				})}
			</div>
		</div>
	);
}
