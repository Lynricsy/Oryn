/**
 * mock 后端的 M30（Agent 沙箱）部分：`GET /sandboxes?thread=` 与 `DELETE /sandboxes/{id}`。
 * 预置是 `sandboxFixtures()`（工具样例线程的两台：一台联网运行中，一台已因空闲回收）。
 * 销毁先进入 `destroying`，片刻后成为 `destroyed`（原因 `owner`），两步都推送 WS `sandboxes.changed`。
 * `MockServer.handleFetch` 调 `handle`，不归这里的请求返回 null。
 */
import type { Schemas } from "../api/client";
import { sandboxFixtures } from "./fixtures";

type Sandbox = Schemas["Sandbox"];

export interface SandboxDeps {
	/** 某线程名下的沙箱变了：推送 WS `sandboxes.changed` */
	changed(threadId: string): void;
}

/** 销毁中到已销毁的模拟耗时。 */
const DESTROY_MS = 800;
/** 线程视图里保留已结束沙箱的时长（同 orynd）。 */
const RECENT_MS = 60 * 60_000;

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

const LIVE: Record<Sandbox["state"], boolean> = {
	creating: true,
	ready: true,
	destroying: true,
	destroyed: false,
	failed: false,
};

export class MockSandboxes {
	private readonly items = new Map<string, Sandbox>(
		sandboxFixtures().map((s) => [s.id, s]),
	);

	private readonly deps: SandboxDeps;

	constructor(deps: SandboxDeps) {
		this.deps = deps;
	}

	handle(p: string, m: string, url: URL): Response | null {
		if (p === "sandboxes" && m === "GET") {
			const thread = url.searchParams.get("thread");
			const since = Date.now() - RECENT_MS;
			const list = [...this.items.values()]
				.filter((s) =>
					thread
						? s.ownerThreadId === thread &&
							(LIVE[s.state] || (s.destroyedAt ?? s.createdAt) >= since)
						: LIVE[s.state],
				)
				.toSorted((a, b) => a.createdAt - b.createdAt);
			return json({ sandboxes: list });
		}
		const dm = /^sandboxes\/([^/]+)$/.exec(p);
		if (dm && m === "DELETE") {
			const sb = this.items.get(decodeURIComponent(dm[1] as string));
			if (!sb)
				return json(
					{ code: "sandbox_not_found", message: "sandbox not found" },
					404,
				);
			if (sb.state === "creating" || sb.state === "ready") {
				sb.state = "destroying";
				sb.destroyReason = "owner";
				this.deps.changed(sb.ownerThreadId);
				setTimeout(() => {
					sb.state = "destroyed";
					sb.destroyedAt = Date.now();
					this.deps.changed(sb.ownerThreadId);
				}, DESTROY_MS);
			}
			return new Response(null, { status: 204 });
		}
		return null;
	}
}
