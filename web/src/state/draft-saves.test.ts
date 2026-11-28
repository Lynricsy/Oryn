import { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, type Schemas } from "../api/client";
import { threadDraftsKey } from "../api/draft-queries";
import {
	flushDraftSaves,
	hasPendingDraftSave,
	hasUnsavedDrafts,
	resetDraft,
	scheduleDraftSave,
} from "./draft-saves";
import { dismissToast, toastsStore } from "./toasts";

type ThreadDraft = Schemas["ThreadDraft"];

const ok = (data: unknown) =>
	({ data, response: new Response(null, { status: 200 }) }) as never;
const fail = (status: number, code: string) =>
	({
		error: { code, message: code },
		response: new Response(null, { status }),
	}) as never;

function savedFrom(
	path: { entryId: string; block: string },
	body: { kind: ThreadDraft["kind"]; title: string; text: string },
	updatedAt = 1000,
): ThreadDraft {
	return { entryId: path.entryId, block: path.block, ...body, updatedAt };
}

const payload = (text: string) => ({
	kind: "email" as const,
	title: "周五会议改期",
	text,
});

describe("draft-saves", () => {
	const putSpy = vi.spyOn(api, "PUT");
	const delSpy = vi.spyOn(api, "DELETE");
	let qc: QueryClient;

	beforeEach(() => {
		vi.useFakeTimers();
		putSpy.mockReset();
		delSpy.mockReset();
		putSpy.mockImplementation(async (_path, init) => {
			const { params, body } = init as never as {
				params: { path: { entryId: string; block: string } };
				body: { kind: ThreadDraft["kind"]; title: string; text: string };
			};
			return ok(savedFrom(params.path, body));
		});
		qc = new QueryClient();
	});

	afterEach(() => {
		vi.useRealTimers();
		for (const t of toastsStore.getState().toasts) dismissToast(t.id);
	});

	it("同一块 800 ms 防抖合并，只发最新一版并写回缓存", async () => {
		qc.setQueryData(threadDraftsKey("T1"), { drafts: [] });
		scheduleDraftSave(qc, "T1", "e1", "1.0", payload("a"));
		await vi.advanceTimersByTimeAsync(500);
		scheduleDraftSave(qc, "T1", "e1", "1.0", payload("ab"));
		await vi.advanceTimersByTimeAsync(500);
		expect(putSpy).not.toHaveBeenCalled();
		expect(hasPendingDraftSave("T1", "e1", "1.0")).toBe(true);
		expect(hasUnsavedDrafts("T1")).toBe(true);
		await vi.advanceTimersByTimeAsync(300);
		await flushDraftSaves("T1");
		expect(putSpy).toHaveBeenCalledTimes(1);
		expect(putSpy.mock.calls[0]?.[1]).toMatchObject({
			params: { path: { id: "T1", entryId: "e1", block: "1.0" } },
			body: payload("ab"),
		});
		expect(qc.getQueryData(threadDraftsKey("T1"))).toEqual({
			drafts: [savedFrom({ entryId: "e1", block: "1.0" }, payload("ab"))],
		});
		expect(hasPendingDraftSave("T1", "e1", "1.0")).toBe(false);
		expect(hasUnsavedDrafts("T1")).toBe(false);
	});

	it("flushDraftSaves 立即发出所有待发保存并等它们结束", async () => {
		// tsconfig 的 lib 不含 Promise.withResolvers，只能用构造器形式
		let release: () => void = () => {};
		const gate = new Promise<void>((r) => {
			release = r;
		});
		putSpy.mockImplementation(async (_path, init) => {
			await gate;
			const { body } = init as never as {
				body: { kind: ThreadDraft["kind"]; title: string; text: string };
			};
			return ok(savedFrom({ entryId: "e1", block: "0.0" }, body));
		});
		scheduleDraftSave(qc, "T2", "e1", "0.0", payload("x"));
		let done = false;
		const flushed = flushDraftSaves("T2").then(() => {
			done = true;
		});
		await vi.advanceTimersByTimeAsync(0);
		expect(putSpy).toHaveBeenCalledTimes(1);
		expect(done).toBe(false);
		expect(hasPendingDraftSave("T2", "e1", "0.0")).toBe(true);
		release();
		await flushed;
		expect(done).toBe(true);
		await vi.advanceTimersByTimeAsync(2000);
		expect(putSpy).toHaveBeenCalledTimes(1);
	});

	it("保存失败给出带重试的提示，不让 flush 抛出", async () => {
		putSpy.mockImplementationOnce(async () => fail(500, "internal"));
		scheduleDraftSave(qc, "T3", "e1", "0.0", payload("x"));
		await expect(flushDraftSaves("T3")).resolves.toBeUndefined();
		const toast = toastsStore.getState().toasts.at(-1);
		expect(toast?.tone).toBe("err");
		expect(toast?.action).toBeDefined();
		toast?.action?.run();
		await flushDraftSaves("T3");
		expect(putSpy).toHaveBeenCalledTimes(2);
		expect(putSpy.mock.calls[1]?.[1]).toMatchObject({ body: payload("x") });
	});

	it("resetDraft：未送达过时取消待发保存并删除，缓存里去掉", async () => {
		const prev = savedFrom({ entryId: "e1", block: "0.0" }, payload("old"));
		qc.setQueryData(threadDraftsKey("T4"), { drafts: [prev] });
		delSpy.mockImplementation(
			async () => ({ response: new Response(null, { status: 204 }) }) as never,
		);
		scheduleDraftSave(qc, "T4", "e1", "0.0", payload("new"));
		await resetDraft(qc, "T4", "e1", "0.0", payload("orig"), false);
		await vi.advanceTimersByTimeAsync(2000);
		expect(putSpy).not.toHaveBeenCalled();
		expect(delSpy).toHaveBeenCalledTimes(1);
		expect(delSpy.mock.calls[0]?.[1]).toMatchObject({
			params: { path: { id: "T4", entryId: "e1", block: "0.0" } },
		});
		expect(qc.getQueryData(threadDraftsKey("T4"))).toEqual({ drafts: [] });
	});

	it("resetDraft：服务端本来没有这份改稿（404）视为已恢复", async () => {
		delSpy.mockImplementation(async () => fail(404, "draft_not_found"));
		const before = toastsStore.getState().toasts.length;
		await resetDraft(qc, "T5", "e1", "0.0", payload("orig"), false);
		expect(toastsStore.getState().toasts.length).toBe(before);
	});

	it("resetDraft：已送达过时把原文作为改稿保存", async () => {
		qc.setQueryData(threadDraftsKey("T6"), {
			drafts: [
				{
					...savedFrom({ entryId: "e1", block: "0.0" }, payload("old")),
					deliveredAt: 900,
				},
			],
		});
		await resetDraft(qc, "T6", "e1", "0.0", payload("orig"), true);
		expect(delSpy).not.toHaveBeenCalled();
		expect(putSpy).toHaveBeenCalledTimes(1);
		expect(putSpy.mock.calls[0]?.[1]).toMatchObject({ body: payload("orig") });
		expect(qc.getQueryData(threadDraftsKey("T6"))).toEqual({
			drafts: [savedFrom({ entryId: "e1", block: "0.0" }, payload("orig"))],
		});
	});
});
