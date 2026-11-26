import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { FileTypeIcon } from "../components/ui/FileTypeIcon";
import { FILE_TYPE_ICONS } from "../components/ui/file-type-icons.gen";
import {
	resolveEntryIconKey,
	resolveFileIconKey,
	resolveFolderIconKey,
} from "./file-icon";
import {
	EXACT_FILE_ICONS,
	EXT_FILE_ICONS,
	FOLDER_NAME_ICONS,
} from "./file-icon-map.gen";

describe("file-icon 解析（Catppuccin 主集 + Material 兜底）", () => {
	it("全部映射表的目标键都存在于内置图标数据中", () => {
		for (const map of [EXACT_FILE_ICONS, EXT_FILE_ICONS, FOLDER_NAME_ICONS]) {
			for (const target of Object.values(map)) {
				expect(Object.hasOwn(FILE_TYPE_ICONS, target)).toBe(true);
			}
		}
	});

	it("精确文件名与复合扩展名优先于单扩展名", () => {
		expect(resolveFileIconKey("package.json")).toBe("package-json");
		expect(resolveFileIconKey("go.mod")).toBe("go-mod");
		expect(resolveFileIconKey("Dockerfile")).toBe("docker");
		expect(resolveFileIconKey("Dockerfile.prod")).toBe("docker");
		expect(resolveFileIconKey("docker-compose.prod.yml")).toBe(
			"docker-compose",
		);
		expect(resolveFileIconKey(".env.local")).toBe("env");
		expect(resolveFileIconKey("types.d.ts")).toBe("typescript-def");
		expect(resolveFileIconKey("App.test.tsx")).toBe("typescript-test");
		expect(resolveFileIconKey("backup.tar.gz")).toBe("zip");
	});

	it("常见语言、办公文档、媒体与 Material 兜底类型均可识别", () => {
		expect(resolveFileIconKey("main.go")).toBe("go");
		expect(resolveFileIconKey("App.tsx")).toBe("typescript-react");
		expect(resolveFileIconKey("README.md")).toBe("readme");
		expect(resolveFileIconKey("report.pdf")).toBe("pdf");
		expect(resolveFileIconKey("spec.docx")).toBe("ms-word");
		expect(resolveFileIconKey("data.xlsx")).toBe("ms-excel");
		expect(resolveFileIconKey("deck.pptx")).toBe("ms-powerpoint");
		expect(resolveFileIconKey("photo.png")).toBe("image");
		expect(resolveFileIconKey("icon.svg")).toBe("svg");
		expect(resolveFileIconKey("demo.mp4")).toBe("video");
		expect(resolveFileIconKey("CLAUDE.md")).toBe("m:claude");
		expect(resolveFileIconKey("openapi.yaml")).toBe("m:openapi");
		expect(resolveFileIconKey("book.epub")).toBe("m:epub");
		expect(resolveFileIconKey("wrangler.toml")).toBe("m:wrangler");
	});

	it("无扩展名文件可按 MIME 类型兜底，未知回落默认图标", () => {
		expect(resolveFileIconKey("blob", "image/png")).toBe("image");
		expect(resolveFileIconKey("blob", "application/pdf")).toBe("pdf");
		expect(resolveFileIconKey("blob", "application/json; charset=utf-8")).toBe(
			"json",
		);
		expect(resolveFileIconKey("blob", "video/mp4")).toBe("video");
		expect(resolveFileIconKey("unknown_file")).toBe("_file");
	});

	it("语义目录区分收起与展开状态，并支持 Material 兜底目录", () => {
		expect(resolveFolderIconKey("src", false)).toBe("folder_src");
		expect(resolveFolderIconKey("src", true)).toBe("folder_src_open");
		expect(resolveFolderIconKey("docs", false)).toBe("folder_docs");
		expect(resolveFolderIconKey("i18n", false)).toBe("folder_locales");
		expect(resolveFolderIconKey("migrations", false)).toBe(
			"m:folder-migrations",
		);
		expect(resolveFolderIconKey("skills", false)).toBe("m:folder-skills");
		expect(resolveFolderIconKey("random_dir", false)).toBe("_folder");
		expect(resolveFolderIconKey("random_dir", true)).toBe("_folder_open");
		expect(resolveEntryIconKey("link", "symlink")).toBe("symlink");
	});

	it("原型属性名（constructor / __proto__ / toString）作目录或文件名时不崩溃且正确回退", () => {
		for (const tricky of [
			"constructor",
			"__proto__",
			"toString",
			"valueOf",
			"hasOwnProperty",
		]) {
			expect(resolveFolderIconKey(tricky, false)).toBe("_folder");
			expect(resolveFolderIconKey(tricky, true)).toBe("_folder_open");
			expect(resolveFileIconKey(tricky)).toBe("_file");
			expect(resolveFileIconKey(`demo.${tricky}`)).toBe("_file");
		}
		const { container } = render(
			<FileTypeIcon name="constructor" kind="dir" open size={15} />,
		);
		expect(container.querySelector("svg")?.getAttribute("data-file-icon")).toBe(
			"_folder_open",
		);
	});

	it("FileTypeIcon 渲染出带主题变量的矢量 SVG", () => {
		const { container } = render(<FileTypeIcon name="src/App.tsx" size={18} />);
		const svg = container.querySelector("svg");
		expect(svg).not.toBeNull();
		expect(svg?.getAttribute("data-file-icon")).toBe("typescript-react");
		expect(svg?.getAttribute("width")).toBe("18");
		expect(svg?.innerHTML).toContain("var(--ctp-");
	});
});
