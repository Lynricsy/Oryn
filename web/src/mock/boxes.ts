/**
 * mock 后端的 M23（Boxes）部分：box 列表 / 详情 / 新建 / 改名 / 时段 / 启停 / 删除、活动与 21 天用量、app 的公开性、
 * 重启、回滚、下线、取消部署、release 与密钥。校验与错误码同 orynd（`internal/boxes`）；`execBox` 回 501。
 * `MockServer.handleFetch` 调 `handle`，不归这里的请求返回 null；写操作后 `deps.changed()`（WS `boxes.changed`）。
 * box 的常驻 orb 放进 `deps.orbs`（`box: true`），app 服务放进 orb 的 `services` 镜像并带日志，
 * 每个 app 的 `~/apps/<app>/current/` 在 mock 文件系统里有几份文件（box 线程的文件面板落在那里）。
 * 预置（`localStorage["oryn.mock.boxes"] === "empty"` 时没有 box）：
 * - `workbench`：运行中，时段 05:00–23:00 Europe/Berlin；`bug-tracker` 公开有预览、两个服务（web、worker），
 *   `team-wiki` 私有（单仓子目录 `apps/wiki`），`tiny-crm` 首次部署进行中（没有 live）；
 * - `lab`：主人停机，一个已部署的 `status-page`。
 * app 地址固定是 `https://<app>.apps.oryn.test`（截图与审计按这个地址拦截 iframe）。
 */
import type { Orb, Schemas, Thread } from "../api/client";
import { makeOrb, providers } from "./fixtures";
import { seedOrbFiles } from "./orbfs";

type Box = Schemas["Box"];
type BoxApp = Schemas["BoxApp"];
type BoxRelease = Schemas["BoxRelease"];
type BoxActivity = Schemas["BoxActivity"];
type BoxUsageDay = Schemas["BoxUsageDay"];
type BoxSchedule = Schemas["BoxSchedule"];
type OrbService = Schemas["OrbService"];

export interface BoxDeps {
	/** box、app、活动或用量变了：推送 WS `boxes.changed` */
	changed(): void;
	threads: ReadonlyMap<string, Thread>;
	/** 全部 orb；box 的常驻 orb 由这里放进去、改状态 */
	orbs: Map<string, Orb>;
	/** orb 状态或服务镜像变了：推送 WS `orb` */
	orbChanged(orb: Orb): void;
	/** 服务日志（服务名 → 文本），服务日志接口读它 */
	serviceLogs: Map<string, string>;
}

/** 种子 app 的地址域（`https://<app>.apps.oryn.test`）。 */
export const MOCK_APP_DOMAIN = "apps.oryn.test";
/** 置为 `"empty"` 时 mock 没有 box（截图与审计看介绍页）。 */
export const MOCK_BOXES_KEY = "oryn.mock.boxes";

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
const NOW = Date.now();
const MAX_BOXES = 16;
const MAX_SECRETS = 64;
const BOX_NAME_RE = /^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$/;
const SECRET_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;
const HHMM_RE = /^([01]\d|2[0-3]):[0-5]\d$/;
const RESERVED_ENV: Record<string, true> = {
	PORT: true,
	ORYN_DEPLOYMENT_URL: true,
	ORYN_DEPLOYMENT_NAME: true,
	ORYN_DEPLOYMENT_BOX: true,
	ORYN_DEPLOYMENT_DATA_DIRECTORY: true,
};

interface MockSecret {
	value: string;
	secret: boolean;
	updatedAt: number;
}

interface MockApp {
	name: string;
	public: boolean;
	repo: string;
	root: string;
	source: boolean;
	port: number;
	/** 新到旧，含 live */
	releases: BoxRelease[];
	deploying: boolean;
	preview: boolean;
	createdAt: number;
	updatedAt: number;
	/** box.yaml 里的服务名（监督器里是 `<app>--<svc>`） */
	services: string[];
	secrets: Map<string, MockSecret>;
}

interface MockBox {
	id: string;
	name: string;
	orbId: string;
	desired: Box["desired"];
	desiredBy: "owner" | "schedule";
	schedule?: BoxSchedule;
	createdAt: number;
	apps: MockApp[];
	/** 活动表（新到旧）；线程项按 orb 上的线程实时算 */
	activity: BoxActivity[];
	usage: BoxUsageDay[];
}

function json(body: unknown, status = 200): Response {
	return Response.json(body, { status });
}

function error(status: number, code: string, message: string): Response {
	return json({ code, message }, status);
}

function sleep(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

function sha(seed: string): string {
	let h = 0x811c9dc5;
	let out = "";
	for (let round = 0; out.length < 40; round++) {
		for (const ch of `${seed}:${round}`) {
			h ^= ch.charCodeAt(0);
			h = Math.imul(h, 0x01000193) >>> 0;
		}
		out += h.toString(16).padStart(8, "0");
	}
	return out.slice(0, 40);
}

function release(
	app: string,
	n: number,
	p: Partial<BoxRelease> & Pick<BoxRelease, "state" | "activatedAt">,
): BoxRelease {
	return {
		id: `bxr_01JDBX${sha(`${app}-${n}`).toUpperCase().slice(0, 20)}`,
		commit: sha(`${app}-commit-${n}`),
		branch: "main",
		repo: `github.com/acme/${app}`,
		root: "",
		source: true,
		sizeBytes: 3_400_000 + n * 120_000,
		createdAt: p.activatedAt - 2 * MIN,
		...p,
	};
}

/** orb 状态 → box 状态（同 `internal/boxes` 的映射）。 */
function boxState(o: Orb["state"]): Box["state"] {
	switch (o) {
		case "ready":
			return "running";
		case "freezing":
		case "stopping":
			return "stopping";
		case "frozen":
		case "stopped":
			return "stopped";
		case "failed":
			return "failed";
		default:
			return "starting";
	}
}

function appService(
	app: string,
	svc: string,
	port: number,
	running: boolean,
	i: number,
): OrbService {
	return {
		name: `${app}--${svc}`,
		source: "app",
		app,
		command: svc === "web" ? "bun run start" : "bun run worker",
		cwd: "",
		port,
		links: [],
		restarts: 0,
		...(running
			? { status: "running", pid: 4100 + i, startedAt: NOW - 2 * HOUR }
			: { status: "stopped" }),
	};
}

/** 最近 21 个 UTC 自然日的用量（确定性的伪随机，box 创建之前两项都是 0）。 */
function seedUsage(
	createdAt: number,
	threads: (i: number) => number,
	up: (i: number) => number,
): BoxUsageDay[] {
	const today = Date.UTC(
		new Date(NOW).getUTCFullYear(),
		new Date(NOW).getUTCMonth(),
		new Date(NOW).getUTCDate(),
	);
	return Array.from({ length: 21 }, (_, k) => {
		const day = today - (20 - k) * DAY;
		const alive = day + DAY > createdAt;
		return {
			date: new Date(day).toISOString().slice(0, 10),
			threads: alive ? threads(k) : 0,
			upRatio: alive ? up(k) : 0,
		};
	});
}

export class MockBoxes {
	private boxes: MockBox[] = [];
	private seq = 1;
	private activitySeq = 1;
	private readonly deps: BoxDeps;

	constructor(deps: BoxDeps) {
		this.deps = deps;
		let empty = false;
		try {
			empty = globalThis.localStorage?.getItem(MOCK_BOXES_KEY) === "empty";
		} catch {
			// 无 localStorage
		}
		if (!empty) this.seed();
	}

	/** `orbId` 是不是某台 box 的常驻 orb。 */
	isBoxOrb(orbId: string | undefined): boolean {
		return Boolean(orbId && this.boxes.some((b) => b.orbId === orbId));
	}

	/** `orbId` 是不是一台期望状态为 stopped 的 box 的 orb（线程的消息与唤醒被闸门挡下）。 */
	isStopped(orbId: string | undefined): boolean {
		return this.boxes.some((b) => b.orbId === orbId && b.desired === "stopped");
	}

	/**
	 * `executor.kind = box` 解析成 orb 与工作目录（同 `boxes.Service.ThreadTarget`）：box 停着 → 409 `box_stopped`；
	 * app 不在这台 box 上 → 404 `app_not_found`；没有 live → 409 `app_not_deployed`。
	 */
	threadTarget(
		ref: string,
		app?: string,
	): { orbId: string; cwd: string } | Response {
		const box = this.find(ref);
		if (!box) return error(404, "box_not_found", `box ${ref} not found`);
		if (box.desired === "stopped")
			return error(
				409,
				"box_stopped",
				"this box is stopped; start it from the Boxes page",
			);
		if (!app) return { orbId: box.orbId, cwd: "~" };
		const a = box.apps.find((x) => x.name === app);
		if (!a)
			return error(404, "app_not_found", `app ${app} not found on ${box.name}`);
		if (!this.live(a))
			return error(
				409,
				"app_not_deployed",
				`app ${app} has no live release yet`,
			);
		const sub = a.source && a.root ? `/${a.root}` : "";
		return { orbId: box.orbId, cwd: `~/apps/${app}/current${sub}` };
	}

	/** box 的 orb 上增删了线程（活动流与用量都数线程）。 */
	threadsChanged(orbId: string | undefined): void {
		if (this.isBoxOrb(orbId)) this.deps.changed();
	}

	async handle(
		req: Request,
		p: string,
		m: string,
		url: URL,
	): Promise<Response | null> {
		if (p !== "boxes" && !p.startsWith("boxes/") && p !== "box-activity")
			return null;
		const body = async <T>(): Promise<T> => {
			try {
				return (await req.json()) as T;
			} catch {
				return {} as T;
			}
		};
		const limit = Math.min(
			100,
			Math.max(1, Number(url.searchParams.get("limit") ?? 50) || 50),
		);
		if (p === "box-activity" && m === "GET")
			return json({ items: this.activity(undefined, limit) });
		if (p === "boxes" && m === "GET")
			return json({ boxes: this.visible().map((b) => this.view(b)) });
		if (p === "boxes" && m === "POST")
			return this.create(await body<Schemas["CreateBoxRequest"]>());
		const bm =
			/^boxes\/([^/]+)(?:\/(start|stop|activity|usage|exec|apps\/([^/]+)(?:\/(restart|rollback|cancel-deploy|releases|secrets(?:\/([^/]+))?))?))?$/.exec(
				p,
			);
		if (!bm) return null;
		const box = this.find(decodeURIComponent(bm[1] ?? ""));
		if (!box) return error(404, "box_not_found", `box ${bm[1]} not found`);
		const sub = bm[2];
		if (!sub) {
			if (m === "GET") return json(this.view(box));
			if (m === "PATCH")
				return this.update(box, await body<Schemas["UpdateBoxRequest"]>());
			if (m === "DELETE") return this.remove(box);
			return null;
		}
		if (sub === "start" && m === "POST") return this.setDesired(box, "running");
		if (sub === "stop" && m === "POST") return this.setDesired(box, "stopped");
		if (sub === "activity" && m === "GET")
			return json({ items: this.activity(box, limit) });
		if (sub === "usage" && m === "GET") return json({ days: box.usage });
		if (sub === "exec")
			return error(501, "not_implemented", "mock: exec is not simulated");
		const appName = decodeURIComponent(bm[3] ?? "");
		const app = box.apps.find((a) => a.name === appName);
		if (!app) return error(404, "app_not_found", `app ${appName} not found`);
		const op = bm[4];
		if (!op) {
			if (m === "PATCH") {
				const b = await body<Schemas["UpdateBoxAppRequest"]>();
				if (typeof b.public !== "boolean")
					return error(400, "invalid_request", "public");
				if (app.public !== b.public) {
					app.public = b.public;
					app.updatedAt = Date.now();
					this.log(box, "visibility", "owner", {
						app: app.name,
						public: b.public,
					});
					this.deps.changed();
				}
				return json(this.appView(box, app));
			}
			if (m === "DELETE") return this.undeploy(box, app);
			return null;
		}
		if (op === "restart" && m === "POST") return this.restart(box, app);
		if (op === "rollback" && m === "POST")
			return this.rollback(
				box,
				app,
				await body<Schemas["BoxRollbackRequest"]>(),
			);
		if (op === "cancel-deploy" && m === "POST") return this.cancel(box, app);
		if (op === "releases" && m === "GET")
			return json({ releases: app.releases });
		if (op === "secrets" && m === "GET")
			return json({
				secrets: [...app.secrets]
					.sort(([a], [b]) => a.localeCompare(b))
					.map(([name, s]) => ({
						name,
						secret: s.secret,
						updatedAt: s.updatedAt,
						...(s.secret ? {} : { value: s.value }),
					})),
			});
		if (op.startsWith("secrets/")) {
			const name = decodeURIComponent(bm[5] ?? "");
			if (m === "PUT")
				return this.putSecret(
					app,
					name,
					await body<Schemas["PutBoxSecretRequest"]>(),
				);
			if (m === "DELETE") {
				if (app.secrets.delete(name)) this.deps.changed();
				return new Response(null, { status: 204 });
			}
		}
		return null;
	}

	// ------------------------------------------------------------ 视图

	private visible(): MockBox[] {
		return this.boxes.filter((b) => {
			const s = this.deps.orbs.get(b.orbId)?.state;
			return s !== "deleting" && s !== "deleted";
		});
	}

	private find(ref: string): MockBox | undefined {
		return this.visible().find((b) => b.id === ref || b.name === ref);
	}

	private live(a: MockApp): BoxRelease | undefined {
		return a.releases.find((r) => r.state === "live");
	}

	private appView(box: MockBox, a: MockApp): BoxApp {
		const orb = this.deps.orbs.get(box.orbId);
		const live = this.live(a);
		return {
			name: a.name,
			url: `https://${a.name}.${MOCK_APP_DOMAIN}`,
			public: a.public,
			repo: a.repo,
			root: a.root,
			source: a.source,
			port: a.port,
			...(live ? { live } : {}),
			deploying: a.deploying,
			preview: a.preview,
			services: (orb?.services ?? []).filter(
				(s) => s.source === "app" && s.app === a.name,
			),
			createdAt: a.createdAt,
			updatedAt: a.updatedAt,
		};
	}

	private view(box: MockBox): Box {
		const orb = this.deps.orbs.get(box.orbId);
		const lastDeployAt = box.apps.reduce(
			(max, a) => Math.max(max, this.live(a)?.activatedAt ?? 0),
			0,
		);
		const state = boxState(orb?.state ?? "stopped");
		return {
			id: box.id,
			name: box.name,
			orbId: box.orbId,
			state,
			desired: box.desired,
			...(box.desired === "stopped" ? { stoppedBy: box.desiredBy } : {}),
			...(state === "failed" && orb?.failureReason
				? { failureReason: orb.failureReason }
				: {}),
			provider: orb?.providerName ?? "local",
			type: orb?.type ?? "medium",
			cpus: orb?.cpus ?? 0,
			memoryMB: orb?.memoryMB ?? 0,
			...(box.schedule ? { schedule: box.schedule } : {}),
			createdAt: box.createdAt,
			...(lastDeployAt ? { lastDeployAt } : {}),
			apps: [...box.apps]
				.sort((a, b) => a.name.localeCompare(b.name))
				.map((a) => this.appView(box, a)),
		};
	}

	/** 活动表加上 box orb 上近 30 天的工作线程，新到旧截到 `limit`；`box` 省略 = 全部 box。 */
	private activity(box: MockBox | undefined, limit: number): BoxActivity[] {
		const items: BoxActivity[] = [];
		for (const b of box ? [box] : this.visible()) {
			items.push(...b.activity);
			for (const t of this.deps.threads.values()) {
				if (t.orbId !== b.orbId || (t.kind && t.kind !== "work")) continue;
				if (t.createdAt < NOW - 30 * DAY) continue;
				const app = /^~\/apps\/([^/]+)\//.exec(t.cwd ?? "")?.[1];
				items.push({
					id: `t:${t.id}`,
					boxId: b.id,
					boxName: b.name,
					ts: t.createdAt,
					kind: "thread",
					actor: "owner",
					threadId: t.id,
					title: t.title,
					...(app ? { app } : {}),
				});
			}
		}
		return items.sort((a, b) => b.ts - a.ts).slice(0, limit);
	}

	private log(
		box: MockBox,
		kind: BoxActivity["kind"],
		actor: BoxActivity["actor"],
		extra: Partial<BoxActivity> = {},
		ts = Date.now(),
	): void {
		box.activity.unshift({
			id: `a:${this.activitySeq++}`,
			boxId: box.id,
			boxName: box.name,
			ts,
			kind,
			actor,
			...extra,
		});
	}

	// ------------------------------------------------------------ 写操作

	private create(b: Schemas["CreateBoxRequest"]): Response {
		const name = String(b.name ?? "");
		if (!BOX_NAME_RE.test(name) || name.includes("--"))
			return error(
				400,
				"invalid_request",
				`invalid box name ${JSON.stringify(name)}`,
			);
		if (this.visible().length >= MAX_BOXES)
			return error(409, "box_limit", `at most ${MAX_BOXES} boxes`);
		if (this.visible().some((x) => x.name === name))
			return error(409, "box_exists", `box ${name} already exists`);
		const prov = b.provider
			? providers.find((x) => x.name === b.provider || x.id === b.provider)
			: providers.find((x) => !x.disabledAt);
		if (!prov)
			return error(400, "invalid_request", `provider ${b.provider} not found`);
		if (prov.disabledAt)
			return error(400, "invalid_request", `provider ${prov.name} is disabled`);
		const typeName = b.type ?? prov.defaultType ?? prov.orbTypes[0]?.name ?? "";
		const type = prov.orbTypes.find((x) => x.name === typeName);
		if (!type)
			return error(400, "invalid_request", `orb type ${typeName} not found`);
		const n = this.seq++;
		const box: MockBox = {
			id: `box_01JDBXNEW${String(n).padStart(17, "0")}`,
			name,
			orbId: `orb-box-new-${n}`,
			desired: "running",
			desiredBy: "owner",
			createdAt: Date.now(),
			apps: [],
			activity: [],
			usage: seedUsage(
				Date.now(),
				() => 0,
				() => 0,
			),
		};
		const orb: Orb = {
			...makeOrb(box.orbId, [], "spawning"),
			box: true,
			projectId: undefined,
			providerId: prov.id,
			providerName: prov.name,
			type: type.name,
			cpus: type.cpus,
			memoryMB: type.memoryMB,
			createdAt: Date.now(),
			stateChangedAt: Date.now(),
			services: [],
		};
		this.deps.orbs.set(orb.id, orb);
		this.deps.orbChanged(orb);
		this.boxes.push(box);
		this.log(box, "created", "owner");
		this.deps.changed();
		void this.drive(box, ["connecting", "setting_up", "ready"]);
		return json(this.view(box), 201);
	}

	/** 把 box 的 orb 依次推过这些状态（每步 800ms），每步推送一次。 */
	private async drive(box: MockBox, states: Orb["state"][]): Promise<void> {
		for (const state of states) {
			await sleep(800);
			const orb = this.deps.orbs.get(box.orbId);
			if (!orb || orb.state === "deleted") return;
			const running = state === "ready";
			const next: Orb = {
				...orb,
				state,
				stateChangedAt: Date.now(),
				services: (orb.services ?? []).map((s) => {
					if (s.source !== "app") return s;
					const { pid: _, startedAt: __, ...rest } = s;
					return running
						? {
								...rest,
								status: "running",
								pid: 4200 + this.seq++,
								startedAt: Date.now(),
							}
						: { ...rest, status: "stopped" };
				}),
			};
			this.deps.orbs.set(orb.id, next);
			this.deps.orbChanged(next);
			this.deps.changed();
		}
	}

	private update(box: MockBox, b: Schemas["UpdateBoxRequest"]): Response {
		if (b.name !== undefined && b.name !== box.name) {
			if (!BOX_NAME_RE.test(b.name) || b.name.includes("--"))
				return error(
					400,
					"invalid_request",
					`invalid box name ${JSON.stringify(b.name)}`,
				);
			if (this.visible().some((x) => x.name === b.name))
				return error(409, "box_exists", `box ${b.name} already exists`);
			if (box.apps.some((a) => a.deploying))
				return error(
					409,
					"deploy_in_progress",
					"a deploy is in progress on this box",
				);
			const from = box.name;
			box.name = b.name;
			for (const a of box.activity) a.boxName = b.name;
			this.log(box, "renamed", "owner", { from, to: b.name });
		}
		if (b.clearSchedule) {
			if (box.schedule) {
				box.schedule = undefined;
				this.log(box, "schedule", "owner");
			}
		} else if (b.schedule) {
			const s = b.schedule;
			if (!HHMM_RE.test(s.on) || !HHMM_RE.test(s.off) || s.on === s.off)
				return error(
					400,
					"invalid_request",
					"schedule on/off must be distinct HH:MM",
				);
			try {
				new Intl.DateTimeFormat("en", { timeZone: s.tz });
			} catch {
				return error(400, "invalid_request", `unknown time zone ${s.tz}`);
			}
			box.schedule = { on: s.on, off: s.off, tz: s.tz };
			this.log(box, "schedule", "owner");
		}
		this.deps.changed();
		return json(this.view(box));
	}

	private remove(box: MockBox): Response {
		this.boxes = this.boxes.filter((b) => b !== box);
		const orb = this.deps.orbs.get(box.orbId);
		if (orb) {
			const next: Orb = {
				...orb,
				state: "deleted",
				stateChangedAt: Date.now(),
			};
			this.deps.orbs.set(orb.id, next);
			this.deps.orbChanged(next);
		}
		this.deps.changed();
		return new Response(null, { status: 204 });
	}

	private setDesired(box: MockBox, desired: Box["desired"]): Response {
		if (box.desired !== desired) {
			box.desired = desired;
			box.desiredBy = "owner";
			this.log(box, desired === "running" ? "started" : "stopped", "owner");
			const orb = this.deps.orbs.get(box.orbId);
			if (orb) {
				const next: Orb = {
					...orb,
					state: desired === "running" ? "waking" : "stopping",
					stateChangedAt: Date.now(),
				};
				this.deps.orbs.set(orb.id, next);
				this.deps.orbChanged(next);
				void this.drive(box, [desired === "running" ? "ready" : "stopped"]);
			}
			this.deps.changed();
		}
		return json(this.view(box), 202);
	}

	private usable(box: MockBox): Response | null {
		if (box.desired === "stopped")
			return error(
				409,
				"box_stopped",
				"this box is stopped; start it from the Boxes page",
			);
		if (this.deps.orbs.get(box.orbId)?.state !== "ready")
			return error(409, "box_not_ready", "the box is not ready yet");
		return null;
	}

	private undeploy(box: MockBox, app: MockApp): Response {
		if (app.deploying)
			return error(
				409,
				"deploy_in_progress",
				"a deploy is in progress for this app",
			);
		const bad = this.usable(box);
		if (bad) return bad;
		box.apps = box.apps.filter((a) => a !== app);
		this.setServices(box, app.name, []);
		this.log(box, "undeployed", "owner", { app: app.name });
		this.deps.changed();
		return new Response(null, { status: 204 });
	}

	private restart(box: MockBox, app: MockApp): Response {
		if (app.deploying)
			return error(
				409,
				"deploy_in_progress",
				"a deploy is in progress for this app",
			);
		if (!this.live(app))
			return error(
				409,
				"app_not_deployed",
				`app ${app.name} has no live release yet`,
			);
		const bad = this.usable(box);
		if (bad) return bad;
		this.setServices(box, app.name, app.services);
		this.log(box, "restarted", "owner", { app: app.name });
		this.deps.changed();
		return json(this.appView(box, app));
	}

	private rollback(
		box: MockBox,
		app: MockApp,
		b: Schemas["BoxRollbackRequest"],
	): Response {
		if (app.deploying)
			return error(
				409,
				"deploy_in_progress",
				"a deploy is in progress for this app",
			);
		const target = b.releaseId
			? app.releases.find((r) => r.id === b.releaseId && r.state === "kept")
			: app.releases.find((r) => r.state === "kept");
		if (!target)
			return error(404, "release_not_found", "no kept release to roll back to");
		const bad = this.usable(box);
		if (bad) return bad;
		const now = Date.now();
		app.releases = app.releases.map((r) =>
			r.state === "live"
				? { ...r, state: "kept" }
				: r.id === target.id
					? { ...r, state: "live", activatedAt: now }
					: r,
		);
		app.repo = target.repo;
		app.root = target.root;
		app.source = target.source;
		app.updatedAt = now;
		this.setServices(box, app.name, app.services);
		this.log(box, "rolled_back", "owner", {
			app: app.name,
			commit: target.commit,
			releaseId: target.id,
		});
		this.deps.changed();
		return json(this.appView(box, app));
	}

	private cancel(box: MockBox, app: MockApp): Response {
		if (!app.deploying)
			return error(404, "deploy_not_found", `no open deploy for ${app.name}`);
		app.deploying = false;
		// 首次部署被取消：临时 app 行让出来
		if (!this.live(app)) box.apps = box.apps.filter((a) => a !== app);
		this.log(box, "deploy_failed", "owner", {
			app: app.name,
			error: "canceled",
		});
		this.deps.changed();
		return new Response(null, { status: 204 });
	}

	private putSecret(
		app: MockApp,
		name: string,
		b: Schemas["PutBoxSecretRequest"],
	): Response {
		if (!SECRET_NAME_RE.test(name))
			return error(
				400,
				"invalid_request",
				`invalid secret name ${JSON.stringify(name)}`,
			);
		if (RESERVED_ENV[name])
			return error(400, "invalid_request", `${name} is reserved`);
		const value = typeof b.value === "string" ? b.value : "";
		if (value.length > 64 * 1024)
			return error(400, "invalid_request", "a value is at most 64 KiB");
		if (!app.secrets.has(name) && app.secrets.size >= MAX_SECRETS)
			return error(
				400,
				"invalid_request",
				`at most ${MAX_SECRETS} secrets per app`,
			);
		app.secrets.set(name, {
			value,
			secret: b.secret !== false,
			updatedAt: Date.now(),
		});
		this.deps.changed();
		return new Response(null, { status: 204 });
	}

	/** 换掉 orb 镜像里某个 app 的服务（空 = 下线），新进程从头跑。 */
	private setServices(box: MockBox, app: string, services: string[]): void {
		const orb = this.deps.orbs.get(box.orbId);
		if (!orb) return;
		const port = box.apps.find((a) => a.name === app)?.port ?? 0;
		const others = (orb.services ?? []).filter(
			(s) => !(s.source === "app" && s.app === app),
		);
		const next: Orb = {
			...orb,
			services: [
				...others,
				...services.map((svc) =>
					appService(app, svc, port, orb.state === "ready", this.seq++),
				),
			],
		};
		this.deps.orbs.set(orb.id, next);
		this.deps.orbChanged(next);
	}

	// ------------------------------------------------------------ 种子

	private seed(): void {
		const app = (
			p: Omit<MockApp, "secrets" | "updatedAt"> & {
				secrets?: [string, string, boolean][];
			},
		): MockApp => ({
			...p,
			updatedAt:
				p.releases.find((r) => r.state === "live")?.activatedAt ?? p.createdAt,
			secrets: new Map(
				(p.secrets ?? []).map(([n, v, s]) => [
					n,
					{ value: v, secret: s, updatedAt: NOW - 6 * DAY },
				]),
			),
		});
		const workbenchCreated = NOW - 40 * DAY;
		const workbench: MockBox = {
			id: "box_01JDBX00000000000WORKBENCH",
			name: "workbench",
			orbId: "orb-box-workbench",
			desired: "running",
			desiredBy: "schedule",
			schedule: { on: "05:00", off: "23:00", tz: "Europe/Berlin" },
			createdAt: workbenchCreated,
			apps: [
				app({
					name: "bug-tracker",
					public: true,
					repo: "github.com/acme/bug-tracker",
					root: "",
					source: true,
					port: 30000,
					deploying: false,
					preview: true,
					createdAt: NOW - 30 * DAY,
					services: ["web", "worker"],
					releases: [
						release("bug-tracker", 3, {
							state: "live",
							activatedAt: NOW - 2 * HOUR,
						}),
						release("bug-tracker", 2, {
							state: "kept",
							activatedAt: NOW - 2 * DAY,
							branch: "main",
						}),
						release("bug-tracker", 1, {
							state: "kept",
							activatedAt: NOW - 9 * DAY,
						}),
					],
					secrets: [
						["DATABASE_URL", "postgres://tracker:•••@db/tracker", true],
						["LOG_LEVEL", "info", false],
					],
				}),
				app({
					name: "team-wiki",
					public: false,
					repo: "github.com/acme/wiki",
					root: "apps/wiki",
					source: true,
					port: 30001,
					deploying: false,
					preview: false,
					createdAt: NOW - 20 * DAY,
					services: ["web"],
					releases: [
						release("team-wiki", 2, {
							state: "live",
							activatedAt: NOW - 26 * HOUR,
							repo: "github.com/acme/wiki",
							root: "apps/wiki",
						}),
						release("team-wiki", 1, {
							state: "kept",
							activatedAt: NOW - 8 * DAY,
							repo: "github.com/acme/wiki",
							root: "apps/wiki",
						}),
					],
				}),
				app({
					name: "tiny-crm",
					public: true,
					repo: "github.com/acme/tiny-crm",
					root: "",
					source: true,
					port: 30002,
					deploying: true,
					preview: false,
					createdAt: NOW - 3 * MIN,
					services: [],
					releases: [],
				}),
			],
			activity: [],
			usage: seedUsage(
				workbenchCreated,
				(k) =>
					[2, 0, 3, 1, 4, 2, 0, 1, 5, 3, 2, 0, 0, 2, 6, 3, 1, 2, 4, 3, 2][k] ??
					0,
				(k) => (k === 20 ? 0.71 : 0.74 + ((k * 7) % 5) / 100),
			),
		};
		const labCreated = NOW - 12 * DAY;
		const lab: MockBox = {
			id: "box_01JDBX00000000000000000LAB",
			name: "lab",
			orbId: "orb-box-lab",
			desired: "stopped",
			desiredBy: "owner",
			createdAt: labCreated,
			apps: [
				app({
					name: "status-page",
					public: true,
					repo: "github.com/acme/status-page",
					root: "",
					source: false,
					port: 30000,
					deploying: false,
					preview: false,
					createdAt: NOW - 11 * DAY,
					services: ["web"],
					releases: [
						release("status-page", 1, {
							state: "live",
							activatedAt: NOW - 4 * DAY,
							source: false,
						}),
					],
				}),
			],
			activity: [],
			usage: seedUsage(
				labCreated,
				(k) => (k % 4 === 0 ? 1 : 0),
				(k) => (k === 20 ? 0.2 : 1),
			),
		};
		this.boxes = [workbench, lab];

		// 活动（旧到新登记，新的在前）
		this.log(workbench, "created", "owner", {}, workbenchCreated);
		this.log(lab, "created", "owner", {}, labCreated);
		this.log(workbench, "schedule", "owner", {}, NOW - 25 * DAY);
		this.log(
			workbench,
			"visibility",
			"agent",
			{ app: "team-wiki", public: false },
			NOW - 26 * HOUR - 5 * MIN,
		);
		for (const [b, a, rel, actor] of [
			[lab, "status-page", lab.apps[0]?.releases[0], "agent"],
			[workbench, "team-wiki", workbench.apps[1]?.releases[0], "agent"],
		] as const)
			if (rel)
				this.log(
					b,
					"deployed",
					actor,
					{ app: a, commit: rel.commit, releaseId: rel.id },
					rel.activatedAt,
				);
		this.log(
			workbench,
			"deploy_failed",
			"agent",
			{
				app: "bug-tracker",
				commit: sha("bug-tracker-commit-x"),
				error: "not_listening: no process is listening on $PORT",
			},
			NOW - 3 * HOUR,
		);
		const bt = workbench.apps[0]?.releases[0];
		if (bt)
			this.log(
				workbench,
				"deployed",
				"agent",
				{
					app: "bug-tracker",
					commit: bt.commit,
					releaseId: bt.id,
				},
				bt.activatedAt,
			);
		this.log(lab, "stopped", "owner", {}, NOW - 5 * HOUR);
		this.log(
			workbench,
			"restarted",
			"owner",
			{ app: "bug-tracker" },
			NOW - 40 * MIN,
		);
		for (const b of this.boxes) b.activity.sort((x, y) => y.ts - x.ts);

		// 常驻 orb、服务镜像与日志、文件
		const base = (b: MockBox, state: Orb["state"]): Orb => ({
			...makeOrb(b.orbId, [], state),
			box: true,
			projectId: undefined,
			type: "medium",
			createdAt: b.createdAt,
			stateChangedAt: state === "ready" ? NOW - 7 * HOUR : NOW - 5 * HOUR,
			services: b.apps.flatMap((a) =>
				a.services.map((svc, i) =>
					appService(a.name, svc, a.port, state === "ready", i + a.port),
				),
			),
		});
		this.deps.orbs.set(workbench.orbId, base(workbench, "ready"));
		this.deps.orbs.set(lab.orbId, base(lab, "stopped"));
		const logs: Record<string, string> = {
			"bug-tracker--web":
				"$ bun run start\nlistening on http://0.0.0.0:30000\nGET / 200 4ms\nGET /api/issues?state=open 200 12ms\nPOST /api/issues 201 18ms\n",
			"bug-tracker--worker":
				"$ bun run worker\nworker ready (queue: notify)\nsent 3 digest emails\n",
			"team-wiki--web":
				"$ bun run start\nwiki serving 214 pages on :30001\nGET /handbook/oncall 200 7ms\n",
			"status-page--web":
				"$ ./status-page\nlistening on :30000\nshutting down (SIGTERM)\n",
		};
		for (const [k, v] of Object.entries(logs)) this.deps.serviceLogs.set(k, v);
		seedOrbFiles(workbench.orbId, {
			"apps/bug-tracker/current/README.md":
				"# bug-tracker\n\n团队内部的缺陷跟踪。`bun run start` 起 web，`bun run worker` 发每日摘要。\n",
			"apps/bug-tracker/current/package.json":
				'{\n  "name": "bug-tracker",\n  "scripts": { "start": "bun src/server.ts", "worker": "bun src/worker.ts" }\n}\n',
			"apps/bug-tracker/current/.oryn/box.yaml":
				"services:\n  web:\n    command: bun run start\n  worker:\n    command: bun run worker\n",
			"apps/bug-tracker/current/src/server.ts":
				'Bun.serve({ port: Number(process.env.PORT), fetch: () => new Response("ok") });\n',
			"apps/bug-tracker/data/tracker.db": "SQLite format 3\n",
			"apps/team-wiki/current/apps/wiki/README.md":
				"# team-wiki\n\n团队手册与值班说明。\n",
			"apps/team-wiki/current/apps/wiki/.oryn/box.yaml":
				"services:\n  web:\n    command: bun run start\n",
		});
		seedOrbFiles(lab.orbId, {
			"apps/status-page/current/dist/status-page": "\u007fELF",
			"apps/status-page/current/dist/index.html":
				"<h1>All systems normal</h1>\n",
		});
	}
}
