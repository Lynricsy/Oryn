/**
 * Boxes 介绍页（M23 步骤 7.3，还没有 box 时）：左栏眉题、两行标题、说明、三条要点、「新建 Box」与「阅读文档」；
 * `expanded` 宽度下右栏是 `BoxesHeroPreview`——真实的 app 行与活动行组件喂一份写死的演示数据，整体不可交互。
 * 新建对话框在 `expanded` 宽度下也放一份裁剪过的演示（`clip`）。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { BoxActivity, BoxApp } from "../../api/box-queries";
import { Icon, type IconName } from "../ui/icons";
import { Button } from "../ui/primitives";
import { AppRow } from "./AppRow";
import { BoxActivityList } from "./BoxActivityList";

export const BOXES_DOCS_URL =
	"https://github.com/Lynricsy/Oryn/blob/main/docs/features.md#boxes";

const POINTS = [
	["terminal", "boxes.hero.point1"],
	["lock", "boxes.hero.point2"],
	["globe", "boxes.hero.point3"],
] as const satisfies readonly (readonly [IconName, string])[];

/** 三条要点（介绍页与新建对话框共用）。 */
export function BoxesPoints(): ReactNode {
	const { t } = useTranslation();
	return (
		<ul className="flex flex-col gap-2.5">
			{POINTS.map(([icon, key]) => (
				<li key={key} className="flex items-start gap-2.5 text-muted text-sm">
					<span className="grid h-6 w-6 shrink-0 place-items-center rounded-xs bg-inset">
						<Icon name={icon} size={13} className="text-fg" />
					</span>
					<span className="pt-0.5">{t(key)}</span>
				</li>
			))}
		</ul>
	);
}

export function BoxesHero({
	onCreate,
	wide,
}: {
	onCreate: () => void;
	/** `expanded` 宽度：右栏放演示 */
	wide: boolean;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<div
			data-testid="boxes-hero"
			className={`mx-auto grid w-full max-w-5xl items-center gap-10 py-6 ${wide ? "grid-cols-[minmax(0,5fr)_minmax(0,6fr)]" : ""}`}
		>
			<div className="flex min-w-0 flex-col gap-5">
				<span className="font-mono text-2xs text-faint uppercase tracking-widest">
					{t("boxes.hero.eyebrow")}
				</span>
				<h2 className="whitespace-pre-line text-balance font-semibold text-2xl text-fg tracking-tight">
					{t("boxes.hero.title")}
				</h2>
				<p className="max-w-md text-muted text-sm leading-6">
					{t("boxes.hero.body")}
				</p>
				<BoxesPoints />
				<div className="flex flex-wrap items-center gap-3 pt-1">
					<Button variant="primary" onClick={onCreate}>
						{t("boxes.new.open")}
						<Icon name="forward" size={14} />
					</Button>
					<a
						href={BOXES_DOCS_URL}
						target="_blank"
						rel="noreferrer"
						className="text-accent-ink text-sm hover:underline"
					>
						{t("boxes.hero.docs")}
					</a>
				</div>
			</div>
			{wide && <BoxesHeroPreview />}
		</div>
	);
}

const DEMO_NOW = Date.now();
const DEMO_APPS: BoxApp[] = [
	["bug-tracker", true, 2],
	["team-wiki", false, 26],
	["tiny-crm", true, 74],
].map(([name, isPublic, hours]) => ({
	name: String(name),
	url: `https://${name}.example.com`,
	public: Boolean(isPublic),
	repo: `github.com/acme/${name}`,
	root: "",
	source: true,
	port: 30000,
	live: {
		id: `bxr_demo_${name}`,
		commit: "4f349ad0c1e2",
		branch: "main",
		repo: `github.com/acme/${name}`,
		root: "",
		source: true,
		sizeBytes: 1,
		state: "live",
		createdAt: DEMO_NOW - Number(hours) * 3_600_000,
		activatedAt: DEMO_NOW - Number(hours) * 3_600_000,
	},
	deploying: false,
	preview: false,
	services: [],
	createdAt: DEMO_NOW,
	updatedAt: DEMO_NOW,
}));
const DEMO_ACTIVITY: BoxActivity[] = [
	{
		id: "demo-1",
		boxId: "demo",
		boxName: "workbench",
		ts: DEMO_NOW - 2 * 3_600_000,
		kind: "deployed",
		actor: "agent",
		app: "bug-tracker",
		commit: "4f349ad",
	},
	{
		id: "demo-2",
		boxId: "demo",
		boxName: "workbench",
		ts: DEMO_NOW - 26 * 3_600_000,
		kind: "visibility",
		actor: "owner",
		app: "team-wiki",
		public: false,
	},
	{
		id: "demo-3",
		boxId: "demo",
		boxName: "workbench",
		ts: DEMO_NOW - 74 * 3_600_000,
		kind: "created",
		actor: "owner",
	},
];

/** 仿窗口的演示卡：真实组件、写死的数据，`inert` 且对读屏隐藏。`clip` 时只露出 app 行。 */
export function BoxesHeroPreview({
	clip = false,
}: {
	clip?: boolean;
}): ReactNode {
	return (
		<div
			aria-hidden
			inert
			className="pointer-events-none min-w-0 select-none overflow-hidden rounded-lg bg-raised shadow-md ring-1 ring-[var(--glass-line)]"
		>
			<div className="flex h-8 items-center gap-1.5 border-border border-b px-3">
				<span className="h-2.5 w-2.5 rounded-full bg-fg/15" />
				<span className="h-2.5 w-2.5 rounded-full bg-fg/15" />
				<span className="h-2.5 w-2.5 rounded-full bg-fg/15" />
				<span className="ml-2 font-mono text-2xs text-faint">workbench</span>
			</div>
			<div className="flex flex-col gap-1 p-2">
				{DEMO_APPS.map((a) => (
					<AppRow key={a.name} app={a} />
				))}
			</div>
			{!clip && (
				<div className="border-border border-t p-2">
					<BoxActivityList items={DEMO_ACTIVITY} />
				</div>
			)}
		</div>
	);
}
