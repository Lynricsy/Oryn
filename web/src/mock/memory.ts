/**
 * mock 后端的 M16（内置 Hindsight 记忆）部分：`/api/v1/memory/*` 全部操作，格式与 orynd 一致，时间戳是 Unix 秒。
 * `MockServer.handleFetch` 在 runner 路由之前调 `handle`，不归这里的请求返回 null。
 * 初始：已连接本机 Hindsight 1.2.1、已生成 .env、总开关开、bank 里有记忆（嵌入已锁定）、两条待批准候选、一条删除待重试。
 * `localStorage["oryn.mock.memory"]` 设为 MemoryStatus 的某个 state（如 `unreachable`、`suspect_empty`）时状态固定为它，
 * 设为 `version` 时报告未验证的 1.3.0、设为 `restart` 时 retain 用途已换协议待重启（截图与验收用）。
 * 改总开关或项目开关时经 `restart("memory")` 把运行中的线程记进 apply-status 的待重启列表。
 * 嵌入锁定时改模型或维度：不带 `reembed` 回 409 `embed_locked`；带 `reembed` 时只在状态为 `unreachable`（API 已停止）时接受，
 * 否则 409 `hindsight_running`。
 */
import type { Schemas } from "../api/client";
import { projects as fixtureProjects, memoryFixtures } from "./fixtures";

type Json = Record<string, unknown>;
type Purpose = Schemas["MemoryPurpose"];
type Family = Schemas["MemoryFamily"];
type State = Schemas["MemoryStatus"]["state"];

const MOCK_KEY = "oryn.mock.memory";
const PURPOSES: readonly Purpose[] = [
	"default",
	"retain",
	"reflect",
	"consolidation",
	"mental_model",
];
const STATES: readonly State[] = [
	"unconfigured",
	"no_keys",
	"disabled",
	"ok",
	"unreachable",
	"unauthorized",
	"not_ready",
	"suspect_empty",
];
/** 每个 document 平均派生出的记忆数（只用来让记忆数看起来像真的）。 */
const FACTS_PER_DOC = 7;
/** 导出从开始到完成的时长。 */
const EXPORT_MS = 2_000;

/** 与服务端 `insecure_url` 判定一致：本机、`::1` 与私有网段可以用 http。 */
function insecureUrl(raw: string): boolean {
	let u: URL;
	try {
		u = new URL(raw);
	} catch {
		return true;
	}
	if (u.protocol === "https:") return false;
	if (u.protocol !== "http:") return true;
	const h = u.hostname.replace(/^\[|\]$/g, "");
	return !(
		h === "localhost" ||
		h === "::1" ||
		/^127\./.test(h) ||
		/^10\./.test(h) ||
		/^192\.168\./.test(h) ||
		/^172\.(1[6-9]|2\d|3[01])\./.test(h)
	);
}

function json(body: unknown, status = 200): Response {
	return new Response(body === undefined ? null : JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

function err(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

function nowSec(): number {
	return Math.floor(Date.now() / 1000);
}

function hex(n: number): string {
	let s = "";
	for (let i = 0; i < n; i++) s += Math.floor(Math.random() * 16).toString(16);
	return s;
}

const ULID_CHARS = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
function ulid(): string {
	let s = "01JD";
	for (let i = 0; i < 22; i++)
		s += ULID_CHARS[Math.floor(Math.random() * ULID_CHARS.length)];
	return s;
}

export interface MemoryDeps {
	/** 记忆开关变了：运行中需重启的线程进 apply-status（原因 `reason`） */
	restart(reason: string): void;
	/** 线程标题（候选与 document 的来源线程） */
	threadTitle(id: string): string | undefined;
}

export class MockMemory {
	private readonly deps: MemoryDeps;
	private settings: Schemas["MemorySettings"] = {
		enabled: true,
		hindsightUrl: "http://127.0.0.1:8888",
		keysGenerated: true,
		keysGeneratedAt: nowSec() - 6 * 86_400,
		outputLanguage: "简体中文",
	};
	/** 项目 → 单独设置的开关（没有 = 跟随全局） */
	private readonly projectOverrides = new Map<string, boolean>([
		[fixtureProjects[1]?.id ?? "", false],
	]);
	/** 用途 → 选定的模型（没有 = 跟随 default） */
	private readonly picks = new Map<
		Purpose,
		{ providerKey: string; modelId: string }
	>([
		["default", { providerKey: "anthropic", modelId: "claude-haiku-4-5" }],
		["reflect", { providerKey: "anthropic", modelId: "claude-sonnet-5" }],
	]);
	/** 用途 → `.env` 里已部署的协议类型 */
	private deployed: Record<Purpose, Family> = {
		default: "anthropic",
		retain: "anthropic",
		reflect: "anthropic",
		consolidation: "anthropic",
		mental_model: "anthropic",
	};
	private endpoints: {
		embed: Schemas["MemoryEndpoint"];
		rerank: Schemas["MemoryEndpoint"];
	} = {
		embed: {
			kind: "embed",
			baseUrl: "https://api.siliconflow.cn/v1",
			model: "BAAI/bge-m3",
			dimensions: 1024,
			hasKey: true,
		},
		rerank: {
			kind: "rerank",
			baseUrl: "https://api.siliconflow.cn/v1",
			model: "BAAI/bge-reranker-v2-m3",
			hasKey: false,
		},
	};
	private readonly data = memoryFixtures();
	private readonly exports = new Map<
		string,
		{ id: string; createdAt: number; doneAt: number }
	>();

	constructor(deps: MemoryDeps) {
		this.deps = deps;
		if (this.flag() === "restart") {
			this.picks.set("retain", {
				providerKey: "openrouter",
				modelId: "deepseek/deepseek-v3.2",
			});
		}
	}

	private flag(): string | null {
		try {
			return globalThis.localStorage?.getItem(MOCK_KEY) ?? null;
		} catch {
			return null;
		}
	}

	private projectList(): Schemas["MemoryProjectToggle"][] {
		return fixtureProjects
			.filter((p) => !p.archivedAt)
			.map((p) => {
				const o = this.projectOverrides.get(p.id);
				return {
					projectId: p.id,
					name: p.name,
					enabled: o ?? this.settings.enabled,
					overridden: o !== undefined,
				};
			});
	}

	private memoryCount(): number {
		return this.data.documents.length * FACTS_PER_DOC;
	}

	private status(): Schemas["MemoryStatus"] {
		const count = this.memoryCount();
		const base = {
			testedMinor: "1.2",
			versionMismatch: false,
			pendingDeletes: this.data.pendingDeletes.length,
			candidates: this.data.candidates.length,
			embedLocked: count > 0,
			checkedAt: nowSec(),
		};
		const flag = this.flag();
		const forced = STATES.find((s) => s === flag);
		const url = this.settings.hindsightUrl ?? "";
		if (forced === "unconfigured" || !url)
			return { ...base, state: "unconfigured" };
		if (forced === "no_keys" || !this.settings.keysGenerated)
			return { ...base, state: "no_keys" };
		if (forced === "unreachable")
			return {
				...base,
				state: "unreachable",
				error: `读取版本：连不上 Hindsight：Get "${url}/version": dial tcp 127.0.0.1:8888: connect: connection refused`,
			};
		if (forced === "unauthorized")
			return {
				...base,
				state: "unauthorized",
				version: "1.2.1",
				error:
					"读取 bank 统计：API key 不被接受（401），请重新生成 .env 并重启 Hindsight",
			};
		if (forced === "not_ready")
			return {
				...base,
				state: "not_ready",
				version: "1.2.1",
				error: "检查就绪：/health/ready 返回 503",
			};
		const version = flag === "version" ? "1.3.0" : "1.2.1";
		const probed = {
			...base,
			version,
			versionMismatch: version !== "1.2.1",
			memoryCount: count,
		};
		if (forced === "suspect_empty")
			return { ...probed, state: "suspect_empty", memoryCount: 0 };
		if (forced === "disabled" || !this.settings.enabled)
			return { ...probed, state: "disabled" };
		return { ...probed, state: "ok" };
	}

	private option(providerKey: string, modelId: string) {
		return this.data.options.find(
			(o) => o.providerKey === providerKey && o.modelId === modelId,
		);
	}

	private models(): Schemas["MemoryModels"] {
		const def = this.picks.get("default");
		const items = PURPOSES.map((purpose): Schemas["MemoryModelChoice"] => {
			const own = this.picks.get(purpose);
			const pick = own ?? def;
			const family = pick
				? this.option(pick.providerKey, pick.modelId)?.family
				: undefined;
			const deployedFamily = this.deployed[purpose];
			return {
				purpose,
				followsDefault: purpose !== "default" && own === undefined,
				...(pick
					? { providerKey: pick.providerKey, modelId: pick.modelId }
					: {}),
				...(family ? { family } : {}),
				deployedFamily,
				needsRestart: family !== undefined && family !== deployedFamily,
			};
		});
		const stale = items.filter((i) => i.needsRestart);
		return {
			items,
			options: this.data.options,
			...(stale.length > 0
				? {
						envSnippet: stale
							.map((i) => {
								const p = envPrefix(i.purpose);
								return `${p}_PROVIDER=${i.family}\n${p}_MODEL=oryn-${i.purpose.replace("_", "-")}\n${p}_BASE_URL=${relayUrl(i.purpose, i.family)}`;
							})
							.join("\n"),
					}
				: {}),
		};
	}

	private envFile(): string {
		const key = `hs_${hex(40)}`;
		const token = `ort_${hex(40)}`;
		const lines = [
			"# Oryn 生成的 Hindsight .env（M16）；两个密钥只在生成时显示一次",
			"HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension",
			`HINDSIGHT_API_TENANT_API_KEY=${key}`,
			"HINDSIGHT_API_WORKER_ID=oryn-hindsight-1",
			"HINDSIGHT_API_LLM_OUTPUT_LANGUAGE=zh-CN",
			"HINDSIGHT_API_LLM_MAX_CONCURRENT=4",
			"HINDSIGHT_API_WORKER_MAX_SLOTS=4",
			"HINDSIGHT_API_TEXT_SEARCH_EXTENSION=pgroonga",
		];
		for (const c of this.models().items) {
			const p = envPrefix(c.purpose);
			const family = c.family ?? "openai";
			lines.push(
				`${p}_PROVIDER=${family}`,
				`${p}_MODEL=oryn-${c.purpose.replace("_", "-")}`,
				`${p}_BASE_URL=${relayUrl(c.purpose, family)}`,
				`${p}_API_KEY=${token}`,
			);
		}
		lines.push(
			"HINDSIGHT_API_EMBEDDINGS_PROVIDER=openai",
			"HINDSIGHT_API_EMBEDDINGS_OPENAI_BASE_URL=http://host.docker.internal:8080/memory-relay/v1/embed",
			`HINDSIGHT_API_EMBEDDINGS_OPENAI_API_KEY=${token}`,
			"HINDSIGHT_API_EMBEDDINGS_OPENAI_MODEL=oryn-embed",
			`HINDSIGHT_API_EMBEDDINGS_OPENAI_DIMENSIONS=${this.endpoints.embed.dimensions ?? 1024}`,
			"HINDSIGHT_API_RERANKER_PROVIDER=cohere",
			"HINDSIGHT_API_RERANKER_COHERE_BASE_URL=http://host.docker.internal:8080/memory-relay/v1/rerank",
			`HINDSIGHT_API_RERANKER_COHERE_API_KEY=${token}`,
			"HINDSIGHT_API_RERANKER_COHERE_MODEL=oryn-rerank",
		);
		return `${lines.join("\n")}\n`;
	}

	private withTitle(d: Schemas["MemoryDocument"]): Schemas["MemoryDocument"] {
		const title = d.threadId ? this.deps.threadTitle(d.threadId) : undefined;
		return title ? { ...d, threadTitle: title } : d;
	}

	private touchUserPreferences(): void {
		const m = this.data.mentalModels.find((x) => x.id === "user-preferences");
		if (m) m.lastRefreshedAt = new Date().toISOString();
	}

	private createGlobal(content: string): Schemas["MemoryDocument"] {
		const at = nowSec();
		const d: Schemas["MemoryDocument"] = {
			id: `oryn-${ulid()}`,
			preview: content.slice(0, 400),
			content,
			createdAt: at,
			updatedAt: at,
		};
		this.data.documents.unshift(d);
		this.touchUserPreferences();
		return d;
	}

	async handle(
		p: string,
		m: string,
		body: () => Promise<Json>,
		url: URL,
	): Promise<Response | null> {
		if (!p.startsWith("memory/")) return null;
		const sub = p.slice("memory/".length);

		if (sub === "settings" && m === "GET") return json(this.settings);
		if (sub === "settings" && m === "PUT") {
			const b = await body();
			if (typeof b.enabled !== "boolean")
				return err(400, "invalid_request", "enabled is required");
			const raw =
				typeof b.hindsightUrl === "string" ? b.hindsightUrl.trim() : "";
			if (raw && insecureUrl(raw))
				return err(
					400,
					"insecure_url",
					"remote hindsightUrl must use https://",
				);
			const changed = b.enabled !== this.settings.enabled;
			const { hindsightUrl: _old, ...rest } = this.settings;
			this.settings = {
				...rest,
				enabled: b.enabled,
				...(raw ? { hindsightUrl: raw.replace(/\/+$/, "") } : {}),
			};
			if (changed) this.deps.restart("memory");
			return json(this.settings);
		}
		if (sub === "status" && m === "GET") return json(this.status());
		if (sub === "env-file" && m === "POST") {
			const generatedAt = nowSec();
			this.settings = {
				...this.settings,
				keysGenerated: true,
				keysGeneratedAt: generatedAt,
			};
			return json({ content: this.envFile(), generatedAt });
		}

		if (sub === "projects" && m === "GET")
			return json({ items: this.projectList() });
		const proj = /^projects\/([^/]+)$/.exec(sub);
		if (proj && m === "PUT") {
			const id = decodeURIComponent(proj[1] ?? "");
			const b = await body();
			if (typeof b.enabled !== "boolean")
				return err(400, "invalid_request", "enabled is required");
			const row = this.projectList().find((x) => x.projectId === id);
			if (!row) return err(404, "not_found", "project not found");
			this.projectOverrides.set(id, b.enabled);
			if (row.enabled !== b.enabled) this.deps.restart("memory");
			return json(this.projectList().find((x) => x.projectId === id));
		}

		if (sub === "models" && m === "GET") return json(this.models());
		if (sub === "models" && m === "PUT") {
			const b = (await body()) as Partial<Schemas["MemoryModelsUpdate"]>;
			const items = Array.isArray(b.items) ? b.items : [];
			const next = new Map<Purpose, { providerKey: string; modelId: string }>();
			for (const i of items) {
				if (!PURPOSES.includes(i.purpose))
					return err(400, "invalid_request", `unknown purpose ${i.purpose}`);
				if (!i.providerKey) continue;
				const o = this.option(i.providerKey, i.modelId ?? "");
				if (!o?.family)
					return err(
						400,
						"unsupported_model",
						`${i.providerKey}/${i.modelId}: not usable by Hindsight`,
					);
				next.set(i.purpose, {
					providerKey: i.providerKey,
					modelId: i.modelId ?? "",
				});
			}
			if (!next.has("default"))
				return err(400, "invalid_request", "default model is required");
			this.picks.clear();
			for (const [k, v] of next) this.picks.set(k, v);
			return json(this.models());
		}
		if (sub === "models/deployed" && m === "POST") {
			for (const c of this.models().items)
				if (c.family) this.deployed[c.purpose] = c.family;
			return json(this.models());
		}

		if (sub === "endpoints" && m === "GET")
			return json({
				...this.endpoints,
				embedLocked: this.memoryCount() > 0,
			});
		const ep = /^endpoints\/(embed|rerank)$/.exec(sub);
		if (ep && m === "PUT") {
			const kind = ep[1] as Schemas["MemoryEndpointKind"];
			const b = (await body()) as Partial<Schemas["MemoryEndpointUpdate"]>;
			if (!b.baseUrl || !b.model)
				return err(400, "invalid_request", "baseUrl and model are required");
			const cur = this.endpoints[kind];
			const locked = kind === "embed" && this.memoryCount() > 0;
			const changesModel =
				b.model !== cur.model ||
				(b.dimensions !== undefined && b.dimensions !== cur.dimensions);
			if (locked && changesModel && !b.reembed)
				return err(
					409,
					"embed_locked",
					"bank has memories; changing the embedding model requires reembed",
				);
			// reembed 只在 Hindsight 不可达（已停止 API）时接受，与服务端一致
			if (locked && b.reembed && this.status().state !== "unreachable")
				return err(
					409,
					"hindsight_running",
					"stop the Hindsight API before switching the embedding model",
				);
			this.endpoints[kind] = {
				kind,
				baseUrl: b.baseUrl,
				model: b.model,
				...(kind === "embed"
					? { dimensions: b.dimensions ?? cur.dimensions }
					: {}),
				hasKey: b.clearKey ? false : b.apiKey ? true : cur.hasKey,
			};
			return json({ ...this.endpoints, embedLocked: locked });
		}

		if (sub === "usage" && m === "GET") return json({ items: this.data.usage });

		if (sub === "documents" && m === "GET") {
			const global = url.searchParams.get("global") === "true";
			const projectId = url.searchParams.get("projectId");
			if (!global && !projectId)
				return err(400, "invalid_request", "projectId or global is required");
			const offset = Number(url.searchParams.get("offset") ?? 0);
			const limit = Number(url.searchParams.get("limit") ?? 30);
			const all = this.data.documents
				.filter((d) => (global ? !d.projectId : d.projectId === projectId))
				.sort((a, b) => b.updatedAt - a.updatedAt);
			return json({
				items: all
					.slice(offset, offset + limit)
					.map((d) =>
						this.withTitle(global ? d : { ...d, content: undefined }),
					),
				hasMore: offset + limit < all.length,
			});
		}
		const docMatch = /^documents\/([^/]+)$/.exec(sub);
		if (docMatch && m === "DELETE") {
			const id = decodeURIComponent(docMatch[1] ?? "");
			const i = this.data.documents.findIndex((d) => d.id === id);
			if (i < 0) return err(404, "not_found", "document not found");
			const [removed] = this.data.documents.splice(i, 1);
			if (removed && !removed.projectId) this.touchUserPreferences();
			return new Response(null, { status: 204 });
		}
		if (sub === "search" && m === "GET") {
			const q = (url.searchParams.get("q") ?? "").trim();
			if (!q) return err(400, "invalid_request", "q is required");
			const projectId = url.searchParams.get("projectId");
			const words = q.toLowerCase().split(/\s+/).filter(Boolean);
			const hits = this.data.documents
				.filter((d) => !d.projectId || d.projectId === projectId)
				.filter((d) => {
					const text = (d.content ?? d.preview).toLowerCase();
					return words.some((w) => text.includes(w));
				})
				.map((d): Schemas["MemorySearchHit"] => ({
					text: (d.content ?? d.preview).split("\n").at(-1) ?? d.preview,
					type: d.projectId ? "experience" : "world",
					mentionedAt: new Date(d.updatedAt * 1000).toISOString(),
					documentId: d.id,
					global: !d.projectId,
				}));
			return json({ items: hits });
		}
		const forget = /^threads\/([^/]+)\/forget$/.exec(sub);
		if (forget && m === "POST") {
			const id = decodeURIComponent(forget[1] ?? "");
			const before = this.data.documents.length;
			this.data.documents = this.data.documents.filter(
				(d) => d.threadId !== id,
			);
			return json({ deleted: before - this.data.documents.length, pending: 0 });
		}
		if (sub === "pending-deletes" && m === "GET")
			return json({ items: this.data.pendingDeletes });

		if (sub === "candidates" && m === "GET")
			return json({
				items: [...this.data.candidates]
					.sort((a, b) => b.createdAt - a.createdAt)
					.map((c) => {
						const title = c.sourceThreadId
							? this.deps.threadTitle(c.sourceThreadId)
							: undefined;
						return title ? { ...c, sourceThreadTitle: title } : c;
					}),
			});
		const cand = /^candidates\/(mcd_[0-9A-Z]{26})(\/approve)?$/.exec(sub);
		if (cand) {
			const id = cand[1] ?? "";
			const i = this.data.candidates.findIndex((c) => c.id === id);
			if (i < 0) return err(404, "not_found", "candidate not found");
			if (!cand[2] && m === "DELETE") {
				this.data.candidates.splice(i, 1);
				return new Response(null, { status: 204 });
			}
			if (cand[2] && m === "POST") {
				const b = await body();
				const content =
					typeof b.content === "string" && b.content.trim()
						? b.content.trim()
						: (this.data.candidates[i]?.content ?? "");
				this.data.candidates.splice(i, 1);
				return json(this.createGlobal(content));
			}
		}
		if (sub === "global" && m === "POST") {
			const b = await body();
			const content = typeof b.content === "string" ? b.content.trim() : "";
			if (!content || content.length > 4000)
				return err(400, "invalid_request", "content must be 1-4000 chars");
			return json(this.createGlobal(content), 201);
		}
		const glob = /^global\/([^/]+)$/.exec(sub);
		if (glob && m === "PUT") {
			const id = decodeURIComponent(glob[1] ?? "");
			const d = this.data.documents.find((x) => x.id === id && !x.projectId);
			if (!d) return err(404, "not_found", "global memory not found");
			const b = await body();
			const content = typeof b.content === "string" ? b.content.trim() : "";
			if (!content || content.length > 4000)
				return err(400, "invalid_request", "content must be 1-4000 chars");
			d.content = content;
			d.preview = content.slice(0, 400);
			d.updatedAt = nowSec();
			this.touchUserPreferences();
			return json(d);
		}

		if (sub === "mental-models" && m === "GET") {
			const projectId = url.searchParams.get("projectId");
			return json({
				items: this.data.mentalModels.filter((x) =>
					projectId ? x.projectId === projectId : x.global,
				),
			});
		}
		const refresh = /^mental-models\/([^/]+)\/refresh$/.exec(sub);
		if (refresh && m === "POST") {
			const id = decodeURIComponent(refresh[1] ?? "");
			const mm = this.data.mentalModels.find((x) => x.id === id);
			if (!mm) return err(404, "not_found", "mental model not found");
			mm.lastRefreshedAt = new Date().toISOString();
			return new Response(null, { status: 202 });
		}

		if (sub === "exports" && m === "POST") {
			const id = `exp-${hex(12)}`;
			const createdAt = nowSec();
			this.exports.set(id, {
				id,
				createdAt,
				doneAt: Date.now() + EXPORT_MS,
			});
			return json({ id, state: "running", createdAt }, 202);
		}
		const exp = /^exports\/([^/]+)(\/download)?$/.exec(sub);
		if (exp && m === "GET") {
			const e = this.exports.get(decodeURIComponent(exp[1] ?? ""));
			if (!e) return err(404, "not_found", "export not found");
			const done = Date.now() >= e.doneAt;
			if (exp[2]) {
				if (!done)
					return err(409, "export_not_ready", "export is still running");
				return new Response(
					JSON.stringify({ bank: "oryn", documents: this.data.documents }),
					{
						headers: {
							"Content-Type": "application/octet-stream",
							"Content-Disposition": `attachment; filename="oryn-memory-${e.id}.json"`,
						},
					},
				);
			}
			return json({
				id: e.id,
				state: done ? "done" : "running",
				createdAt: e.createdAt,
			});
		}
		return err(404, "not_found", `no mock for ${m} /api/v1/${p}`);
	}
}

/** 用途在 Hindsight .env 里的变量前缀。 */
function envPrefix(purpose: Purpose): string {
	return purpose === "default"
		? "HINDSIGHT_API_LLM"
		: `HINDSIGHT_API_${purpose.toUpperCase()}_LLM`;
}

/** 中转入口：协议决定 Hindsight 拼的路径，所以地址按协议分开。 */
function relayUrl(purpose: Purpose, family: Family | undefined): string {
	return `http://host.docker.internal:8080/memory-relay/v1/${purpose}/${family ?? "openai"}`;
}
