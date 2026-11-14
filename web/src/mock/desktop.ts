/**
 * mock 的浏览器 Profile 与 orb 桌面接口（M8，与控制面同形）：Profile 增删改查、orb 选用 / 切换 / 解除 Profile
 * （独占被占用 → 409 `ProfileInUseConflict`；只读 → 只读副本；接管 → 原持有者降级为只读并推送它的 `orb`）、
 * 立即保存（修订 +1）、浏览器启停；新建线程时按 `browserProfileId` / `profileMode` 给新 orb 定 Profile。
 * 改动经 `setOrb` 写回内存并广播 `orb`。
 */
import type { Orb, Project, Schemas } from "../api/client";
import { browserProfiles } from "./fixtures";

type Json = Record<string, unknown>;
type Profile = Schemas["BrowserProfile"];
type Holder = Schemas["BrowserProfileHolder"];
type Mode = Schemas["BrowserProfileMode"];

export interface DesktopDeps {
	orbs: Map<string, Orb>;
	setOrb(orb: Orb): void;
	projects(): Project[];
	threadTitle(id: string): string | undefined;
}

function json(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

const err = (code: string, message: string, status: number) =>
	json({ code, message }, status);

export class MockDesktop {
	readonly profiles: Profile[] = structuredClone(browserProfiles);
	private seq = 0;
	private readonly deps: DesktopDeps;

	constructor(deps: DesktopDeps) {
		this.deps = deps;
	}

	private holderOf(orb: Orb, now = Date.now()): Holder {
		const threadId = orb.threadIds[0];
		return {
			orbId: orb.id,
			threadIds: orb.threadIds,
			...(threadId
				? { threadId, threadTitle: this.deps.threadTitle(threadId) }
				: {}),
			since: now,
		};
	}

	private conflict(p: Profile): Response {
		return json(
			{
				code: "profile_in_use",
				message: `profile ${p.name} is held by ${p.holder?.orbId}`,
				profileId: p.id,
				profileName: p.name,
				holder: p.holder,
			},
			409,
		);
	}

	private sorted(): Profile[] {
		return [...this.profiles].sort((a, b) => a.name.localeCompare(b.name));
	}

	/** 释放本 orb 的租约与只读登记。 */
	private release(orbId: string) {
		for (const p of this.profiles) {
			if (p.holder?.orbId === orbId) delete p.holder;
			if (p.readers) {
				p.readers = p.readers.filter((r) => r.orbId !== orbId);
				if (p.readers.length === 0) delete p.readers;
			}
		}
	}

	/**
	 * 给 orb 定 Profile，返回它的桌面 Profile 视图。`takeover`：原持有者先「保存」（修订 +1），
	 * 再降级为只读（`taken_over`）并推送它的 `orb`。
	 */
	private bind(
		orb: Orb,
		p: Profile,
		mode: Mode,
		now = Date.now(),
	): Schemas["OrbDesktopProfile"] {
		this.release(orb.id);
		if (mode === "readonly") {
			p.readers = [...(p.readers ?? []), this.holderOf(orb, now)];
			return {
				id: p.id,
				name: p.name,
				status: "readonly",
				mode: "readonly",
				revision: p.revision,
				readonlyReason: "requested",
			};
		}
		const prev = p.holder;
		if (mode === "takeover" && prev && prev.orbId !== orb.id) {
			p.revision += 1;
			p.savedAt = now;
			const held = this.deps.orbs.get(prev.orbId);
			const me = this.holderOf(orb, now);
			p.readers = [...(p.readers ?? []), { ...prev, since: now }];
			if (held?.desktop?.profile)
				this.deps.setOrb({
					...held,
					desktop: {
						...held.desktop,
						profile: {
							...held.desktop.profile,
							status: "readonly",
							mode: "readonly",
							readonlyReason: "taken_over",
							takenOverBy: {
								threadId: me.threadId,
								threadTitle: me.threadTitle,
								at: now,
							},
						},
					},
				});
		}
		p.holder = this.holderOf(orb, now);
		p.updatedAt = now;
		return {
			id: p.id,
			name: p.name,
			status: "attached",
			mode: "exclusive",
			revision: p.revision,
		};
	}

	/**
	 * 新建线程的 Profile：省略 = 项目默认；空串 = 不用。被占用（独占方式）→ 409；不存在 → 404。
	 * 返回给新 orb 用的 `desktop`（没有 Profile 时也有桌面），或错误响应。
	 */
	forNewOrb(b: Json): Response | ((orb: Orb) => Schemas["OrbDesktop"]) {
		const explicit =
			typeof b.browserProfileId === "string" ? b.browserProfileId : undefined;
		const projectId = typeof b.projectId === "string" ? b.projectId : undefined;
		const id =
			explicit ??
			this.deps.projects().find((x) => x.id === projectId)
				?.defaultBrowserProfileId ??
			"";
		const mode = (b.profileMode as Mode | undefined) ?? "exclusive";
		const base: Schemas["OrbDesktop"] = {
			displayReady: true,
			browser: "stopped",
		};
		if (!id) return () => base;
		const p = this.profiles.find((x) => x.id === id);
		if (!p) return err("profile_not_found", id, 404);
		if (mode === "exclusive" && p.holder) return this.conflict(p);
		return (orb) => ({ ...base, profile: this.bind(orb, p, mode) });
	}

	async handle(req: Request, p: string, m: string): Promise<Response | null> {
		const body = async (): Promise<Json> => {
			try {
				return (await req.json()) as Json;
			} catch {
				return {};
			}
		};
		if (p === "browser-profiles") {
			if (m === "GET") return json({ profiles: this.sorted() });
			if (m !== "POST") return null;
			const name = String((await body()).name ?? "").trim();
			if (!name || [...name].length > 40)
				return err("invalid_request", "name", 400);
			if (this.profiles.some((x) => x.name === name))
				return err("profile_name_taken", name, 409);
			if (this.profiles.length >= 50) return err("profile_limit", "50", 409);
			const now = Date.now();
			const created: Profile = {
				id: `bpf_01MOCK${String(this.seq++).padStart(19, "0")}`,
				name,
				revision: 0,
				sizeBytes: 0,
				createdAt: now,
				updatedAt: now,
			};
			this.profiles.push(created);
			return json(created, 201);
		}
		const one = /^browser-profiles\/([^/]+)$/.exec(p);
		if (one) {
			const i = this.profiles.findIndex((x) => x.id === one[1]);
			const cur = this.profiles[i];
			if (!cur) return err("profile_not_found", one[1] ?? "", 404);
			if (m === "PATCH") {
				const name = String((await body()).name ?? "").trim();
				if (!name || [...name].length > 40)
					return err("invalid_request", "name", 400);
				if (this.profiles.some((x) => x.name === name && x.id !== cur.id))
					return err("profile_name_taken", name, 409);
				cur.name = name;
				cur.updatedAt = Date.now();
				return json(cur);
			}
			if (m === "DELETE") {
				if (cur.holder) return this.conflict(cur);
				this.profiles.splice(i, 1);
				for (const proj of this.deps.projects())
					if (proj.defaultBrowserProfileId === cur.id)
						delete proj.defaultBrowserProfileId;
				return new Response(null, { status: 204 });
			}
			return null;
		}
		const desk =
			/^orbs\/([^/]+)\/desktop\/(profile|profile\/save|browser\/start|browser\/stop)$/.exec(
				p,
			);
		if (!desk) return null;
		const orb = this.deps.orbs.get(desk[1] ?? "");
		if (!orb) return err("not_found", "orb", 404);
		if (!orb.desktop) return err("orbd_outdated", "orbd has no desktop", 409);
		const op = desk[2];
		const desktop = orb.desktop;
		if (op === "profile" && m === "PUT") {
			const b = (await body()) as Schemas["OrbDesktopProfilePut"];
			if (!b.profileId) {
				this.release(orb.id);
				const { profile: _, ...rest } = desktop;
				const next = {
					...orb,
					desktop: { ...rest, browser: "stopped" as const },
				};
				this.deps.setOrb(next);
				return json(next);
			}
			const prof = this.profiles.find((x) => x.id === b.profileId);
			if (!prof) return err("profile_not_found", b.profileId, 404);
			const mode = b.profileMode ?? "exclusive";
			if (mode === "exclusive" && prof.holder && prof.holder.orbId !== orb.id)
				return this.conflict(prof);
			const next: Orb = {
				...orb,
				desktop: {
					...desktop,
					browser: "stopped",
					profile: this.bind(orb, prof, mode),
				},
			};
			this.deps.setOrb(next);
			return json(next);
		}
		if (op === "profile/save" && m === "POST") {
			const cur = desktop.profile;
			if (!cur) return err("no_profile", "no profile", 409);
			if (cur.mode === "readonly")
				return err("profile_readonly", "read-only copy", 409);
			const prof = this.profiles.find((x) => x.id === cur.id);
			if (!prof || prof.holder?.orbId !== orb.id)
				return err("no_profile", "not held", 409);
			prof.revision += 1;
			prof.savedAt = Date.now();
			delete prof.saveError;
			this.deps.setOrb({
				...orb,
				desktop: { ...desktop, profile: { ...cur, revision: prof.revision } },
			});
			return json(prof);
		}
		if (op?.startsWith("browser/") && m === "POST") {
			const next: Orb = {
				...orb,
				desktop: {
					...desktop,
					browser: op === "browser/start" ? "running" : "stopped",
				},
			};
			this.deps.setOrb(next);
			return json(next);
		}
		return null;
	}
}
