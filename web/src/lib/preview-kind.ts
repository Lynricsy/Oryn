/**
 * 文件预览分流：按扩展名（其次 MIME 与服务端的 `text` 嗅探结果）决定文件查看器用哪种预览。
 * - `text`：CodeMirror 编辑器；`render` 非空时另有渲染视图（Markdown、表格、HTML、Notebook、mermaid、SVG）；
 * - `blob`：整份读入后交给对应的预览组件（PDF、Office、图片解码、字体、压缩包、3D 模型）；
 * - `media`：音视频直接用下载地址流式播放（支持 Range 拖动），不整份读入；
 * - `binary`：只显示类型、大小与下载按钮。
 */

export type TextRender =
	| "markdown"
	| "table"
	| "html"
	| "notebook"
	| "mermaid"
	| "svg";

export type BlobViewer =
	| "image"
	| "heic"
	| "tiff"
	| "psd"
	| "pdf"
	| "docx"
	| "sheet"
	| "slides"
	| "font"
	| "archive"
	| "model";

export type PreviewKind =
	| { kind: "text"; render?: TextRender }
	| { kind: "blob"; viewer: BlobViewer }
	| { kind: "media"; media: "video" | "audio" }
	| { kind: "binary" };

/** 浏览器 `<img>` 原生可解码的位图（SVG 走文本渲染视图）。 */
const NATIVE_IMAGE_EXT = [
	"png",
	"apng",
	"jpg",
	"jpeg",
	"jfif",
	"pjpeg",
	"pjp",
	"gif",
	"webp",
	"avif",
	"bmp",
	"ico",
	"cur",
];

const NATIVE_IMAGE_MIME: Record<string, true> = {
	"image/png": true,
	"image/apng": true,
	"image/jpeg": true,
	"image/gif": true,
	"image/webp": true,
	"image/avif": true,
	"image/bmp": true,
	"image/x-icon": true,
	"image/vnd.microsoft.icon": true,
};

const TEXT_RENDER: Record<string, TextRender> = {
	md: "markdown",
	markdown: "markdown",
	csv: "table",
	tsv: "table",
	html: "html",
	htm: "html",
	xhtml: "html",
	ipynb: "notebook",
	mmd: "mermaid",
	mermaid: "mermaid",
	svg: "svg",
};

const BLOB_VIEWER: Record<string, BlobViewer> = {
	...Object.fromEntries(NATIVE_IMAGE_EXT.map((e) => [e, "image" as const])),
	heic: "heic",
	heif: "heic",
	tif: "tiff",
	tiff: "tiff",
	psd: "psd",
	pdf: "pdf",
	docx: "docx",
	docm: "docx",
	dotx: "docx",
	dotm: "docx",
	xlsx: "sheet",
	xlsm: "sheet",
	xlsb: "sheet",
	xls: "sheet",
	xltx: "sheet",
	ods: "sheet",
	fods: "sheet",
	numbers: "sheet",
	pptx: "slides",
	pptm: "slides",
	ppsx: "slides",
	potx: "slides",
	ttf: "font",
	otf: "font",
	woff: "font",
	woff2: "font",
	zip: "archive",
	jar: "archive",
	war: "archive",
	ear: "archive",
	apk: "archive",
	aar: "archive",
	ipa: "archive",
	xpi: "archive",
	whl: "archive",
	nupkg: "archive",
	vsix: "archive",
	tar: "archive",
	tgz: "archive",
	glb: "model",
	gltf: "model",
	stl: "model",
	obj: "model",
	ply: "model",
};

const MEDIA_EXT: Record<string, "video" | "audio"> = {
	mp4: "video",
	m4v: "video",
	webm: "video",
	mov: "video",
	ogv: "video",
	mkv: "video",
	mp3: "audio",
	wav: "audio",
	ogg: "audio",
	oga: "audio",
	opus: "audio",
	flac: "audio",
	m4a: "audio",
	aac: "audio",
	weba: "audio",
};

/** 小写扩展名；`.tar.gz` 视为 `tgz`，无扩展名或隐藏文件（`.bashrc`）为空串。 */
export function extOf(name: string): string {
	const base = name.slice(name.lastIndexOf("/") + 1).toLowerCase();
	if (base.endsWith(".tar.gz")) return "tgz";
	const i = base.lastIndexOf(".");
	return i <= 0 ? "" : base.slice(i + 1);
}

/** 只取表自身的键：扩展名来自不可信的文件名，`x.constructor` 不能命中原型上的属性。 */
function own<V>(table: Record<string, V>, key: string): V | undefined {
	return Object.hasOwn(table, key) ? table[key] : undefined;
}

export function previewKind(
	name: string,
	mime: string | undefined,
	text: boolean | undefined,
): PreviewKind {
	const ext = extOf(name);
	const render = own(TEXT_RENDER, ext);
	// 渲染视图要求内容是 UTF-8 文本（UTF-16 的 CSV 之类只能下载）；未被嗅探为文本的 SVG 仍按图片交给 <img> 解析
	if (render) {
		if (text) return { kind: "text", render };
		return render === "svg"
			? { kind: "blob", viewer: "image" }
			: { kind: "binary" };
	}
	const viewer = own(BLOB_VIEWER, ext);
	if (viewer) return { kind: "blob", viewer };
	const media = own(MEDIA_EXT, ext);
	if (media) return { kind: "media", media };
	if (mime && own(NATIVE_IMAGE_MIME, mime))
		return { kind: "blob", viewer: "image" };
	if (text) return { kind: "text" };
	if (mime === "application/pdf") return { kind: "blob", viewer: "pdf" };
	if (mime?.startsWith("video/")) return { kind: "media", media: "video" };
	if (mime?.startsWith("audio/")) return { kind: "media", media: "audio" };
	return { kind: "binary" };
}

/** 位图扩展名对应的 MIME（下载接口一律回 octet-stream，`<img>` 要靠它识别 SVG 之外的类型时也更稳）。 */
export function imageMime(name: string): string | undefined {
	const ext = extOf(name);
	switch (ext) {
		case "png":
		case "apng":
			return "image/png";
		case "jpg":
		case "jpeg":
		case "jfif":
		case "pjpeg":
		case "pjp":
			return "image/jpeg";
		case "gif":
			return "image/gif";
		case "webp":
			return "image/webp";
		case "avif":
			return "image/avif";
		case "bmp":
			return "image/bmp";
		case "ico":
		case "cur":
			return "image/x-icon";
		default:
			return undefined;
	}
}
