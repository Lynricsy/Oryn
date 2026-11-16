/**
 * mock 后端的 artifact 发布（M14 8.1、8.4）：`/api/v1/artifacts/{id}/publication*`、`/api/v1/artifact-publications*`、
 * `/api/v1/settings/artifacts`，响应形状与 orynd（`internal/api/artifact_publish.go`）一致。
 * - 发布为访问码 / 公开（或换到新版本）前扫描该版本的文本文件：有命中且没带 `acknowledgeFindings` 回 409 `secrets_found`。
 * - 跟随最新版本：新版本到来时推进；访问码 / 公开且新版本有命中时不推进，记 `pendingVersion`。
 * - 自动发布：开着时新建（导入）的 artifact 立即以私有、跟随发布。
 * - 发布地址是 `<origin>/p/<slug>/`（mock 没有发布页，打开只是一个不存在的页面）；访客数是夹具里的固定值。
 * 由 `MockArtifacts.handle` 先调 `handle`，不归这里的请求返回 null。
 */
import type { Schemas } from "../api/client";
import { ARTIFACT_IDS, NOW } from "./fixtures";
import { scanFiles } from "./share-scan";

type Artifact = Schemas["Artifact"];
type Publication = Schemas["ArtifactPublication"];

/** 发布模块要用到的 artifact 存储。 */
export interface PublishHost {
	meta(id: string): Artifact | undefined;
	/** 改 `Artifact.published`（`null` = 取消发布）。 */
	setPublished(id: string, published: Artifact["published"] | null): void;
	/** 某个版本里要扫描的文本文件（路径 → 内容）。 */
	texts(id: string, version: number): [string, string][];
	/** 该版本的字节数（`usageBytes` 用）。 */
	size(id: string, version: number): number;
	changed(): void;
}

interface Stored extends Publication {
	slug: string;
}

const CODE_ALPHABET = "23456789abcdefghjkmnpqrstuvwxyz";
const BASE32 = "abcdefghijklmnopqrstuvwxyz234567";

function random(alphabet: string, n: number): string {
	const b = new Uint8Array(n);
	crypto.getRandomValues(b);
	return Array.from(b, (x) => alphabet[x % alphabet.length]).join("");
}

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

function origin(): string {
	return globalThis.location?.origin ?? "http://localhost:5173";
}

export class MockArtifactPublish {
	private readonly pubs = new Map<string, Stored>();
	private autoPublish = false;
	private readonly host: PublishHost;

	constructor(host: PublishHost) {
		this.host = host;
	}

	/** 夹具：文档以访问码发布，有两位访客（总览页徽标与查看器顶栏的访客数）。 */
	seed() {
		const id = ARTIFACT_IDS.document;
		const a = this.host.meta(id);
		if (!a) return;
		const slug = random(BASE32, 26);
		this.pubs.set(id, {
			id: `apb_${slug.toUpperCase()}`,
			slug,
			url: `${origin()}/p/${slug}/`,
			access: "code",
			code: "k7mq",
			codeLocked: false,
			version: a.version,
			follow: true,
			visitors: 2,
			createdAt: NOW - 3_600_000,
			updatedAt: NOW - 3_600_000,
		});
		this.host.setPublished(id, { access: "code" });
	}

	private view(p: Stored): Publication {
		const { slug: _, code, ...rest } = p;
		return { ...rest, ...(p.access === "code" && code ? { code } : {}) };
	}

	private state(id: string): Response {
		const p = this.pubs.get(id);
		let usageBytes = 0;
		for (const [aid, x] of this.pubs)
			usageBytes += this.host.size(aid, x.version);
		return json({
			publication: p ? this.view(p) : null,
			usageBytes,
		} satisfies Schemas["ArtifactPublicationState"]);
	}

	private flagged(id: string, version: number): Schemas["ShareScan"] | null {
		const scan = scanFiles(this.host.texts(id, version));
		return scan.findings.length > 0 ? scan : null;
	}

	private async publish(id: string, req: Request): Promise<Response> {
		const a = this.host.meta(id);
		if (!a) return error(404, "not_found", "artifact 不存在");
		const body = (await req.json()) as Schemas["ArtifactPublishRequest"];
		if (!["private", "code", "public"].includes(body.access))
			return error(400, "invalid", "access 必须是 private、code 或 public。");
		const cur = this.pubs.get(id);
		if (!cur && a.deletedAt !== undefined)
			return error(410, "deleted", "这个 artifact 已被删除");
		let version = cur?.version ?? a.version;
		if (body.version !== undefined) {
			if (body.version < 1 || body.version > a.version)
				return error(400, "invalid", `没有版本 ${body.version}`);
			version = body.version;
		}
		const follow = body.follow ?? cur?.follow ?? true;
		const exposed =
			body.access !== "private" &&
			(!cur || cur.version !== version || cur.access === "private");
		if (exposed && body.acknowledgeFindings !== true) {
			const scan = this.flagged(id, version);
			if (scan)
				return json(
					{
						code: "secrets_found",
						message: "这个版本里有疑似密钥",
						scan,
					} satisfies Schemas["ShareScanConflict"],
					409,
				);
		}
		const now = Date.now();
		const slug = cur?.slug ?? random(BASE32, 26);
		const next: Stored = {
			id: cur?.id ?? `apb_${slug.toUpperCase()}`,
			slug,
			url: `${origin()}/p/${slug}/`,
			access: body.access,
			codeLocked: cur?.codeLocked ?? false,
			version,
			follow,
			visitors: cur?.visitors ?? 0,
			createdAt: cur?.createdAt ?? now,
			updatedAt: now,
		};
		const code =
			cur?.code ??
			(body.access === "code" ? random(CODE_ALPHABET, 4) : undefined);
		if (code) next.code = code;
		const pending = cur?.pendingVersion;
		if (pending !== undefined && version < pending)
			next.pendingVersion = pending;
		this.pubs.set(id, next);
		if (!cur || cur.access !== body.access) {
			this.host.setPublished(id, { access: body.access });
			this.host.changed();
		}
		return this.state(id);
	}

	private rotate(id: string): Response {
		const cur = this.pubs.get(id);
		if (!cur) return error(404, "not_found", "这个 artifact 没有发布");
		this.pubs.set(id, {
			...cur,
			code: random(CODE_ALPHABET, 4),
			codeLocked: false,
			updatedAt: Date.now(),
		});
		return this.state(id);
	}

	/** 取消发布（删除时选了「同时取消发布」也走这里）。 */
	drop(id: string) {
		if (!this.pubs.delete(id)) return;
		this.host.setPublished(id, null);
		this.host.changed();
	}

	/** 发布页当前的访客数（未发布为 0）；在场消息里的 `visitors` 取它，与 orynd 一致。 */
	visitors(id: string): number {
		return this.pubs.get(id)?.visitors ?? 0;
	}

	/** 新版本：跟随的发布推进到它（访问码 / 公开且有命中时记 `pendingVersion`）。 */
	onVersion(id: string, version: number) {
		const cur = this.pubs.get(id);
		if (!cur?.follow) return;
		if (cur.access !== "private" && this.flagged(id, version)) {
			this.pubs.set(id, { ...cur, pendingVersion: version });
			return;
		}
		const { pendingVersion: _, ...rest } = cur;
		this.pubs.set(id, { ...rest, version, updatedAt: Date.now() });
	}

	/** 新建的 artifact：自动发布开着时以私有、跟随发布。 */
	onCreated(id: string) {
		const a = this.host.meta(id);
		if (!this.autoPublish || !a) return;
		const now = Date.now();
		const slug = random(BASE32, 26);
		this.pubs.set(id, {
			id: `apb_${slug.toUpperCase()}`,
			slug,
			url: `${origin()}/p/${slug}/`,
			access: "private",
			codeLocked: false,
			version: a.version,
			follow: true,
			visitors: 0,
			createdAt: now,
			updatedAt: now,
		});
		this.host.setPublished(id, { access: "private" });
	}

	async handle(req: Request, p: string, m: string): Promise<Response | null> {
		if (p === "settings/artifacts") {
			if (m === "PUT") {
				const body = (await req.json()) as Schemas["ArtifactPrefs"];
				this.autoPublish = body.autoPublish === true;
			} else if (m !== "GET") return null;
			return json({
				autoPublish: this.autoPublish,
			} satisfies Schemas["ArtifactPrefs"]);
		}
		if (p === "artifact-publications" && m === "GET") {
			const publications = [...this.pubs].flatMap(([id, x]) => {
				const artifact = this.host.meta(id);
				return artifact ? [{ artifact, publication: this.view(x) }] : [];
			});
			publications.sort(
				(x, y) => y.publication.updatedAt - x.publication.updatedAt,
			);
			return json({
				publications,
			} satisfies Schemas["ArtifactPublicationList"]);
		}
		const grant = /^artifact-publications\/([a-z2-7]{26})\/grant$/.exec(p);
		if (grant && m === "POST") {
			const hit = [...this.pubs.values()].find((x) => x.slug === grant[1]);
			if (!hit) return error(404, "not_found", "这个页面已不在线上");
			return json({
				url: `${hit.url}~${random(BASE32, 32)}/`,
			} satisfies Schemas["ArtifactOpenLink"]);
		}
		const pub = /^artifacts\/(art_[0-9A-Z]{26})\/publication(\/code)?$/.exec(p);
		if (!pub) return null;
		const id = pub[1] ?? "";
		if (!this.host.meta(id)) return error(404, "not_found", "artifact 不存在");
		if (pub[2]) return m === "POST" ? this.rotate(id) : null;
		if (m === "GET") return this.state(id);
		if (m === "PUT") return this.publish(id, req);
		if (m === "DELETE") {
			this.drop(id);
			return new Response(null, { status: 204 });
		}
		return null;
	}
}
