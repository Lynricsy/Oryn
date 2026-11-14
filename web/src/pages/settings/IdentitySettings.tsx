/**
 * 设置 → 身份与头像（M9.6）：Agent 身份（名字 + 头像）、全局助手（名字 + 头像，留空跟随 Agent，带底栏预览）、
 * 角色头像（cycleOrder 里的每个 role，以及只剩头像的遗留项）、我的头像。
 * 身份接口是整体替换：每次保存都发完整对象——只改名字时带上当前头像，只改头像时带上当前名字。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Schemas } from "../../api/client";
import {
	saveIdentity,
	saveRoleAvatar,
	useAssistantIdentity,
	useIdentity,
	useRoleIdentities,
} from "../../api/identity-queries";
import { AvatarEditor } from "../../components/settings/AvatarEditor";
import { useAsync } from "../../components/settings/useAsync";
import { AvatarTile } from "../../components/ui/AvatarTile";
import { Callout, Card, firstGrapheme } from "../../components/ui/data";
import {
	AssistantAvatar,
	IdentityAvatar,
} from "../../components/ui/IdentityAvatar";
import {
	Button,
	ErrorText,
	Field,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import { UserAvatar } from "../../components/ui/UserAvatar";
import i18n from "../../i18n";

type AgentIdentity = Schemas["AgentIdentity"];
type AvatarIdentity = Schemas["AvatarIdentity"];
type RoleIdentity = Schemas["RoleIdentity"];

/** PUT 体：名字去空白后为空则不带；头像图片与字形至多其一。 */
function identityBody(
	name: string | undefined,
	avatar: AvatarIdentity | undefined,
): AgentIdentity {
	const out: AgentIdentity = {};
	const n = name?.trim();
	if (n) out.name = n;
	if (avatar?.avatarImage) out.avatarImage = avatar.avatarImage;
	else if (avatar?.avatarIcon) out.avatarIcon = avatar.avatarIcon;
	return out;
}

const hasAvatar = (v: AvatarIdentity | undefined) =>
	Boolean(v?.avatarIcon || v?.avatarImage);

/** role 的显示名：config.yml 的 `modelTags.<role>.name`，否则内置 role 的本地化名，否则 role id。 */
function roleDisplayName(r: Pick<RoleIdentity, "role" | "name">): string {
	if (r.name) return r.name;
	const key = `roles.names.${r.role}`;
	return i18n.exists(key) ? i18n.t(key as "roles.names.default") : r.role;
}

/** 头像种类的一句话：未设置 / 图片 / 字形。 */
function AvatarKind({
	value,
	unset,
}: {
	value: AvatarIdentity | undefined;
	unset: string;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			{value?.avatarImage
				? t("appearance.avatarKind.image")
				: value?.avatarIcon
					? t("appearance.avatarKind.icon")
					: unset}
		</>
	);
}

/** 名字输入：失焦或回车时与服务端值不同才保存（回车即失焦，只走一条保存路径）。 */
function NameField({
	value,
	placeholder,
	disabled,
	onCommit,
}: {
	value: string | undefined;
	placeholder: string;
	disabled: boolean;
	onCommit: (name: string) => void;
}): ReactNode {
	const { t } = useTranslation();
	const [draft, setDraft] = useState(value ?? "");
	useEffect(() => setDraft(value ?? ""), [value]);
	return (
		<Field label={t("identity.name")}>
			<TextInput
				className="max-w-72"
				maxLength={40}
				placeholder={placeholder}
				disabled={disabled}
				value={draft}
				onChange={(e) => setDraft(e.target.value)}
				onBlur={() => {
					if (draft.trim() !== (value ?? "")) onCommit(draft);
				}}
				onKeyDown={(e) => {
					if (e.key !== "Enter") return;
					e.preventDefault();
					e.currentTarget.blur();
				}}
			/>
		</Field>
	);
}

function AgentIdentityCard(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const q = useIdentity("agent");
	const save = useAsync();
	const cur = q.data;
	const put = (name: string | undefined, avatar: AvatarIdentity | undefined) =>
		save.run(() => saveIdentity(qc, "agent", identityBody(name, avatar)));
	return (
		<Card title={t("identity.agentTitle")} icon="robot">
			<div data-testid="identity-agent" className="flex flex-col gap-4">
				<div className="flex items-center gap-3">
					<IdentityAvatar identity={cur} of="agent" size={40} />
					<div className="min-w-0 flex-1">
						<p className="truncate text-sm">
							<AvatarKind
								value={cur}
								unset={t("appearance.avatarKind.default")}
							/>
						</p>
						<p className="text-faint text-xs">{t("identity.agentHint")}</p>
					</div>
					{(save.busy || q.isPending) && <Spinner />}
				</div>
				<NameField
					value={cur?.name}
					placeholder={t("identity.namePlaceholder")}
					disabled={q.isPending}
					onCommit={(name) => void put(name, cur)}
				/>
				<AvatarEditor
					value={cur}
					busy={save.busy || q.isPending}
					resetLabel={t("appearance.avatarReset")}
					onChange={(avatar) => put(cur?.name, avatar)}
				/>
				<ErrorText error={save.error ?? q.error} />
			</div>
		</Card>
	);
}

function AssistantIdentityCard(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const own = useIdentity("assistant");
	const agent = useIdentity("agent");
	const effective = useAssistantIdentity();
	const save = useAsync();
	const cur = own.data;
	const put = (name: string | undefined, avatar: AvatarIdentity | undefined) =>
		save.run(() => saveIdentity(qc, "assistant", identityBody(name, avatar)));
	return (
		<Card title={t("identity.assistantTitle")} icon="sparkle">
			<div data-testid="identity-assistant" className="flex flex-col gap-4">
				<div className="flex flex-wrap items-center gap-3">
					<AssistantAvatar size={40} />
					<div className="min-w-0 flex-1">
						<p className="truncate text-sm">
							<AvatarKind value={cur} unset={t("identity.followAgent")} />
						</p>
						<p className="text-faint text-xs">{t("identity.assistantHint")}</p>
					</div>
					<figure className="m-0 flex flex-col items-center gap-1">
						<div
							data-testid="assistant-tab-preview"
							className="flex w-24 flex-col items-center gap-0.5 rounded-lg bg-inset px-2 py-1.5"
						>
							<AssistantAvatar size={20} shape="circle" />
							<span className="max-w-full truncate text-2xs text-fg">
								{effective.name || t("tabBar.assistant")}
							</span>
						</div>
						<figcaption className="text-2xs text-faint">
							{t("identity.tabPreview")}
						</figcaption>
					</figure>
					{(save.busy || own.isPending) && <Spinner />}
				</div>
				<NameField
					value={cur?.name}
					placeholder={agent.data?.name || t("tabBar.assistant")}
					disabled={own.isPending}
					onCommit={(name) => void put(name, cur)}
				/>
				<AvatarEditor
					value={cur}
					busy={save.busy || own.isPending}
					resetLabel={t("identity.followAgent")}
					onChange={(avatar) => put(cur?.name, avatar)}
				/>
				<ErrorText error={save.error ?? own.error} />
			</div>
		</Card>
	);
}

function RoleAvatar({ role }: { role: RoleIdentity }): ReactNode {
	return hasAvatar(role) ? (
		<IdentityAvatar identity={role} of="role" size={32} />
	) : (
		<AvatarTile
			of="role"
			identity={undefined}
			size={32}
			tone="neutral"
			fallback={
				<span className="font-semibold text-sm">
					{firstGrapheme(roleDisplayName(role))}
				</span>
			}
		/>
	);
}

function RoleAvatarsCard(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const q = useRoleIdentities();
	const save = useAsync();
	const [open, setOpen] = useState<string | null>(null);
	const roles = q.data?.roles ?? [];
	const cycle = roles.filter((r) => r.inCycle);
	const orphans = roles.filter((r) => !r.inCycle);
	const put = (role: string, avatar: AvatarIdentity) =>
		save.run(() => saveRoleAvatar(qc, role, avatar));
	return (
		<Card
			title={t("identity.rolesTitle")}
			icon="robot"
			action={(save.busy || q.isPending) && <Spinner />}
		>
			<div data-testid="identity-roles" className="flex flex-col gap-3">
				<p className="text-faint text-xs">{t("identity.rolesHint")}</p>
				{q.data?.configError && (
					<Callout tone="warn" role="status">
						{t("identity.rolesConfigError", { error: q.data.configError })}
					</Callout>
				)}
				<ul className="-mx-2 flex flex-col">
					{cycle.map((r) => {
						const expanded = open === r.role;
						return (
							<li key={r.role} data-role={r.role} className="flex flex-col">
								<div className="flex min-h-12 items-center gap-3 rounded-md px-2 py-1.5">
									<RoleAvatar role={r} />
									<div className="min-w-0 flex-1">
										<p className="truncate font-medium text-sm">
											{roleDisplayName(r)}
										</p>
										<p className="flex min-w-0 items-center gap-2 text-faint text-xs">
											<span
												className={`min-w-0 truncate ${r.selector ? "font-mono" : ""}`}
												title={r.selector}
											>
												{r.selector || t("identity.roleNoModel")}
											</span>
											{!hasAvatar(r) && (
												<span className="shrink-0">
													{t("identity.roleNoAvatar")}
												</span>
											)}
										</p>
									</div>
									<Button
										size="sm"
										variant={expanded ? "secondary" : "ghost"}
										aria-expanded={expanded}
										onClick={() => setOpen(expanded ? null : r.role)}
									>
										{t("identity.setAvatar")}
									</Button>
								</div>
								{expanded && (
									<div className="mx-2 mb-2 rounded-lg border border-border bg-raised p-3">
										<AvatarEditor
											value={r}
											busy={save.busy}
											resetLabel={t("identity.removeAvatar")}
											onChange={(avatar) => put(r.role, avatar)}
										/>
									</div>
								)}
							</li>
						);
					})}
				</ul>
				{orphans.length > 0 && (
					<section className="flex flex-col gap-1">
						<h4 className="font-medium text-muted text-xs">
							{t("identity.rolesOrphans")}
						</h4>
						<ul className="-mx-2 flex flex-col">
							{orphans.map((r) => (
								<li
									key={r.role}
									data-role={r.role}
									className="flex min-h-12 items-center gap-3 rounded-md px-2 py-1.5"
								>
									<RoleAvatar role={r} />
									<p className="min-w-0 flex-1 truncate font-medium text-sm">
										{roleDisplayName(r)}
									</p>
									<Button
										size="sm"
										variant="ghost"
										icon="trash"
										disabled={save.busy}
										onClick={() => void put(r.role, {})}
									>
										{t("identity.removeAvatar")}
									</Button>
								</li>
							))}
						</ul>
					</section>
				)}
				<ErrorText error={save.error ?? q.error} />
				<Link
					to="/settings/agent-config"
					className="self-start text-accent-ink text-xs underline"
				>
					{t("identity.editConfig")}
				</Link>
			</div>
		</Card>
	);
}

function UserAvatarCard(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const q = useIdentity("user");
	const save = useAsync();
	return (
		<Card title={t("appearance.userAvatar")} icon="user">
			<div data-testid="identity-user" className="flex flex-col gap-4">
				<div className="flex items-center gap-3">
					<UserAvatar size={40} />
					<div className="min-w-0 flex-1">
						<p className="truncate text-sm">
							<AvatarKind
								value={q.data}
								unset={t("appearance.userAvatarDefault")}
							/>
						</p>
						<p className="text-faint text-xs">
							{t("appearance.userAvatarHint")}
						</p>
					</div>
					{(save.busy || q.isPending) && <Spinner />}
				</div>
				<AvatarEditor
					value={q.data}
					busy={save.busy || q.isPending}
					resetLabel={t("appearance.avatarReset")}
					onChange={(avatar) =>
						save.run(() => saveIdentity(qc, "user", identityBody("", avatar)))
					}
				/>
				<ErrorText error={save.error ?? q.error} />
			</div>
		</Card>
	);
}

export function IdentitySettings(): ReactNode {
	return (
		<>
			<AgentIdentityCard />
			<AssistantIdentityCard />
			<RoleAvatarsCard />
			<UserAvatarCard />
		</>
	);
}
