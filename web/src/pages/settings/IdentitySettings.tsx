/**
 * 设置 → 身份与头像（M9.6 / M13）：默认 Agent 身份（名字 + 头像）、全局助手（名字 + 头像，留空显示「全局助手」与 sparkle，
 * 带底栏预览）、role 头像入口（链接卡，头像改在「Agent › 模型角色」里编辑）、新对话默认对象、我的头像。
 * 身份接口是整体替换：每次保存都发完整对象——只改名字时带上当前头像，只改头像时带上当前名字（见 `useIdentityWriter`）。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Schemas } from "../../api/client";
import {
	identityKeys,
	saveComposerPrefs,
	saveIdentity,
	useAssistantIdentity,
	useComposerPrefs,
	useIdentity,
} from "../../api/identity-queries";
import { AvatarEditor } from "../../components/settings/AvatarEditor";
import { useAsync } from "../../components/settings/useAsync";
import { Card } from "../../components/ui/data";
import {
	AssistantAvatar,
	IdentityAvatar,
} from "../../components/ui/IdentityAvatar";
import {
	ErrorText,
	Field,
	Radio,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import { UserAvatar } from "../../components/ui/UserAvatar";

type AgentIdentity = Schemas["AgentIdentity"];
type AvatarIdentity = Schemas["AvatarIdentity"];
type NewThreadTarget = Schemas["ComposerPrefs"]["defaultTarget"];

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

/** 一次身份编辑只是局部意图：改名字或改头像。 */
type IdentityPatch = { name: string } | { avatar: AvatarIdentity };

/**
 * Agent / 助手身份的写入。写入串行排队，轮到时才取缓存里的最新身份（前一次成功后已写回缓存）合并本次改动再发，
 * 不复用事件闭包里的旧对象——否则头像 PUT 未完成时提交名字，会把旧头像随整体替换带回去。
 * 排队而不锁输入：等待期间两个字段的改动都不丢。
 */
function useIdentityWriter(who: "agent" | "assistant") {
	const qc = useQueryClient();
	const save = useAsync();
	const queue = useRef<Promise<unknown>>(Promise.resolve());
	const write = (patch: IdentityPatch): Promise<boolean> => {
		const next = queue.current.then(() =>
			save.run(() => {
				const cur = qc.getQueryData<AgentIdentity>(identityKeys[who]);
				const name = "name" in patch ? patch.name : cur?.name;
				const avatar = "avatar" in patch ? patch.avatar : cur;
				return saveIdentity(qc, who, identityBody(name, avatar));
			}),
		);
		queue.current = next;
		return next;
	};
	return { busy: save.busy, error: save.error, write };
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
	const q = useIdentity("agent");
	const save = useIdentityWriter("agent");
	const cur = q.data;
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
					onCommit={(name) => void save.write({ name })}
				/>
				<AvatarEditor
					value={cur}
					busy={save.busy || q.isPending}
					resetLabel={t("appearance.avatarReset")}
					onChange={(avatar) => save.write({ avatar })}
				/>
				<ErrorText error={save.error ?? q.error} />
			</div>
		</Card>
	);
}

function AssistantIdentityCard(): ReactNode {
	const { t } = useTranslation();
	const own = useIdentity("assistant");
	const effective = useAssistantIdentity();
	const save = useIdentityWriter("assistant");
	const cur = own.data;
	return (
		<Card title={t("identity.assistantTitle")} icon="sparkle">
			<div data-testid="identity-assistant" className="flex flex-col gap-4">
				<div className="flex flex-wrap items-center gap-3">
					<AssistantAvatar size={40} />
					<div className="min-w-0 flex-1">
						<p className="truncate text-sm">
							<AvatarKind value={cur} unset={t("identity.assistantDefault")} />
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
					placeholder={t("tabBar.assistant")}
					disabled={own.isPending}
					onCommit={(name) => void save.write({ name })}
				/>
				<AvatarEditor
					value={cur}
					busy={save.busy || own.isPending}
					resetLabel={t("appearance.avatarReset")}
					onChange={(avatar) => save.write({ avatar })}
				/>
				<ErrorText error={save.error ?? own.error} />
			</div>
		</Card>
	);
}

/** role 头像入口：头像改在「Agent › 模型角色」编辑。 */
function RoleAvatarsLinkCard(): ReactNode {
	const { t } = useTranslation();
	return (
		<Card title={t("identity.rolesTitle")} icon="robot">
			<div data-testid="identity-roles" className="flex flex-col gap-2">
				<p className="text-faint text-xs">{t("identity.rolesHint")}</p>
				<Link
					to="/settings/agent-roles"
					className="self-start text-accent-ink text-xs underline"
				>
					{t("identity.rolesLink")}
				</Link>
			</div>
		</Card>
	);
}

const NEW_THREAD_TARGETS: readonly NewThreadTarget[] = ["role", "assistant"];

/** 新对话默认对象（R18）：存在服务端，首屏每次打开都按它预选。 */
function NewThreadTargetCard(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const q = useComposerPrefs();
	const save = useAsync();
	const cur = q.data?.defaultTarget;
	return (
		<Card
			title={t("identity.newThreadTitle")}
			icon="chats"
			action={(save.busy || q.isPending) && <Spinner />}
		>
			<div data-testid="identity-new-thread" className="flex flex-col gap-3">
				<p className="text-faint text-xs">{t("identity.newThreadHint")}</p>
				<fieldset className="m-0 flex flex-col gap-2 border-0 p-0">
					<legend className="sr-only">{t("identity.newThreadTitle")}</legend>
					{NEW_THREAD_TARGETS.map((v) => (
						// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Radio 组件（内含原生 input）
						<label
							key={v}
							className={`flex cursor-pointer gap-3 rounded-lg border p-3 transition-colors duration-150 ${cur === v ? "border-border-strong bg-hover" : "border-border hover:bg-hover"}`}
						>
							<Radio
								name="new-thread-target"
								checked={cur === v}
								disabled={q.isPending || save.busy}
								onChange={() =>
									void save.run(() =>
										saveComposerPrefs(qc, { defaultTarget: v }),
									)
								}
								className="mt-0.5"
							/>
							<span className="min-w-0">
								<span className="block font-medium text-sm">
									{t(`identity.newThreadTargets.${v}`)}
								</span>
								<span className="block text-muted text-xs">
									{t(`identity.newThreadTargets.${v}Hint`)}
								</span>
							</span>
						</label>
					))}
				</fieldset>
				<ErrorText error={save.error ?? q.error} />
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
			<NewThreadTargetCard />
			<RoleAvatarsLinkCard />
			<UserAvatarCard />
		</>
	);
}
