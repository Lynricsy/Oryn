/** 上传队列随宿主卸载终止：进行中的上传收到 abort，挂起的 mkdir 之后不再入队 / 写入。 */
import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { FileSource, WriteOptions } from "../../lib/file-source";
import { batchFromFiles, useUploads } from "./UploadQueue";

function deferred<T>() {
	let resolve!: (v: T) => void;
	const promise = new Promise<T>((r) => {
		resolve = r;
	});
	return { promise, resolve };
}

function source(over: Partial<FileSource>): FileSource {
	return {
		id: "mem",
		caps: { mkdir: true, rename: true, upload: true, download: false },
		roots: async () => [],
		list: async () => [],
		stat: vi.fn(),
		readText: vi.fn(),
		readBlob: vi.fn(),
		write: vi.fn(),
		mkdir: vi.fn(async () => {}),
		rename: vi.fn(),
		remove: vi.fn(),
		downloadUrl: () => null,
		...over,
	} as FileSource;
}

describe("useUploads 卸载", () => {
	it("卸载时中止进行中的上传，排队中的文件不再开始", async () => {
		const signals: AbortSignal[] = [];
		const write = vi.fn(
			(_p: string, _b: Blob | string, opts?: WriteOptions) =>
				new Promise<never>((_res, rej) => {
					const signal = opts?.signal;
					if (signal) signals.push(signal);
					signal?.addEventListener("abort", () =>
						rej(new DOMException("aborted", "AbortError")),
					);
				}),
		);
		const src = source({ write });
		const { result, unmount } = renderHook(() => useUploads(() => {}));
		const files = Array.from(
			{ length: 5 },
			(_, i) => new File(["x"], `${i}.txt`),
		);
		await act(() => result.current.enqueue(batchFromFiles(files, "/r"), src));
		expect(write).toHaveBeenCalledTimes(3);
		expect(signals.every((s) => !s.aborted)).toBe(true);
		unmount();
		expect(signals).toHaveLength(3);
		expect(signals.every((s) => s.aborted)).toBe(true);
		await act(async () => {
			for (let i = 0; i < 20; i++) await Promise.resolve();
		});
		expect(write).toHaveBeenCalledTimes(3);
	});

	it("卸载时还在 mkdir 的批次：之后不再建目录、不入队、不写入", async () => {
		const gate = deferred<void>();
		const mkdir = vi.fn(async () => gate.promise);
		const write = vi.fn();
		const src = source({ mkdir, write });
		const { result, unmount } = renderHook(() => useUploads(() => {}));
		let done!: Promise<void>;
		act(() => {
			done = result.current.enqueue(
				{
					dirs: ["/r/a", "/r/a/b"],
					files: [{ file: new File(["x"], "f.txt"), path: "/r/a/b/f.txt" }],
					dropped: 0,
				},
				src,
			);
		});
		unmount();
		gate.resolve();
		await act(() => done);
		expect(mkdir).toHaveBeenCalledTimes(1);
		expect(write).not.toHaveBeenCalled();
	});
});
