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
import { chatModels, useModelCatalog } from "../../api/model-catalog-queries";
import { roleDefaultGlyph } from "../../lib/avatar-defaults";
import { modelBrand, shortModelName } from "../../lib/model-brand";
import {
	BUILTIN_ROLE_NAMES,
	type BuiltinRole,
	DEFAULT_CYCLE_ORDER,
	type ModelRef,
	type RoleEntry,
	resolveRoleValue,
	roleHue,
} from "../../lib/model-roles";
import { useBreakpoint } from "../../lib/viewport";
import { useModelCaps } from "../../state/model-caps";
import { useTheme } from "../../state/theme";
import { IdentityAvatar } from "../ui/IdentityAvatar";
import { Icon } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import {
	ModelCatalogFooter,
	newThreadThinkingOptions,
} from "../ui/ModelSelect";
import { Option, Popover, PopoverLabel } from "../ui/PickerPopover";
import { Dialog } from "../ui/primitives";
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
}

/** `provider/id` 选择器。 */
export const modelSelector = (m: ModelRef): string => `${m.provider}/${m.id}`;

/** 模型的短名（logo 已说明品牌）；全名放悬停提示。 */
export function modelShortName(m: ModelRef): string {
	return shortModelName(m.name || m.id, modelBrand(m.id, m.provider));
}

/**
 * cycleOrder 里的 role（按其顺序）与各自解析出的模型。role 身份列表读失败或为空时用 omp 默认的 cycleOrder。
 */
export function useRoleOptions(): RoleOption[] {
	const identities = useRoleIdentities();
	const catalog = useModelCatalog();
	const agent = useIdentity("agent").data;
	const accent = useTheme((s) => s.accent);
	return useMemo(() => {
		const listed = (identities.data?.roles ?? []).filter((r) => r.inCycle);
		const roles: Schemas["RoleIdentity"][] =
			listed.length > 0
				? listed
				: DEFAULT_CYCLE_ORDER.map((role) => ({ role, inCycle: true }));
		const builtinName = (role: string) =>
			Object.hasOwn(BUILTIN_ROLE_NAMES, role)
				? BUILTIN_ROLE_NAMES[role as BuiltinRole]
				: role;
		const entries = new Map<string, RoleEntry>(
			roles.map((r) => [
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
		return roles.map((r, i) => {
			const glyph = roleDefaultGlyph(r.role);
			const custom = !!(r.avatarIcon || r.avatarImage);
			const own = custom || !!glyph;
			return {
				role: r.role,
				name: roleLabel({ id: r.role, name: r.name || builtinName(r.role) }),
				hue: roleHue(r.role, i, accent),
				selector: r.selector,
				model: r.selector
					? resolveRoleValue(r.selector, entries, pool, r.role).model
					: undefined,
				identity: own
					? { avatarIcon: r.avatarIcon, avatarImage: r.avatarImage }
					: agentAvatar,
				of: own ? "role" : "agent",
				glyph,
			};
		});
	}, [identities.data, catalog.data, agent, accent]);
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
	const levels = newThreadThinkingOptions(caps);
	const thinking = value.thinking ?? "";
	const levelLabel = (l: string) =>
		l === ""
			? t("newThread.defaultThinking")
			: t(`model.levels.${l as "auto"}`);

	const role =
		value.kind === "role"
			? roles.find((r) => r.role === value.role)
			: undefined;
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
	} else {
		face = role ? (
			<RoleFace option={role} size={compactPill ? 20 : 16} />
		) : (
			<Icon name="sparkle" size={13} />
		);
		main = role?.name ?? value.role;
	}
	const badge = value.thinking ? levelLabel(value.thinking) : undefined;
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
			{roles.map((r) => (
				<Option
					key={r.role}
					selected={value.kind === "role" && value.role === r.role}
					leading={<RoleFace option={r} size={18} />}
					hint={
						// 解析出的模型短名；解析不出时给原始选择器或「未配置」（default 未配置 = omp 的默认模型）
						r.model
							? modelShortName(r.model)
							: (r.selector ??
								(r.role === "default"
									? t("newThread.defaultModel")
									: t("roles.unconfigured")))
					}
					trailing={
						r.model ? (
							<ModelLogo
								model={r.model.id}
								provider={r.model.provider}
								size={12}
								className="text-faint"
							/>
						) : undefined
					}
					onClick={() => {
						if (value.kind !== "role" || value.role !== r.role)
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
								key={l || "default"}
								type="button"
								role="radio"
								aria-checked={thinking === l}
								title={
									l === "" && caps === undefined
										? t("newThread.thinkingUnknown")
										: undefined
								}
								className={`h-7 rounded-full border px-2.5 text-xs transition-colors duration-150 ${thinking === l ? "border-border-strong bg-hover text-fg" : "border-border text-muted hover:bg-hover hover:text-fg"}`}
								onClick={() => onChange({ ...value, thinking: l || undefined })}
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
					badge,
				}}
				shortcut="Ctrl+S"
				active={open}
				onClick={() => (open ? close() : setOpen(true))}
				shape={compactPill ? "circle" : "pill"}
			/>
			{open &&
				(compact ? (
					<Dialog open title={t("roleCapsule.title")} onClose={close}>
						<div data-testid="role-capsule-drawer">{body}</div>
					</Dialog>
				) : (
					<Popover onClose={close}>
						<div data-testid="role-capsule-popover">{body}</div>
					</Popover>
				))}
		</span>
	);
}
