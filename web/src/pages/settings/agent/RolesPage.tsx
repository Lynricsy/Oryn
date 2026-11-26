/**
 * 设置 → 模型与角色（M13 R27）：
 * - 默认 Agent 身份（`AgentIdentityCard`）；
 * - 新会话：默认思考档位（config.yml 的 `defaultThinkingLevel`，新会话没有显式级别时从它起步）；
 * - 轮换顺序 / 其他角色：config.yml 的 `cycleOrder`（拖动或上下移调整）、`modelRoles.<role>`（模型，补全来自中央模型目录）、
 *   `modelTags.<role>.name`（显示名），增删自定义 role，以及每个 role 的头像——一行一个 role，「设置」打开对话框编辑；
 * - 遗留头像：只剩头像、不再是已知 role 的遗留项，可清除头像。
 * 已知 role 同控制面 `agentcfg.ParseKnownRoles`：cycleOrder、modelRoles 的键与内置 role 的并集。
 * config.yml 的每次改动都在最新内容上执行（`useOmpConfig().mutate`），不会把别处的改动盖掉，注释原样保留。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Document } from "yaml";
import { useOmpConfig } from "../../../api/agent-config-queries";
import type { Schemas } from "../../../api/client";
import {
	saveRoleAvatar,
	useRoleIdentities,
} from "../../../api/identity-queries";
import { AvatarEditor } from "../../../components/settings/AvatarEditor";
import {
	AgentErrorText,
	AgentPageHead,
	AvatarRow,
	GroupNote,
} from "../../../components/settings/agent-config-ui";
import { ModelPatternInput } from "../../../components/settings/ModelPatternInput";
import { useAsync } from "../../../components/settings/useAsync";
import { AvatarTile } from "../../../components/ui/AvatarTile";
import { Badge, Callout, firstGrapheme } from "../../../components/ui/data";
import { IdentityAvatar } from "../../../components/ui/IdentityAvatar";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Select,
	Spinner,
	TextInput,
} from "../../../components/ui/primitives";
import { Group, GroupList, GroupRow } from "../../../components/ui/workbench";
import i18n from "../../../i18n";
import { roleDefaultGlyph } from "../../../lib/avatar-defaults";
import { confirmAction } from "../../../lib/confirm";
import { BUILTIN_ROLE_NAMES, parseCycleOrder } from "../../../lib/model-roles";
import {
	defaultThinkingLevel,
	setCycleOrder,
	setDefaultThinkingLevel,
	setModelRole,
	setModelTagName,
	THINKING_LEVELS,
	type ThinkingLevel,
} from "../../../lib/omp-config";
import { AgentIdentityCard } from "../IdentitySettings";

type AvatarIdentity = Schemas["AvatarIdentity"];
type RoleIdentity = Schemas["RoleIdentity"];

/** 新建 role 的 id 格式（同其他 Agent 子页的名称）。 */
const ROLE_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const BUILTIN_ROLES = Object.keys(BUILTIN_ROLE_NAMES);

export interface RolesConfig {
	/** 生效的轮换顺序（未设置时为 Core 缺省） */
	cycle: string[];
	/** `modelRoles.<role>` 原值（列表以 ", " 连接） */
	models: Record<string, string>;
	/** `modelTags.<role>.name` */
	names: Record<string, string>;
}

function stringRecord(
	v: unknown,
	pick: (x: unknown) => string | undefined,
): Record<string, string> {
	const out: Record<string, string> = {};
	if (!v || typeof v !== "object" || Array.isArray(v)) return out;
	for (const [k, x] of Object.entries(v)) {
		const s = pick(x)?.trim();
		if (s) out[k] = s;
	}
	return out;
}

/** 从 config.yml 读角色相关设置；文档缺失或不合法时按缺省配置。 */
export function rolesConfig(doc: Document | undefined): RolesConfig {
	const js: unknown = doc && doc.errors.length === 0 ? (doc.toJS() ?? {}) : {};
	const root = (js && typeof js === "object" ? js : {}) as Record<
		string,
		unknown
	>;
	return {
		cycle: [...new Set(parseCycleOrder(root.cycleOrder).map((r) => r.trim()))],
		models: stringRecord(root.modelRoles, (x) =>
			typeof x === "string"
				? x
				: Array.isArray(x)
					? x.filter((s) => typeof s === "string").join(", ")
					: undefined,
		),
		names: stringRecord(root.modelTags, (x) =>
			x && typeof x === "object" && "name" in x && typeof x.name === "string"
				? x.name
				: undefined,
		),
	};
}

/** 已知 role：cycleOrder（按其顺序）、modelRoles 其余的键（字典序）、内置 role 其余的项（按内置顺序）。 */
export function knownRoles(cfg: RolesConfig): string[] {
	const seen = new Set(cfg.cycle);
	const out = [...cfg.cycle];
	for (const r of [...Object.keys(cfg.models).sort(), ...BUILTIN_ROLES])
		if (!seen.has(r)) {
			seen.add(r);
			out.push(r);
		}
	return out;
}

/** 把 role 移到 `to`（移出后的下标）；不在列表里或位置不变时返回 null。 */
export function moveRole(
	order: readonly string[],
	role: string,
	to: number,
): string[] | null {
	const from = order.indexOf(role);
	const at = Math.max(0, Math.min(to, order.length - 1));
	if (from < 0 || from === at) return null;
	const next = order.filter((r) => r !== role);
	next.splice(at, 0, role);
	return next;
}

/** role 的显示名：改过的 `modelTags.<role>.name`，否则内置 role 的本地化名，否则 role id。 */
function roleDisplayName(role: string, tagName?: string): string {
	const builtin = Object.hasOwn(BUILTIN_ROLE_NAMES, role)
		? BUILTIN_ROLE_NAMES[role as keyof typeof BUILTIN_ROLE_NAMES]
		: undefined;
	if (tagName && tagName !== builtin) return tagName;
	const key = `roles.names.${role}`;
	return i18n.exists(key) ? i18n.t(key as "roles.names.default") : role;
}

function RoleAvatar({
	role,
	identity,
	label,
}: {
	role: string;
	identity: AvatarIdentity | undefined;
	label: string;
}): ReactNode {
	const glyph = roleDefaultGlyph(role);
	return identity?.avatarIcon || identity?.avatarImage || glyph ? (
		<IdentityAvatar identity={identity} of="role" glyph={glyph} size={28} />
	) : (
		<AvatarTile
			of="role"
			identity={undefined}
			size={28}
			tone="neutral"
			fallback={
				<span className="font-semibold text-sm">{firstGrapheme(label)}</span>
			}
		/>
	);
}

/** 失焦或回车时提交的文本框（显示名）。 */
function CommitInput({
	value,
	placeholder,
	label,
	disabled,
	onCommit,
}: {
	value: string;
	placeholder?: string;
	label: string;
	disabled?: boolean;
	onCommit(next: string): void;
}): ReactNode {
	const [draft, setDraft] = useState<string | null>(null);
	const commit = () => {
		if (draft === null) return;
		setDraft(null);
		if (draft.trim() !== value) onCommit(draft.trim());
	};
	return (
		<TextInput
			aria-label={label}
			placeholder={placeholder}
			value={draft ?? value}
			disabled={disabled}
			onChange={(e) => setDraft(e.target.value)}
			onBlur={commit}
			onKeyDown={(e) => {
				if (e.key === "Enter") {
					e.preventDefault();
					commit();
				} else if (e.key === "Escape" && draft !== null) {
					e.preventDefault();
					e.stopPropagation();
					setDraft(null);
				}
			}}
		/>
	);
}

/** 默认思考档位的显示名（`model.levels.*`）。 */
function levelLabel(level: ThinkingLevel): string {
	return i18n.t(`model.levels.${level}`);
}

export default function RolesPage(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const config = useOmpConfig();
	const identities = useRoleIdentities();
	const avatarSave = useAsync();
	const [open, setOpen] = useState<string | null>(null);
	const [dragging, setDragging] = useState<string | null>(null);
	const [creating, setCreating] = useState(false);

	const cfg = useMemo(() => rolesConfig(config.doc), [config.doc]);
	const known = useMemo(() => knownRoles(cfg), [cfg]);
	const cycle = cfg.cycle;
	const others = known.filter((r) => !cycle.includes(r));
	const byRole = new Map<string, RoleIdentity>(
		identities.data?.roles.map((r) => [r.role, r]) ?? [],
	);
	const orphans =
		identities.data?.roles.filter(
			(r) => !known.includes(r.role) && (r.avatarIcon || r.avatarImage),
		) ?? [];
	const invalid = config.doc !== undefined && config.doc.errors.length > 0;
	const ready = config.doc !== undefined && !invalid;
	const thinking = defaultThinkingLevel(ready ? config.doc : undefined);

	/** 在最新的 config.yml 上改轮换顺序；`edit` 返回 null 表示不改。 */
	const editCycle = (edit: (cur: string[]) => string[] | null) =>
		void config.mutate((doc) => {
			const next = edit(rolesConfig(doc).cycle);
			if (!next) return false;
			setCycleOrder(doc, next.length > 0 ? next : null);
			return true;
		});
	const move = (role: string, to: number) =>
		editCycle((cur) => moveRole(cur, role, to));
	const deleteRole = async (role: string, label: string) => {
		const ok = await confirmAction({
			title: t("agentPages.roles.deleteConfirm", { name: label }),
			body: t("agentPages.roles.deleteBody"),
			action: t("common.delete"),
			danger: true,
		});
		if (!ok) return;
		setOpen(null);
		void config.mutate((doc) => {
			const cur = rolesConfig(doc).cycle;
			if (cur.includes(role)) {
				const next = cur.filter((r) => r !== role);
				setCycleOrder(doc, next.length > 0 ? next : null);
			}
			setModelRole(doc, role, null);
			setModelTagName(doc, role, null);
			return true;
		});
	};
	const putAvatar = (role: string, avatar: AvatarIdentity) =>
		avatarSave.run(() => saveRoleAvatar(qc, role, avatar));

	const row = (role: string, index: number, inCycle: boolean) => {
		const tagName = cfg.names[role];
		const label = roleDisplayName(role, tagName);
		const builtin = Object.hasOwn(BUILTIN_ROLE_NAMES, role);
		const identity = byRole.get(role);
		const model = cfg.models[role] ?? "";
		const hasAvatar = Boolean(identity?.avatarIcon || identity?.avatarImage);
		const draggable = inCycle && ready;
		return (
			// 整行可拖（拖动是鼠标捷径，键盘与读屏用行尾的上移 / 下移按钮）
			<AvatarRow
				key={role}
				data-role={role}
				draggable={draggable}
				onDragStart={(e) => {
					e.dataTransfer?.setData("text/plain", role);
					if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
					setDragging(role);
				}}
				onDragEnd={() => setDragging(null)}
				onDragOver={(e) => {
					if (inCycle && dragging) e.preventDefault();
				}}
				onDrop={(e) => {
					if (!inCycle || !dragging) return;
					e.preventDefault();
					move(dragging, index);
					setDragging(null);
				}}
				className={`${draggable ? "cursor-grab" : ""} ${dragging === role ? "opacity-50" : ""}`}
				leading={<RoleAvatar role={role} identity={identity} label={label} />}
				title={
					<>
						<span className="min-w-0 truncate">{label}</span>
						{label !== role && (
							<span className="shrink-0 font-mono font-normal text-faint text-xs">
								{role}
							</span>
						)}
						{!builtin && <Badge>{t("agentPages.roles.custom")}</Badge>}
					</>
				}
				meta={
					<>
						<span
							className={`min-w-0 truncate ${model ? "font-mono" : ""}`}
							title={model || undefined}
						>
							{model || t("agentPages.roles.noModel")}
						</span>
						{!hasAvatar && !builtin && (
							<span className="shrink-0">
								· {t("agentPages.roles.noAvatar")}
							</span>
						)}
					</>
				}
				trailing={
					<>
						{inCycle ? (
							<>
								<IconButton
									icon="chevronUp"
									box="sm"
									size={14}
									label={t("agentPages.roles.moveUp", { name: label })}
									disabled={!ready || index === 0}
									onClick={() => move(role, index - 1)}
								/>
								<IconButton
									icon="chevronDown"
									box="sm"
									size={14}
									label={t("agentPages.roles.moveDown", { name: label })}
									disabled={!ready || index === cycle.length - 1}
									onClick={() => move(role, index + 1)}
								/>
								<IconButton
									icon="minusCircle"
									box="sm"
									size={14}
									label={t("agentPages.roles.removeFromCycle", {
										name: label,
									})}
									disabled={!ready || cycle.length <= 1}
									onClick={() =>
										editCycle((cur) =>
											cur.includes(role) && cur.length > 1
												? cur.filter((r) => r !== role)
												: null,
										)
									}
								/>
							</>
						) : (
							<IconButton
								icon="plus"
								box="sm"
								size={14}
								label={t("agentPages.roles.addToCycle", { name: label })}
								disabled={!ready}
								onClick={() =>
									editCycle((cur) =>
										cur.includes(role) ? null : [...cur, role],
									)
								}
							/>
						)}
						<Button
							size="sm"
							aria-label={t("agentPages.roles.configure", { name: label })}
							onClick={() => setOpen(role)}
						>
							{t("agentPages.roles.configureShort")}
						</Button>
					</>
				}
			/>
		);
	};

	const editing = open !== null && known.includes(open) ? open : null;
	return (
		<>
			<AgentPageHead reachesAssistant />
			<AgentIdentityCard />
			{invalid && (
				<Callout tone="warn" role="status">
					{t("agentPages.errors.configInvalid", {
						reason: config.doc?.errors[0]?.message ?? "",
					})}
				</Callout>
			)}
			<Group title={t("agentPages.roles.sessionTitle")}>
				<GroupList>
					<GroupRow
						icon="brain"
						title={t("agentPages.roles.thinking")}
						meta={t("agentPages.roles.thinkingHint", {
							fallback: levelLabel("high"),
						})}
						trailing={
							<Select<ThinkingLevel>
								className="w-32"
								aria-label={t("agentPages.roles.thinking")}
								value={thinking}
								disabled={!ready}
								options={THINKING_LEVELS.map((l) => ({
									value: l,
									label: levelLabel(l),
								}))}
								onChange={(level) =>
									void config.mutate((doc) => {
										if (doc.get("defaultThinkingLevel") === level) return false;
										setDefaultThinkingLevel(doc, level);
										return true;
									})
								}
							/>
						}
					/>
				</GroupList>
			</Group>
			<Group
				title={t("agentPages.roles.cycleTitle")}
				action={
					<span className="inline-flex items-center gap-2">
						{(config.pending || avatarSave.busy) && <Spinner />}
						<Button
							size="sm"
							icon="plus"
							disabled={!ready}
							onClick={() => setCreating(true)}
						>
							{t("agentPages.roles.new")}
						</Button>
					</span>
				}
			>
				{config.error ? (
					<ErrorText error={config.error} />
				) : config.doc === undefined ? (
					<Spinner />
				) : (
					<div data-testid="roles-cycle" className="contents">
						<GroupList>{cycle.map((r, i) => row(r, i, true))}</GroupList>
					</div>
				)}
				<GroupNote>{t("agentPages.roles.cycleHint")}</GroupNote>
			</Group>
			{config.doc !== undefined && others.length > 0 && (
				<Group title={t("agentPages.roles.othersTitle")}>
					<div data-testid="roles-others" className="contents">
						<GroupList>{others.map((r, i) => row(r, i, false))}</GroupList>
					</div>
					<GroupNote>{t("agentPages.roles.othersHint")}</GroupNote>
				</Group>
			)}
			{orphans.length > 0 && (
				<Group title={t("agentPages.roles.orphansTitle")}>
					<GroupList>
						{orphans.map((r) => {
							const label = roleDisplayName(r.role, r.name);
							return (
								<AvatarRow
									key={r.role}
									data-role={r.role}
									leading={
										<RoleAvatar role={r.role} identity={r} label={label} />
									}
									title={<span className="min-w-0 truncate">{label}</span>}
									trailing={
										<Button
											size="sm"
											variant="ghost"
											icon="trash"
											disabled={avatarSave.busy}
											onClick={() => void putAvatar(r.role, {})}
										>
											{t("agentPages.removeAvatar")}
										</Button>
									}
								/>
							);
						})}
					</GroupList>
					<GroupNote>{t("agentPages.roles.orphansHint")}</GroupNote>
				</Group>
			)}
			<div className="flex flex-col gap-1 empty:hidden">
				{identities.error ? <ErrorText error={identities.error} /> : null}
				{avatarSave.error ? <ErrorText error={avatarSave.error} /> : null}
				<AgentErrorText error={config.mutateError} />
			</div>
			{editing !== null && (
				<RoleDialog
					role={editing}
					tagName={cfg.names[editing]}
					model={cfg.models[editing] ?? ""}
					identity={byRole.get(editing)}
					ready={ready}
					avatarBusy={avatarSave.busy}
					onModel={(next) =>
						void config.mutate((doc) => {
							setModelRole(doc, editing, next || null);
							return true;
						})
					}
					onName={(next) =>
						void config.mutate((doc) => {
							setModelTagName(doc, editing, next || null);
							return true;
						})
					}
					onAvatar={(avatar) => putAvatar(editing, avatar)}
					onDelete={() =>
						void deleteRole(
							editing,
							roleDisplayName(editing, cfg.names[editing]),
						)
					}
					onClose={() => setOpen(null)}
				/>
			)}
			{creating && (
				<NewRoleDialog
					taken={new Set([...known, ...orphans.map((r) => r.role)])}
					onClose={() => setCreating(false)}
					onCreate={(role, name) => {
						setCreating(false);
						setOpen(role);
						void config.mutate((doc) => {
							const cur = rolesConfig(doc).cycle;
							if (!cur.includes(role)) setCycleOrder(doc, [...cur, role]);
							if (name) setModelTagName(doc, role, name);
							return true;
						});
					}}
				/>
			)}
		</>
	);
}

/** 一个 role 的设置：模型、显示名、头像（都即改即存），自定义 role 可删除。 */
function RoleDialog({
	role,
	tagName,
	model,
	identity,
	ready,
	avatarBusy,
	onModel,
	onName,
	onAvatar,
	onDelete,
	onClose,
}: {
	role: string;
	tagName: string | undefined;
	model: string;
	identity: AvatarIdentity | undefined;
	ready: boolean;
	avatarBusy: boolean;
	onModel(next: string): void;
	onName(next: string): void;
	onAvatar(avatar: AvatarIdentity): Promise<boolean>;
	onDelete(): void;
	onClose(): void;
}): ReactNode {
	const { t } = useTranslation();
	const label = roleDisplayName(role, tagName);
	const builtin = Object.hasOwn(BUILTIN_ROLE_NAMES, role);
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("agentPages.roles.configure", { name: label })}
			footer={
				<>
					{!builtin && (
						<Button
							variant="danger"
							icon="trash"
							className="mr-auto"
							disabled={!ready}
							onClick={onDelete}
						>
							{t("agentPages.roles.delete")}
						</Button>
					)}
					<Button variant="primary" onClick={onClose}>
						{t("common.close")}
					</Button>
				</>
			}
		>
			<div data-role-dialog={role} className="flex flex-col gap-4">
				<Field
					label={t("agentPages.roles.model")}
					hint={t("agentPages.roles.modelHint")}
				>
					<ModelPatternInput
						value={model}
						label={t("agentPages.roles.modelInput", { name: label })}
						placeholder={t("agentPages.models.placeholder")}
						disabled={!ready}
						onCommit={onModel}
					/>
				</Field>
				<Field
					label={t("agentPages.roles.displayName")}
					hint={t("agentPages.roles.displayNameHint")}
				>
					<CommitInput
						value={tagName ?? ""}
						placeholder={roleDisplayName(role)}
						label={t("agentPages.roles.displayNameInput", { name: role })}
						disabled={!ready}
						onCommit={onName}
					/>
				</Field>
				<div className="flex flex-col gap-[7px]">
					<span className="font-[550] text-[13px] text-fg">
						{t("agentPages.avatar")}
					</span>
					<AvatarEditor
						value={identity}
						busy={avatarBusy}
						resetLabel={t("agentPages.removeAvatar")}
						onChange={onAvatar}
					/>
				</div>
			</div>
		</Dialog>
	);
}

function NewRoleDialog({
	taken,
	onClose,
	onCreate,
}: {
	taken: ReadonlySet<string>;
	onClose(): void;
	onCreate(role: string, name: string): void;
}): ReactNode {
	const { t } = useTranslation();
	const [role, setRole] = useState("");
	const [name, setName] = useState("");
	const [showErrors, setShowErrors] = useState(false);
	const id = role.trim();
	const error = !id
		? t("agentPages.errors.nameRequired")
		: !ROLE_ID.test(id)
			? t("agentPages.errors.nameInvalid")
			: taken.has(id)
				? t("agentPages.errors.nameTaken")
				: null;
	const submit = () => {
		setShowErrors(true);
		if (!error) onCreate(id, name.trim());
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("agentPages.roles.newTitle")}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						disabled={showErrors && error !== null}
						onClick={submit}
					>
						{t("common.create")}
					</Button>
				</>
			}
		>
			<form
				className="flex flex-col gap-3"
				onSubmit={(e) => {
					e.preventDefault();
					submit();
				}}
			>
				<Field
					label={t("agentPages.roles.roleId")}
					hint={t("agentPages.roles.roleIdHint")}
				>
					<TextInput
						autoFocus
						className="font-mono"
						value={role}
						aria-invalid={showErrors && error !== null}
						onChange={(e) => setRole(e.target.value)}
					/>
				</Field>
				{showErrors && error && (
					<p className="-mt-2 text-err text-xs">{error}</p>
				)}
				<Field label={t("agentPages.roles.displayName")}>
					<TextInput value={name} onChange={(e) => setName(e.target.value)} />
				</Field>
			</form>
		</Dialog>
	);
}
