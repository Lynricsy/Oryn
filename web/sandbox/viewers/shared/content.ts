// 查看器读写入口文件：内容按页面地址相对读取（落在页面所在的版本）；编辑经页面运行时的 `content` 能力
// （`window.oryn.use("content")`）保存成新版本。能力不可用（旧版本、在浏览器中单独打开、发布页、缩略图）时只读。

import { useCallback, useEffect, useRef, useState } from "react";
import type { Capability } from "../../runtime/transport";
import { type ViewerConfig, type ViewMode, versionBase } from "./config";

/** 显示中的内容。 */
export interface LoadedContent {
	text: string;
	version: number;
	head: number;
	/** 相对路径（images/…）按它解析。 */
	base: string;
}

export type SaveStatus =
	| { kind: "idle" }
	| { kind: "saving" }
	| { kind: "saved"; version: number }
	| { kind: "conflict" }
	| { kind: "invalid"; message: string }
	| { kind: "failed"; message: string };

export interface ViewerContent {
	state: "loading" | "error" | "ready";
	doc: LoadedContent | null;
	/** 能编辑：`content` 能力可用且显示的就是最新版本。 */
	editable: boolean;
	status: SaveStatus;
	/**
	 * 把入口文件换成 `text` 保存成新版本。`saved` 与 `conflict`（这时已改为显示最新版本）之后草稿作废；
	 * `error`（校验没过、网络等）时草稿保留，可以改了再存。
	 */
	save(text: string): Promise<SaveOutcome>;
	/** 收起保存状态条。 */
	dismiss(): void;
}

export type SaveOutcome = "saved" | "conflict" | "error";

interface OrynGlobal {
	use(cap: string): Promise<Capability | null>;
}

interface ContentCap {
	read(opts?: { version?: number }): Promise<unknown>;
	write(opts: { baseVersion: number; text: string }): Promise<unknown>;
}

/** 页面运行时的 `content` 能力；没有运行时或能力不可用时为 `null`。 */
export async function openContent(win: Window): Promise<ContentCap | null> {
	const oryn = (win as Window & { oryn?: OrynGlobal }).oryn;
	if (!oryn || typeof oryn.use !== "function") return null;
	const cap = await oryn.use("content");
	if (!cap || typeof cap.read !== "function" || typeof cap.write !== "function")
		return null;
	return cap as unknown as ContentCap;
}

function errorMessage(e: unknown): string {
	return e instanceof Error ? e.message : String(e);
}

export function useViewerContent(
	config: ViewerConfig,
	view: ViewMode,
): ViewerContent {
	const [state, setState] = useState<ViewerContent["state"]>("loading");
	const [doc, setDoc] = useState<LoadedContent | null>(null);
	const [cap, setCap] = useState<ContentCap | null>(null);
	const [status, setStatus] = useState<SaveStatus>({ kind: "idle" });
	const saving = useRef(false);

	useEffect(() => {
		let live = true;
		const page = window.location.href;
		fetch(new URL(config.entry, page), { credentials: "omit" })
			.then((res) => {
				if (!res.ok) throw new Error(String(res.status));
				return res.text();
			})
			.then(
				(text) => {
					if (!live) return;
					setDoc({
						text,
						version: config.version,
						head: config.head,
						base: page,
					});
					setState("ready");
				},
				() => {
					if (live) setState("error");
				},
			);
		if (view === null)
			openContent(window).then(
				(c) => {
					if (live) setCap(c);
				},
				() => {},
			);
		return () => {
			live = false;
		};
	}, [config, view]);

	const save = useCallback(
		async (text: string): Promise<SaveOutcome> => {
			if (!cap || !doc || saving.current) return "error";
			saving.current = true;
			setStatus({ kind: "saving" });
			try {
				const res = (await cap.write({ baseVersion: doc.version, text })) as {
					version?: unknown;
				} | null;
				const version =
					typeof res?.version === "number" ? res.version : doc.head + 1;
				setDoc({
					text,
					version,
					head: version,
					base: versionBase(config.root, version, window.location.href),
				});
				setStatus({ kind: "saved", version });
				return "saved";
			} catch (e) {
				// 能力调用失败时运行时抛出带 code 的错误
				const code = (e as { code?: unknown } | null)?.code;
				if (code === "conflict") {
					setStatus({ kind: "conflict" });
					try {
						const latest = (await cap.read()) as {
							text?: unknown;
							version?: unknown;
							head?: unknown;
						};
						if (
							typeof latest.text === "string" &&
							typeof latest.version === "number"
						) {
							const head =
								typeof latest.head === "number" ? latest.head : latest.version;
							setDoc({
								text: latest.text,
								version: latest.version,
								head,
								base: versionBase(
									config.root,
									latest.version,
									window.location.href,
								),
							});
						}
					} catch (err) {
						setStatus({ kind: "failed", message: errorMessage(err) });
					}
					return "conflict";
				}
				setStatus({
					kind: code === "invalid" ? "invalid" : "failed",
					message: errorMessage(e),
				});
				return "error";
			} finally {
				saving.current = false;
			}
		},
		[cap, doc, config.root],
	);

	const dismiss = useCallback(() => setStatus({ kind: "idle" }), []);

	return {
		state,
		doc,
		editable: cap !== null && doc !== null && doc.version === doc.head,
		status,
		save,
		dismiss,
	};
}
