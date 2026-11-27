/**
 * Office 文件名与 artifact 类型的对应（计划 M31 C1）：导入、「转为 artifact」与菜单共用。纯函数，不依赖应用状态。
 */
import type { Schemas } from "../../../api/client";

export type OfficeType = Exclude<
	Schemas["ArtifactType"],
	"document" | "slides"
>;

/** Office artifact 单文件（即单版本）上限，同服务端 `MaxOfficeFileBytes`。 */
export const MAX_OFFICE_FILE_BYTES = 256 * 1024 * 1024;

/** 每个 Office 类型的文件扩展名（不带点）。 */
export const OFFICE_EXT: Readonly<Record<OfficeType, string>> = {
	docx: "docx",
	docm: "docm",
	xlsx: "xlsx",
	xlsm: "xlsm",
	pptx: "pptx",
	pptm: "pptm",
	pdf: "pdf",
};

const LEGACY_EXT: Readonly<Record<string, true>> = {
	doc: true,
	xls: true,
	ppt: true,
	odt: true,
	ods: true,
	odp: true,
};

function extOf(name: string): string {
	const dot = name.lastIndexOf(".");
	return dot < 0 ? "" : name.slice(dot + 1).toLowerCase();
}

/** 是不是 Office 类型（版本里只有一个原生二进制文件）。 */
export function isOfficeType(type: string | undefined): type is OfficeType {
	return type !== undefined && Object.hasOwn(OFFICE_EXT, type);
}

/** 按小写扩展名回 Office 类型；不认识的回 `null`。 */
export function officeTypeOfName(name: string): OfficeType | null {
	const ext = extOf(name);
	return isOfficeType(ext) ? ext : null;
}

/** 旧版二进制或 OpenDocument 格式（`.doc .xls .ppt .odt .ods .odp`）：登记为 artifact 前需要转换。 */
export function isLegacyOfficeName(name: string): boolean {
	return Object.hasOwn(LEGACY_EXT, extOf(name));
}
