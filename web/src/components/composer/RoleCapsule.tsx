/**
 * 首屏的 role 胶囊（M13 R13 / R17）：胶囊显示当前对象的头像和名字；点开（或 Ctrl+S）后桌面是锚在胶囊上的弹层，
 * 窄屏（compact）是底部抽屉。内容依次为：全局助手（`allowAssistant` 时）、`cycleOrder` 里的各 role（头像、名字、
 * 解析出的模型）、「指定模型…」（中央模型目录与自由输入）、思考强度。
 * 新线程还没有会话，role 列表不向 agent 要：名字、头像、选择器读 `settings/role-identities`，模型按中央目录
 * 照 omp 的规则解析（`lib/model-roles.ts`）；列表读不到时按 omp 默认的 cycleOrder 列出内置 role。
 */
import { type ReactElement, type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Schemas } from "../../api/client";
import {
	useAssistantIdentity,
	useIdentity,
	useRoleIdentities,
} from "../../api/identity-queries";
import {
	chatModels,
	type ModelCatalog,
	useModelCatalog,
} from "../../api/model-catalog-queries";
import { roleDefaultGlyph } from "../../lib/avatar-defaults";
import { modelBrand, shortModelName } from "../../lib/model-brand";
import {
	BUILTIN_ROLE_NAMES,
	type BuiltinRole,
	clampThinking,
	DEFAULT_CYCLE_ORDER,
	type ModelRef,
	type RoleEntry,
	resolveRoleValue,
	roleHue,
	sessionStartThinking,
	switchedThinking,
} from "../../lib/model-roles";
import { useBreakpoint } from "../../lib/viewport";
import { modelCapsOf, useModelCaps } from "../../state/model-caps";
import { useTheme } from "../../state/theme";
import { IdentityAvatar } from "../ui/IdentityAvatar";
import { Icon } from "../ui/icons";
import { ModelCatalogFooter } from "../ui/ModelCatalogFooter";
import { ModelLogo } from "../ui/ModelLogo";
import { Option, Popover, PopoverLabel } from "../ui/PickerPopover";
import { Dialog } from "../ui/primitives";
import { CornerLevel, LevelGlyph } from "../ui/ThinkingDial";
import { ModelPickerBody } from "./ModelPickerBody";
import { PillButton } from "./PillButton";
import { roleLabel } from "./RoleTrack";

/** 新对话的对象：全局助手（可指定模型）、某个 role、或指定的模型（普通线程）。 */
export type Target =
	| { kind: "assistant"; model?: ModelRef; thinking?: string }
	| { kind: "role"; role: string; thinking?: string }
	| { kind: "model"; model: ModelRef; thinking?: string };

/** 胶囊里的一个 role：名字、头像、色相（cycleOrder 位置取色板，与 Ctrl+P 轨道同色）与解析出的模型。 */
export interface RoleOption {
	role: string;
	name: string;
	/** CSS 色相值（写进 `--role-h`） */
	hue: string;
	/** `config.yml` 里的原始选择器；未配置时省略 */
	selector?: string;
	/** 按中央目录解析出的模型；解析不出（目录未读到、选择器认不出、未配置）时省略 */
	model?: ModelRef;
	identity: Schemas["AvatarIdentity"] | undefined;
	/** 头像的身份类型：自定义 role 没设头像时回落为默认 Agent 身份 */
	of: "role" | "agent";
	glyph?: string;
	/** 是否在 cycleOrder 里（`default` 不在时由 useRoleOptions 补在末尾） */
	inCycle: boolean;
	/** 选择器是 `@role` / `pi/role`（可带 `:level`）这样单纯指向另一个已知 role 时，那个 role */
	alias?: RoleOption;
	/** 选择器（经别名展开后）显式带的思考级别（`…:high`）；没带时省略 */
	explicitThinking?: string;
	/**
	 * 以这个 role 开新会话实际生效的思考级别（见 useRoleOptions）；模型不能调节思考、或缺数据推不出来时省略
	 * （推不出来就不显示、不选中，不拿猜测冒充实际值）
	 */
	thinking?: string;
}

/** `provider/id` 选择器。 */
export const modelSelector = (m: ModelRef): string => `${m.provider}/${m.id}`;

/** 模型的短名（logo 已说明品牌）；全名放悬停提示。 */
export function modelShortName(m: ModelRef): string {
	return shortModelName(m.name || m.id, modelBrand(m.id, m.provider));
}

/** 单纯的 role 别名（`@Cyrene`、`pi/slow:high`）指向的 role；回退链、模型选择器返回 undefined。 */
function aliasTarget(selector: string | undefined): string | undefined {
	return /^(?:@|pi\/)([^\s,:]+)(?::[\w-]+)?$/.exec(selector?.trim() ?? "")?.[1];
}

/**
 * cycleOrder 里的 role（按其顺序）与各自解析出的模型；`default` 不在 cycleOrder 里时补在末尾（首屏的缺省对象就是它，
 * 胶囊、托盘色相与思考档位要按它解析；它只是 cycle 里某个 role 的别名时弹层不单列，见 RoleCapsule）。
 * 别名按全部已知 role 解析（`default: '@Cyrene'` 这类配置）。
 * 思考级别照 Core 的取法：role 选择器显式带的级别；否则切到该 role 的模型时的级别（`switchedThinking`：沿用新会话的起始级别
 * 并按模型能力收窄，会话没有级别时才用模型自带的默认档位）。
 * role 身份列表读失败或为空时用 omp 默认的 cycleOrder。
 */
export function useRoleOptions(): RoleOption[] {
	const identities = useRoleIdentities();
	const catalog = useModelCatalog();
	const agent = useIdentity("agent").data;
	const accent = useTheme((s) => s.accent);
	return useMemo(() => {
		const known = identities.data?.roles ?? [];
		const listed = known.filter((r) => r.inCycle);
		const cycle: Schemas["RoleIdentity"][] =
			listed.length > 0
				? listed
				: DEFAULT_CYCLE_ORDER.map((role) => ({ role, inCycle: true }));
		const roles = cycle.some((r) => r.role === "default")
			? cycle
			: [
					...cycle,
					known.find((r) => r.role === "default") ?? {
						role: "default",
						inCycle: false,
					},
				];
		const builtinName = (role: string) =>
			Object.hasOwn(BUILTIN_ROLE_NAMES, role)
				? BUILTIN_ROLE_NAMES[role as BuiltinRole]
				: role;
		const entries = new Map<string, RoleEntry>(
			[...roles, ...known].map((r) => [
				r.role,
				{ id: r.role, name: r.name || builtinName(r.role), model: r.selector },
			]),
		);
		const pool: ModelRef[] = chatModels(catalog.data).map((m) => ({
			provider: m.provider,
			id: m.id,
			name: m.name,
		}));
		const agentAvatar = {
			avatarIcon: agent?.avatarIcon,
			avatarImage: agent?.avatarImage,
		};
		const toOption = (r: Schemas["RoleIdentity"], hue: string): RoleOption => {
			const glyph = roleDefaultGlyph(r.role);
			const custom = !!(r.avatarIcon || r.avatarImage);
			const own = custom || !!glyph;
			const resolved = r.selector
				? resolveRoleValue(r.selector, entries, pool, r.role)
				: {};
			return {
				role: r.role,
				name: roleLabel({ id: r.role, name: r.name || builtinName(r.role) }),
				hue,
				selector: r.selector,
				model: resolved.model,
				explicitThinking: resolved.thinkingLevel,
				identity: own
					? { avatarIcon: r.avatarIcon, avatarImage: r.avatarImage }
					: agentAvatar,
				of: own ? "role" : "agent",
				glyph,
				inCycle: r.inCycle,
			};
		};
		const options = roles.map((r, i) =>
			toOption(r, roleHue(r.role, i, accent)),
		);
		const configured = identities.data?.defaultThinkingLevel;
		const start = sessionStart(options, configured, catalog.data);
		const withThinking = (o: RoleOption): RoleOption => {
			const caps = o.model
				? modelCapsOf(catalog.data, modelSelector(o.model))
				: undefined;
			const level =
				o.explicitThinking ??
				// 模型没解析出来时能力与默认档位都未知
				switchedThinking(
					start,
					o.model ? caps?.defaultLevel : undefined,
					o.model ? caps?.thinking : undefined,
				);
			return { ...o, thinking: clampThinking(level, caps?.thinking) };
		};
		return options.map(withThinking).map((o) => {
			const to = aliasTarget(o.selector);
			if (!to || to === o.role || !entries.has(to)) return o;
			const listedTo = options.find((x) => x.role === to);
			const target = known.find((r) => r.role === to);
			const alias =
				listedTo ??
				(target ? toOption(target, roleHue(to, undefined, accent)) : undefined);
			// 别名 role 与它指向的 role 同色（托盘光晕不随「换了个名字」变色）
			return alias ? { ...o, alias: withThinking(alias), hue: alias.hue } : o;
		});
	}, [identities.data, catalog.data, agent, accent]);
}

/**
 * 新会话的起始思考级别（会话以 default role 的模型开场，见 `sessionStartThinking`）；推不出来时 undefined，
 * default 模型不能调节思考（会话没有级别）时 null。
 */
export function sessionStart(
	roles: readonly RoleOption[],
	configured: string | undefined,
	catalog: ModelCatalog | undefined,
): string | null | undefined {
	const def = roles.find((r) => r.role === "default");
	const caps = def?.model
		? modelCapsOf(catalog, modelSelector(def.model))
		: undefined;
	return sessionStartThinking(
		def?.explicitThinking,
		configured,
		caps?.defaultLevel,
		caps?.thinking,
	);
}

/** 对象实际用的模型（思考档位据此取）：role 取解析出的模型，指定模型与助手取所选模型。 */
export function targetModel(
	target: Target,
	roles: readonly RoleOption[],
): ModelRef | undefined {
	if (target.kind === "role")
		return roles.find((r) => r.role === target.role)?.model;
	return target.model;
}

function RoleFace({
	option,
	size,
}: {
	option: RoleOption;
	size: number;
}): ReactNode {
	return (
		<IdentityAvatar
			identity={option.identity}
			of={option.of}
			glyph={option.glyph}
			size={size}
			shape="circle"
		/>
	);
}

function AssistantFace({ size }: { size: number }): ReactNode {
	const { avatar } = useAssistantIdentity();
	return (
		<IdentityAvatar
			identity={avatar}
			of="assistant"
			size={size}
			shape="circle"
		/>
	);
}

export function RoleCapsule({
	value,
	onChange,
	allowAssistant,
	open: openProp,
	onOpenChange,
	compactPill = false,
	shortcut,
	side,
}: {
	value: Target;
	onChange: (t: Target) => void;
	/** 列出全局助手（只在首屏；线程里没有这一项，R17） */
	allowAssistant: boolean;
	/** 受控的展开状态（首屏 Ctrl+S）；不给时自己管 */
	open?: boolean;
	onOpenChange?: (open: boolean) => void;
	/** 窄屏：胶囊只留头像圆圈 */
	compactPill?: boolean;
	/** 悬停提示里的快捷键（首屏 Ctrl+S）；没有快捷键的地方（定时对话框）不给 */
	shortcut?: string;
	/** 桌面弹层向上（首屏托盘下方没有空间，缺省）或向下（对话框里）展开 */
	side?: "top" | "bottom";
}): ReactNode {
	const { t } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const [ownOpen, setOwnOpen] = useState(false);
	const open = openProp ?? ownOpen;
	const setOpen = (next: boolean) => {
		if (openProp === undefined) setOwnOpen(next);
		onOpenChange?.(next);
	};
	const [view, setView] = useState<"main" | "model">("main");
	const close = () => {
		setOpen(false);
		setView("main");
	};
	const roles = useRoleOptions();
	const assistant = useAssistantIdentity(allowAssistant);
	const catalog = useModelCatalog();
	const assistantName = assistant.name || t("tabBar.assistant");
	const current = targetModel(value, roles);
	const caps = useModelCaps(current ? modelSelector(current) : "");
	const identities = useRoleIdentities();
	// 不改思考级别时实际生效的级别（推不出来为 undefined：不选中、不画角标）：role 取它自己的（见 useRoleOptions）；
	// 指定模型：与 default role 同一个模型时 Core 不切模型，就是起始级别，否则按切模型的规则；
	// 全局助手跑在协调宿主自己的配置上，这里没有它的数据
	const defaultModel = roles.find((r) => r.role === "default")?.model;
	const start = sessionStart(
		roles,
		identities.data?.defaultThinkingLevel,
		catalog.data,
	);
	const defaultLevel =
		value.kind === "role"
			? roles.find((r) => r.role === value.role)?.thinking
			: value.kind === "model"
				? clampThinking(
						defaultModel &&
							modelSelector(defaultModel) === modelSelector(value.model)
							? (start ?? undefined)
							: switchedThinking(start, caps?.defaultLevel, caps?.thinking),
						caps?.thinking,
					)
				: undefined;
	const shownLevel = value.thinking ?? defaultLevel;
	// 可选级别：模型能力已知时是 auto 加目录里的级别（实际生效的级别不在其中时补在最前，如 off）；
	// 能力未知时只列实际生效的那一档；模型不能调节思考时没有这一节
	const levels: readonly string[] | null = caps
		? caps.thinking.length === 0
			? null
			: [
					...(shownLevel && !["auto", ...caps.thinking].includes(shownLevel)
						? [shownLevel]
						: []),
					"auto",
					...caps.thinking,
				]
		: shownLevel
			? [shownLevel]
			: null;
	const levelLabel = (l: string) => t(`model.levels.${l as "auto"}`);

	const role =
		value.kind === "role"
			? roles.find((r) => r.role === value.role)
			: undefined;
	// default 不在 cycleOrder、只是 cycle 里某个 role 的别名（`default: '@Cyrene'`）：弹层不另列「默认」，
	// 直接选中它指向的那个 role（请求仍按 default 发，模型相同）
	const defaultOpt = roles.find((r) => r.role === "default");
	const defaultTo = defaultOpt?.alias?.role;
	const defaultAs =
		defaultOpt && !defaultOpt.inCycle && roles.some((r) => r.role === defaultTo)
			? defaultTo
			: undefined;
	const pickedRole =
		value.kind !== "role"
			? undefined
			: value.role === "default" && defaultAs
				? defaultAs
				: value.role;
	let face: ReactElement;
	let main: string;
	if (value.kind === "assistant") {
		face = <AssistantFace size={compactPill ? 20 : 16} />;
		main = value.model
			? `${assistantName} · ${modelShortName(value.model)}`
			: assistantName;
	} else if (value.kind === "model") {
		face = (
			<ModelLogo
				model={value.model.id}
				provider={value.model.provider}
				size={compactPill ? 16 : 13}
			/>
		);
		main = modelShortName(value.model);
	} else if (value.role === "default" && role?.alias) {
		// 「默认」说明不了是谁：default 只是另一个 role 的别名（如 `@Cyrene`）时直接显示那个 role
		face = <RoleFace option={role.alias} size={compactPill ? 20 : 16} />;
		main = role.alias.name;
	} else if (value.role === "default" && role?.model) {
		// 「默认」这个名字说明不了用哪个模型：胶囊直接显示 default role 解析出的模型
		face = (
			<ModelLogo
				model={role.model.id}
				provider={role.model.provider}
				size={compactPill ? 16 : 13}
			/>
		);
		main = modelShortName(role.model);
	} else {
		face = role ? (
			<RoleFace option={role} size={compactPill ? 20 : 16} />
		) : (
			<Icon name="sparkle" size={13} />
		);
		main = role?.name ?? value.role;
	}
	const pickedModel =
		value.kind === "model" || value.kind === "assistant"
			? value.model
			: undefined;

	const pickModel = (m: ModelRef | undefined) => {
		if (value.kind === "assistant") onChange({ kind: "assistant", model: m });
		else if (m) onChange({ kind: "model", model: m });
		close();
	};

	const mainView = (
		<>
			{allowAssistant && (
				<Option
					selected={value.kind === "assistant"}
					leading={<AssistantFace size={18} />}
					hint={t("roleCapsule.assistantHint")}
					onClick={() => {
						if (value.kind !== "assistant") onChange({ kind: "assistant" });
						close();
					}}
				>
					{assistantName}
				</Option>
			)}
			<PopoverLabel>{t("model.roles")}</PopoverLabel>
			{roles
				.filter((r) => !(defaultAs && r.role === "default"))
				.map((r) => (
					<Option
						key={r.role}
						selected={pickedRole === r.role}
						leading={<RoleFace option={r} size={18} />}
						hint={
							// 解析出的模型短名（别名前面加上它指向的 role）；解析不出时给原始选择器或「未配置」
							// （default 未配置 = omp 的默认模型）
							r.model
								? r.alias
									? `${r.alias.name} · ${modelShortName(r.model)}`
									: modelShortName(r.model)
								: (r.selector ??
									(r.role === "default"
										? t("newThread.defaultModel")
										: t("roles.unconfigured")))
						}
						trailing={
							// 思考级别与模型 logo 各占固定的一格：某一行缺其中一样时，其它行的两列照样对齐
							<>
								<span className="inline-flex w-3 justify-center">
									{r.thinking && <LevelGlyph level={r.thinking} size={12} />}
								</span>
								<span className="inline-flex w-3 justify-center">
									{r.model && (
										<ModelLogo
											model={r.model.id}
											provider={r.model.provider}
											size={12}
											className="text-faint"
										/>
									)}
								</span>
							</>
						}
						onClick={() => {
							if (pickedRole !== r.role)
								onChange({ kind: "role", role: r.role });
							close();
						}}
					>
						{r.name}
					</Option>
				))}
			<Option
				selected={value.kind === "model"}
				leading={<Icon name="cpu" size={14} />}
				hint={pickedModel ? modelShortName(pickedModel) : undefined}
				onClick={() => setView("model")}
			>
				{t("roleCapsule.pickModel")}
			</Option>
			{levels && (
				<>
					<PopoverLabel>{t("model.thinking")}</PopoverLabel>
					<div
						role="radiogroup"
						aria-label={t("model.thinking")}
						className="flex flex-wrap gap-1 px-1 pb-1"
					>
						{levels.map((l) => (
							// biome-ignore lint/a11y/useSemanticElements: 胶囊形的分段选择，原生 radio 画不出来；读屏语义由 role/aria-checked 提供
							<button
								key={l}
								type="button"
								role="radio"
								aria-checked={shownLevel === l}
								title={
									caps === undefined
										? t("newThread.thinkingUnknown")
										: undefined
								}
								className={`h-7 rounded-full border px-2.5 text-xs transition-colors duration-150 ${shownLevel === l ? "border-border-strong bg-hover text-fg" : "border-border text-muted hover:bg-hover hover:text-fg"}`}
								// 选回实际生效的那一档即不再单独指定（与没动过时一样跟着 role / 配置走）
								onClick={() =>
									onChange({
										...value,
										thinking: l === defaultLevel ? undefined : l,
									})
								}
							>
								{levelLabel(l)}
							</button>
						))}
					</div>
				</>
			)}
		</>
	);

	const modelView = (
		<>
			<button
				type="button"
				className="mb-1 flex items-center gap-1 rounded-sm px-2 py-1 text-faint text-xs hover:bg-hover hover:text-fg"
				onClick={() => setView("main")}
			>
				<Icon name="back" size={12} aria-hidden />
				{t("roleCapsule.back")}
			</button>
			<ModelPickerBody
				free
				current={pickedModel}
				onPick={pickModel}
				roles={
					value.kind === "assistant" && (
						<div className="mb-2">
							<Option
								selected={!value.model}
								leading={<Icon name="sparkle" size={14} />}
								onClick={() => pickModel(undefined)}
							>
								{t("newThread.defaultModel")}
							</Option>
						</div>
					)
				}
			/>
			<div className="px-1 pt-1">
				<ModelCatalogFooter catalog={catalog.data} />
			</div>
		</>
	);
	const body = view === "main" ? mainView : modelView;

	return (
		<span className="relative inline-flex min-w-0 shrink">
			<PillButton
				icon={face}
				text={{
					label: t("roleCapsule.label", { name: main }),
					main,
				}}
				shortcut={shortcut}
				active={open}
				onClick={() => (open ? close() : setOpen(true))}
				shape={compactPill ? "circle" : "pill"}
			/>
			{/* 思考级别不写文字：实际生效的级别画成右下角的角标 */}
			{levels && shownLevel && <CornerLevel level={shownLevel} />}
			{open &&
				(compact ? (
					<Dialog open title={t("roleCapsule.title")} onClose={close}>
						<div data-testid="role-capsule-drawer">{body}</div>
					</Dialog>
				) : (
					<Popover side={side} onClose={close}>
						<div data-testid="role-capsule-popover">{body}</div>
					</Popover>
				))}
		</span>
	);
}
