/** 改动标签的未看签名：首次记录即为基线，之后的新签名才算未看；在看改动标签时记为已看。 */
import { beforeEach, describe, expect, it } from "vitest";
import { hasUnseenChanges, uiStore } from "./ui";

const T = "T-ui-changes";
const sig = () => uiStore.getState().changesSig[T];

beforeEach(() => uiStore.setState({ changesSig: {} }));

describe("changesSig", () => {
	it("首次记录作为基线，不提示", () => {
		uiStore.getState().noteChanges(T, "a", false);
		expect(sig()).toEqual({ current: "a", seen: "a" });
		expect(hasUnseenChanges(sig())).toBe(false);
	});

	it("之后的新签名提示，标记已看后清除", () => {
		uiStore.getState().noteChanges(T, "a", false);
		uiStore.getState().noteChanges(T, "b", false);
		expect(hasUnseenChanges(sig())).toBe(true);
		uiStore.getState().markChangesSeen(T);
		expect(sig()).toEqual({ current: "b", seen: "b" });
		expect(hasUnseenChanges(sig())).toBe(false);
	});

	it("seen=true 的记录直接记为已看；改动清空（空签名）不提示", () => {
		uiStore.getState().noteChanges(T, "a", false);
		uiStore.getState().noteChanges(T, "b", true);
		expect(hasUnseenChanges(sig())).toBe(false);
		uiStore.getState().noteChanges(T, "", false);
		expect(sig()).toEqual({ current: "", seen: "b" });
		expect(hasUnseenChanges(sig())).toBe(false);
	});

	it("签名不变时不产生新状态", () => {
		uiStore.getState().noteChanges(T, "a", false);
		const before = uiStore.getState().changesSig;
		uiStore.getState().noteChanges(T, "a", false);
		uiStore.getState().markChangesSeen(T);
		expect(uiStore.getState().changesSig).toBe(before);
	});
});
