/**
 * 首屏的 role 胶囊（M13 R13 / R17）：胶囊显示当前对象的头像和名字；点开（或 Ctrl+S）后是统一模型选择器
 * （`ModelPicker`，桌面锚在胶囊上的弹层、窄屏底部抽屉）：对象区依次为全局助手（`allowAssistant` 时）与
 * `cycleOrder` 里的各 role（头像、名字、解析出的模型、思考级别图标），其下是中央模型目录（可自由输入 provider/id），
 * 思考级别在检视栏里跟着高亮项走。
 * 新线程还没有会话，role 列表不向 agent 要：名字、头像、选择器读 `settings/role-identities`，模型按中央目录
 * 照 omp 的规则解析（`lib/model-roles.ts`）；列表读不到时按 omp 默认的 cycleOrder 列出内置 role。
 */
import { type ReactElement, type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAssistantSettings } from "../../api/assistant-queries";
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
import {
	modelSelector,
	modelShortName,
	parseModelInput,
} from "../../lib/model-picker";
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
import { modelCapsOf } from "../../state/model-caps";
import { useTheme } from "../../state/theme";
import {
	entryKey,
	ModelPicker,
	modelKey,
	type PickerEntry,
	type PickTarget,
	targetKey,
} from "../model-picker/ModelPicker";
import { ModelPickerPopover } from "../model-picker/ModelPickerPopover";
import { IdentityAvatar } from "../ui/IdentityAvatar";
import { Icon } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { CornerLevel } from "../ui/ThinkingDial";
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
	const close = () => setOpen(false);
	const roles = useRoleOptions();
	const assistant = useAssistantIdentity(allowAssistant);
	const assistantSettings = useAssistantSettings(allowAssistant);
	const catalog = useModelCatalog();
	const assistantName = assistant.name || t("tabBar.assistant");
	const assistantDefaultModel: ModelRef | undefined = useMemo(() => {
		const m = assistantSettings.data?.model;
		if (!m) return undefined;
		const named = catalog.data?.models.find(
			(x) => x.provider === m.provider && x.id === m.id,
		);
		return { provider: m.provider, id: m.id, name: named?.name };
	}, [assistantSettings.data?.model, catalog.data]);
	const assistantDefaultThinking = assistantSettings.data?.thinkingLevel;
	const identities = useRoleIdentities();
	const capsOf = (m: ModelRef | undefined) =>
		m ? modelCapsOf(catalog.data, modelSelector(m)) : undefined;
	// 某个对象不改思考级别时实际生效的级别（推不出来为 undefined：不选中、不画角标）：role 取它自己的（见 useRoleOptions）；
	// 指定模型：与 default role 同一个模型时 Core 不切模型，就是起始级别，否则按切模型的规则；
	// 全局助手：若全局助手设置里配置了默认思考档位，则按所用模型能力收窄显示，否则不猜测。
	const defaultModel = roles.find((r) => r.role === "default")?.model;
	const start = sessionStart(
		roles,
		identities.data?.defaultThinkingLevel,
		catalog.data,
	);
	const defaultLevelOf = (target: Target): string | undefined => {
		if (target.kind === "role")
			return roles.find((r) => r.role === target.role)?.thinking;
		if (target.kind === "assistant")
			return assistantDefaultThinking
				? clampThinking(
						assistantDefaultThinking,
						capsOf(target.model ?? assistantDefaultModel)?.thinking,
					)
				: undefined;
		const caps = capsOf(target.model);
		return clampThinking(
			defaultModel &&
				modelSelector(defaultModel) === modelSelector(target.model)
				? (start ?? undefined)
				: switchedThinking(start, caps?.defaultLevel, caps?.thinking),
			caps?.thinking,
		);
	};
	const defaultLevel = defaultLevelOf(value);
	const shownLevel = value.thinking ?? defaultLevel;
	const caps = capsOf(
		value.kind === "assistant"
			? (value.model ?? assistantDefaultModel)
			: targetModel(value, roles),
	);

	const role =
		value.kind === "role"
			? roles.find((r) => r.role === value.role)
			: undefined;
	// default 不在 cycleOrder、只是 cycle 里某个 role 的别名（`default: '@Cyrene'`）：选择器不另列「默认」，
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

	const selected =
		value.kind === "assistant"
			? entryKey("assistant")
			: value.kind === "role"
				? entryKey(pickedRole ?? value.role)
				: modelKey(value.model);
	const entries: PickerEntry[] = [
		...(allowAssistant
			? [
					{
						key: "assistant",
						name: assistantName,
						face: <AssistantFace size={18} />,
						model: assistantDefaultModel,
						hint: t("roleCapsule.assistantHint"),
						// 全局助手与 role 同列、头像也相似：名字后加主题色闪光标记；改过名时再写出「助手」
						mark: {
							label: t("tabBar.assistant"),
							text:
								assistantName === t("tabBar.assistant")
									? undefined
									: t("modelPicker.assistantMark"),
						},
						level:
							value.kind === "assistant"
								? shownLevel
								: defaultLevelOf({ kind: "assistant" }),
					},
				]
			: []),
		...roles
			.filter((r) => !(defaultAs && r.role === "default"))
			.map(
				(r): PickerEntry => ({
					key: r.role,
					name: r.name,
					face: <RoleFace option={r} size={18} />,
					model: r.model,
					level: pickedRole === r.role ? shownLevel : r.thinking,
					// default 指向的 role 标「默认」；解析出模型的别名 role 标它指向的 role
					badge:
						r.role === defaultTo
							? t("modelPicker.default")
							: r.alias && r.model
								? r.alias.name
								: undefined,
					// 解析不出模型时给原始选择器或「未配置」（default 未配置 = omp 的默认模型）
					hint:
						r.selector ??
						(r.role === "default"
							? t("newThread.defaultModel")
							: t("roles.unconfigured")),
					data: { role: r.role },
				}),
			),
	];

	/** 选择器里的一项 → 新对话的对象；全局助手下的模型项是「助手改用此模型」。 */
	const toTarget = (p: PickTarget): Target | undefined => {
		if (p.kind === "entry") {
			const r = p.entry.data?.role;
			return r === undefined
				? { kind: "assistant" }
				: { kind: "role", role: r };
		}
		const model: ModelRef | undefined =
			p.kind === "model"
				? { provider: p.model.provider, id: p.model.id, name: p.model.name }
				: parseModelInput(p.value);
		if (!model) return undefined;
		return value.kind === "assistant"
			? { kind: "assistant", model }
			: { kind: "model", model };
	};
	const isSelected = (p: PickTarget) => targetKey(p) === selected;
	const onPick = (p: PickTarget) => {
		const next = toTarget(p);
		// 再点选中项不改对象（default 别名选中的是它指向的 role，仍按 default 发）；
		// 只有改用了别的模型的全局助手再点「全局助手」才重置为默认模型
		const keep = isSelected(p) && !(value.kind === "assistant" && value.model);
		if (next && !keep) onChange(next);
		close();
	};

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
			{/* 思考级别不写文字：实际生效的级别画成右下角的角标（已知不能调节思考的模型不画） */}
			{shownLevel && caps?.thinking.length !== 0 && (
				<CornerLevel level={shownLevel} />
			)}
			{open && (
				<ModelPickerPopover
					title={t("roleCapsule.title")}
					onClose={close}
					side={side ?? "top"}
					testId={compact ? "role-capsule-drawer" : "role-capsule-popover"}
				>
					<ModelPicker
						entries={entries}
						selected={selected}
						free="model"
						onPick={onPick}
						pickLabel={(p) =>
							p.kind !== "entry" && value.kind === "assistant"
								? t("roleCapsule.pickForAssistant")
								: t("roleCapsule.pickForNew")
						}
						thinking={{
							value: (p) => {
								if (isSelected(p)) return shownLevel;
								const next = toTarget(p);
								return next ? defaultLevelOf(next) : undefined;
							},
							preset: (p) =>
								p.kind === "entry"
									? roles.find((r) => r.role === p.entry.data?.role)?.thinking
									: undefined,
							onChange: (p, level) => {
								// 已选中项：就地改级别（选回实际生效的那一档即不再单独指定），选择器不关
								if (isSelected(p)) {
									onChange({
										...value,
										thinking: level === defaultLevel ? undefined : level,
									});
									return;
								}
								// 别的项：连同该级别选中它（等于它自己的级别时不单独指定）
								const next = toTarget(p);
								if (next)
									onChange({
										...next,
										thinking:
											level === defaultLevelOf(next) ? undefined : level,
									});
								close();
							},
						}}
					/>
				</ModelPickerPopover>
			)}
		</span>
	);
}
