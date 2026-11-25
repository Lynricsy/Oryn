/**
 * 选中元素条（M14 3.5）：「标题 › 锚点」的显示（元素、只有标签、整个页面、画布页）与移除钮。
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import i18n from "../../i18n";
import type { ArtifactSelectionRef } from "../../lib/artifacts/prompt";
import { SelectionChip } from "./SelectionChip";

const base: ArtifactSelectionRef = {
	artifactId: "art_01JAAAAAAAAAAAAAAAAAAAAAAA",
	title: "周报",
	version: 2,
	anchor: {
		version: 2,
		path: "index.html",
		page: null,
		offset: 10,
		tag: "button",
		text: "提交",
		missing: false,
	},
};

function label(selection: ArtifactSelectionRef): string {
	const { unmount } = render(
		<SelectionChip selection={selection} onRemove={() => undefined} />,
	);
	const text =
		screen.getByTestId("composer-artifact-selection").querySelector("p")
			?.title ?? "";
	unmount();
	return text;
}

describe("SelectionChip", () => {
	it("显示「标题 › 锚点」", () => {
		expect(label(base)).toBe("周报 › <button>“提交”");
		expect(label({ ...base, anchor: { ...base.anchor, text: "" } })).toBe(
			"周报 › <button>",
		);
		expect(
			label({
				...base,
				anchor: { ...base.anchor, page: { number: 2, name: "首页" } },
			}),
		).toBe("周报 › 首页 › <button>“提交”");
	});

	it("设计画布的选中带 loc 时显示 文件:行", () => {
		render(
			<SelectionChip
				selection={{
					...base,
					anchor: { ...base.anchor, loc: "src/frames/login.tsx:42:7" },
				}}
				onRemove={() => undefined}
			/>,
		);
		expect(
			screen.getByTestId("composer-artifact-selection-loc").textContent,
		).toBe("src/frames/login.tsx:42");
	});

	it("移除钮调 onRemove", () => {
		const onRemove = vi.fn();
		render(<SelectionChip selection={base} onRemove={onRemove} />);
		fireEvent.click(
			screen.getByRole("button", {
				name: i18n.t("artifacts.selection.remove"),
			}),
		);
		expect(onRemove).toHaveBeenCalledOnce();
	});
});
