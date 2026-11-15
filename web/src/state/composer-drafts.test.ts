/** 输入区草稿 store：按线程各自保存；挂载登记按引用计数，重新挂载时新旧交替不误判离开。 */
import { beforeEach, describe, expect, it } from "vitest";
import {
	claimComposer,
	composerMounted,
	getComposerDraft,
	patchComposerDraft,
	resetComposerDrafts,
} from "./composer-drafts";

beforeEach(() => resetComposerDrafts());

describe("composer-drafts", () => {
	it("草稿按线程各自保存，局部更新不动其它字段", () => {
		patchComposerDraft("A", { text: "a" });
		patchComposerDraft("B", { text: "b", error: new Error("x") });
		patchComposerDraft("A", { error: "oops" });
		expect(getComposerDraft("A")).toMatchObject({ text: "a", error: "oops" });
		expect(getComposerDraft("B").text).toBe("b");
		expect(getComposerDraft("C")).toEqual({
			text: "",
			error: null,
			selection: null,
			editorText: null,
			inFlight: null,
		});
	});

	it("挂载登记：新的先登记、旧的后注销（重新挂载）仍算挂着；全部注销后才算离开", () => {
		const oldOne = claimComposer("A");
		const newOne = claimComposer("A");
		oldOne();
		expect(composerMounted("A")).toBe(true);
		newOne();
		expect(composerMounted("A")).toBe(false);
		expect(composerMounted("B")).toBe(false);
	});
});
