import { describe, expect, it } from "vitest";
import {
	isLegacyOfficeName,
	isOfficeType,
	OFFICE_EXT,
	officeTypeOfName,
} from "./formats";

describe("Office 文件名与类型", () => {
	it("按小写扩展名认出七个 Office 类型", () => {
		expect(officeTypeOfName("季度报告.docx")).toBe("docx");
		expect(officeTypeOfName("宏.DOCM")).toBe("docm");
		expect(officeTypeOfName("a.b.Xlsx")).toBe("xlsx");
		expect(officeTypeOfName("x.xlsm")).toBe("xlsm");
		expect(officeTypeOfName("deck.pptx")).toBe("pptx");
		expect(officeTypeOfName("deck.pptm")).toBe("pptm");
		expect(officeTypeOfName("scan.PDF")).toBe("pdf");
		for (const name of ["a.doc", "a.html", "docx", "a.", "", "a.constructor"])
			expect(officeTypeOfName(name)).toBeNull();
	});

	it("旧版与 OpenDocument 格式单独认出", () => {
		for (const name of ["a.doc", "b.XLS", "c.ppt", "d.odt", "e.ods", "f.odp"])
			expect(isLegacyOfficeName(name)).toBe(true);
		for (const name of ["a.docx", "a.pdf", "doc", "a.toString"])
			expect(isLegacyOfficeName(name)).toBe(false);
	});

	it("类型与扩展名一一对应", () => {
		for (const [type, ext] of Object.entries(OFFICE_EXT)) {
			expect(isOfficeType(type)).toBe(true);
			expect(officeTypeOfName(`f.${ext}`)).toBe(type);
		}
		expect(isOfficeType("document")).toBe(false);
		expect(isOfficeType(undefined)).toBe(false);
	});
});
