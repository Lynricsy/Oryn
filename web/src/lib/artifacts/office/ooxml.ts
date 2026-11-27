// `@silurus/ooxml` 不打进任何 bundle：运行时从 OfficeAssets.ooxml 目录动态 import 原样发布的 ESM 产物，
// 库里按 `import.meta.url` 解析的 wasm 与 worker 因此落在同一目录（帧里是 `_oryn/viewer/ooxml/`，主站是
// `office/ooxml/`，见 vite.config.ts 的 officeAssets 与 internal/artifacts/host.go）。只引用类型。

import type * as Docx from "@silurus/ooxml/docx";
import type * as Pptx from "@silurus/ooxml/pptx";
import type * as Xlsx from "@silurus/ooxml/xlsx";

export type DocxModule = typeof Docx;
export type PptxModule = typeof Pptx;
export type XlsxModule = typeof Xlsx;

const modules = new Map<string, Promise<unknown>>();

function importOnce<T>(url: string): Promise<T> {
	let p = modules.get(url);
	if (!p) {
		p = import(/* @vite-ignore */ url);
		// 失败（网络、CSP）不缓存，下次重试
		p.catch(() => modules.delete(url));
		modules.set(url, p);
	}
	return p as Promise<T>;
}

/** base 是 OfficeAssets.ooxml。 */
export function loadDocx(base: string): Promise<DocxModule> {
	return importOnce(`${base}docx.mjs`);
}

export function loadPptx(base: string): Promise<PptxModule> {
	return importOnce(`${base}pptx.mjs`);
}

export function loadXlsx(base: string): Promise<XlsxModule> {
	return importOnce(`${base}xlsx.mjs`);
}
