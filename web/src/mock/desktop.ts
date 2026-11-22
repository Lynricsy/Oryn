/**
 * mock 的浏览器 Profile 与 orb 桌面接口（M8，与控制面同形）：Profile 增删改查、orb 选用 / 切换 / 解除 Profile
 * （独占被占用 → 409 `ProfileInUseConflict`；只读 → 只读副本；接管 → 原持有者降级为只读并推送它的 `orb`）、
 * 立即保存（修订 +1）、浏览器启停、桌面控制（接管 / 交还；回合与 `desktop_ask_user` 由 server 的脚本经
 * `turn` / `setAsk` 驱动，状态推导同 orbd）；新建线程时按 `browserProfileId` / `profileMode` 给新 orb 定 Profile。
 * 任务栏（M25）：有桌面的 orb 都带镜像清单里的 6 个应用（`seed` 给夹具 orb 摆好初始窗口），启动 300ms 后出现新窗口，
 * 窗口操作立即生效；agent 操作中两者都回 409 `control_agent`。图标是中性的占位 SVG（`iconDataUrl` 供 `<img>` 拦截用）。
 * 改动经 `setOrb` 写回内存并广播 `orb`。
 */
import type { Orb, Project, Schemas } from "../api/client";
import { browserProfiles, DESKTOP_THREADS } from "./fixtures";

type Json = Record<string, unknown>;
type Profile = Schemas["BrowserProfile"];
type Holder = Schemas["BrowserProfileHolder"];
type Mode = Schemas["BrowserProfileMode"];
type Desktop = Schemas["OrbDesktop"];
type Win = Schemas["OrbDesktopWindow"];

/** 任务栏应用：与镜像的 `desktop-apps.json` 同序同名；`title` 是新窗口的标题，`glyph` 是占位图标里的线条。 */
const APPS = [
	{
		id: "chromium",
		name: "Chromium",
		title: "New Tab - Chromium",
		glyph: '<circle cx="24" cy="24" r="9"/>',
	},
	{
		id: "terminal",
		name: "Terminal",
		title: "lyn@orb: ~/workspace",
		glyph: '<path d="M15 18l7 6-7 6M25 31h9"/>',
	},
	{
		id: "vscode",
		name: "VS Code",
		title: "Welcome - Visual Studio Code",
		glyph: '<path d="M19 16l-8 8 8 8M29 16l8 8-8 8"/>',
	},
	{
		id: "files",
		name: "Files",
		title: "workspace",
		glyph: '<path d="M12 16h9l3 3h12v14H12z"/>',
	},
	{
		id: "editor",
		name: "Text Editor",
		title: "Untitled 1 - Mousepad",
		glyph: '<path d="M15 17h18M15 23h18M15 29h12"/>',
	},
	{
		id: "libreoffice",
		name: "LibreOffice",
		title: "LibreOffice",
		glyph: '<path d="M16 12h11l7 7v17H16z"/>',
	},
] as const;

/** 启动到窗口出现的延迟。 */
const LAUNCH_MS = 300;

function iconSvg(glyph: string): string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><rect x="3" y="3" width="42" height="42" rx="11" fill="#7d838c"/><g fill="none" stroke="#fff" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">${glyph}</g></svg>`;
}

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
	/** 桌面控制：用户接管时间、回合中的 orb、待答的桌面请求（按 orb）。 */
	private readonly held = new Map<string, number>();
	private readonly working = new Set<string>();
	private readonly asks = new Map<string, Schemas["OrbDesktopAsk"]>();
	/** 已发起启动、窗口还没出现的应用（按 orb）。 */
	private readonly launching = new Map<string, Set<string>>();
	/** 下一个窗口 id（X 窗口 id 的十进制）。 */
	private wid = 0x0400_0001;
	private readonly deps: DesktopDeps;

	constructor(deps: DesktopDeps) {
		this.deps = deps;
	}

	/** 按窗口与浏览器状态补上 `apps`（`chromium` 另外在浏览器运行时算运行中），连同 `windows` 一起返回。 */
	private withApps(orbId: string, desktop: Desktop, windows: Win[]): Desktop {
		const pending = this.launching.get(orbId);
		return {
			...desktop,
			apps: APPS.map((a) => ({
				id: a.id,
				name: a.name,
				running:
					windows.some((w) => w.appId === a.id) ||
					(a.id === "chromium" && desktop.browser === "running"),
				launching: pending?.has(a.id) ?? false,
			})),
			windows,
		};
	}

	/** 写入新的窗口列表（与浏览器状态）并推送；没有任务栏的 orb 不动。 */
	private pushWindows(
		orbId: string,
		windows: Win[],
		browser?: Desktop["browser"],
	): Orb | undefined {
		const orb = this.deps.orbs.get(orbId);
		if (!orb?.desktop?.apps) return orb;
		const desktop = browser ? { ...orb.desktop, browser } : orb.desktop;
		const next: Orb = {
			...orb,
			desktop: this.withApps(orbId, desktop, windows),
		};
		this.deps.setOrb(next);
		return next;
	}

	private newWindow(
		appId: string | undefined,
		title: string,
		active = true,
	): Win {
		return {
			id: String(this.wid++),
			...(appId ? { appId } : {}),
			title,
			active,
			minimized: false,
		};
	}

	/**
	 * 给夹具里有桌面的 orb 摆初始窗口（server 建好 orb 后调用一次，不广播）：浏览器在跑的有一个 Chromium 窗口；
	 * 主线程的 orb 另有两个终端（一个最小化）和一个不属于任何应用的窗口，任务栏的圆点、活动底色、多窗口菜单与「其它窗口」都看得到。
	 * Profile 被占用场景的 orb 保留为旧 orbd（不上报 apps）：没有任务栏，舞台中央仍是「启动浏览器」。
	 */
	seed() {
		for (const orb of this.deps.orbs.values()) {
			if (!orb.desktop || orb.threadIds.includes(DESKTOP_THREADS.inUse))
				continue;
			const windows: Win[] = [];
			if (orb.desktop.browser === "running")
				windows.push(
					this.newWindow("chromium", "Pull requests · oryn - Chromium"),
				);
			if (orb.threadIds.includes(DESKTOP_THREADS.connected))
				windows.push(
					this.newWindow("terminal", "lyn@orb: ~/workspace/repo", false),
					{
						...this.newWindow("terminal", "bun run dev", false),
						minimized: true,
					},
					this.newWindow(undefined, "xclock", false),
				);
			this.deps.orbs.set(orb.id, {
				...orb,
				desktop: this.withApps(orb.id, orb.desktop, windows),
			});
		}
	}

	/** `<img>` 的图标地址 → 占位 SVG 的 data: 地址；不是图标地址或应用不在清单里时返回 undefined。 */
	iconDataUrl(src: string): string | undefined {
		const m = /\/api\/v1\/orbs\/[^/]+\/desktop\/apps\/([^/]+)\/icon$/.exec(src);
		const app = m && APPS.find((a) => a.id === decodeURIComponent(m[1] ?? ""));
		return app
			? `data:image/svg+xml,${encodeURIComponent(iconSvg(app.glyph))}`
			: undefined;
	}

	private controlOf(orbId: string): Schemas["OrbDesktopControl"] {
		const since = this.held.get(orbId);
		const ask = this.asks.get(orbId);
		const state = ask
			? "ask"
			: since !== undefined
				? "user"
				: this.working.has(orbId)
					? "agent"
					: "idle";
		return {
			state,
			userHeld: since !== undefined,
			...(since !== undefined ? { userSince: since } : {}),
			...(ask ? { ask } : {}),
		};
	}

	/** 按当前数据重算 orb 的控制状态并推送；返回推送后的 orb。 */
	private pushControl(orbId: string): Orb | undefined {
		const orb = this.deps.orbs.get(orbId);
		if (!orb?.desktop) return orb;
		const next: Orb = {
			...orb,
			desktop: { ...orb.desktop, control: this.controlOf(orbId) },
		};
		this.deps.setOrb(next);
		return next;
	}

	/** 回合开始 / 结束（对应 orbd 看到的 agent_start / 终结的 agent_end）。 */
	turn(orbId: string | undefined, working: boolean) {
		if (!orbId) return;
		if (working) this.working.add(orbId);
		else this.working.delete(orbId);
		this.pushControl(orbId);
	}

	/** 登记 / 清除待答的 `desktop_ask_user` 请求。 */
	setAsk(orbId: string | undefined, ask: Schemas["OrbDesktopAsk"] | undefined) {
		if (!orbId) return;
		if (ask) this.asks.set(orbId, ask);
		else this.asks.delete(orbId);
		this.pushControl(orbId);
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
		const base: Schemas["OrbDesktop"] = this.withApps(
			"",
			{ displayReady: true, browser: "stopped" },
			[],
		);
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
		const appOp = /^orbs\/([^/]+)\/desktop\/apps\/([^/]+)\/(launch|icon)$/.exec(
			p,
		);
		const winOp =
			/^orbs\/([^/]+)\/desktop\/windows\/([^/]+)\/(activate|minimize|close)$/.exec(
				p,
			);
		if (appOp || winOp) return this.dock(m, appOp, winOp);
		const desk =
			/^orbs\/([^/]+)\/desktop\/(profile|profile\/save|browser\/start|browser\/stop|control)$/.exec(
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
					desktop: this.withBrowser(orb.id, rest, "stopped"),
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
					...this.withBrowser(orb.id, desktop, "stopped"),
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
				desktop: this.withBrowser(
					orb.id,
					desktop,
					op === "browser/start" ? "running" : "stopped",
				),
			};
			this.deps.setOrb(next);
			return json(next);
		}
		if (op === "control" && m === "POST") {
			const b = (await body()) as Schemas["OrbDesktopControlRequest"];
			if (b.action !== "take" && b.action !== "release")
				return err("invalid_request", "action", 400);
			if (b.action === "release") this.held.delete(orb.id);
			else if (!this.held.has(orb.id)) this.held.set(orb.id, Date.now());
			return json(this.pushControl(orb.id));
		}
		return null;
	}

	/** 浏览器启停时同步 Chromium 窗口：启动时没有就开一个，关闭时全部去掉（没有任务栏的 orb 只改 `browser`）。 */
	private withBrowser(
		orbId: string,
		desktop: Desktop,
		browser: Desktop["browser"],
	): Desktop {
		if (!desktop.apps) return { ...desktop, browser };
		const wins = desktop.windows ?? [];
		const windows =
			browser !== "running"
				? wins.filter((w) => w.appId !== "chromium")
				: wins.some((w) => w.appId === "chromium")
					? wins
					: [
							...wins.map((w) => ({ ...w, active: false })),
							this.newWindow("chromium", APPS[0].title),
						];
		return this.withApps(orbId, { ...desktop, browser }, windows);
	}

	/** 任务栏（M25）：启动应用、取图标、窗口操作。 */
	private dock(
		m: string,
		appOp: RegExpExecArray | null,
		winOp: RegExpExecArray | null,
	): Response | null {
		const orb = this.deps.orbs.get((appOp ?? winOp)?.[1] ?? "");
		if (!orb) return err("not_found", "orb", 404);
		const desktop = orb.desktop;
		if (!desktop?.apps)
			return err("orbd_outdated", "orbd has no desktop apps", 409);
		if (appOp) {
			const id = appOp[2] ?? "";
			const app = APPS.find((a) => a.id === id);
			if (appOp[3] === "icon") {
				if (m !== "GET") return null;
				if (orb.state !== "ready")
					return err("orb_unavailable", "orb is not ready", 409);
				if (!app) return err("app_not_found", id, 404);
				return new Response(iconSvg(app.glyph), {
					headers: {
						"Content-Type": "image/svg+xml",
						"Cache-Control": "private, max-age=86400",
					},
				});
			}
			if (m !== "POST") return null;
			if (this.controlOf(orb.id).state === "agent")
				return err("control_agent", "agent is using the desktop", 409);
			if (!app) return err("app_not_found", id, 404);
			const pending = this.launching.get(orb.id) ?? new Set<string>();
			pending.add(app.id);
			this.launching.set(orb.id, pending);
			const chromium = app.id === "chromium";
			setTimeout(() => {
				pending.delete(app.id);
				const wins = this.deps.orbs.get(orb.id)?.desktop?.windows ?? [];
				this.pushWindows(
					orb.id,
					[
						...wins.map((w) => ({ ...w, active: false })),
						this.newWindow(app.id, app.title),
					],
					chromium ? "running" : undefined,
				);
			}, LAUNCH_MS);
			return json(
				this.pushWindows(
					orb.id,
					desktop.windows ?? [],
					chromium && desktop.browser !== "running" ? "starting" : undefined,
				),
			);
		}
		if (!winOp || m !== "POST") return null;
		if (this.controlOf(orb.id).state === "agent")
			return err("control_agent", "agent is using the desktop", 409);
		const action = winOp[3];
		const wins = desktop.windows ?? [];
		const target = wins.find((w) => w.id === winOp[2]);
		if (!target) return err("window_not_found", winOp[2] ?? "", 404);
		const windows =
			action === "close"
				? wins.filter((w) => w !== target)
				: wins.map((w) =>
						w === target
							? {
									...w,
									active: action === "activate",
									minimized: action === "minimize",
								}
							: action === "activate"
								? { ...w, active: false }
								: w,
					);
		// 关掉最后一个 Chromium 窗口，浏览器随之退出
		const browserGone =
			target.appId === "chromium" &&
			!windows.some((w) => w.appId === "chromium");
		return json(
			this.pushWindows(orb.id, windows, browserGone ? "stopped" : undefined),
		);
	}
}
