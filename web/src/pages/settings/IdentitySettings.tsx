/**
 * 身份分组（M9.6 / M13），分别挂在各自的设置分区，都是工作台原生（D）的 `Group`：
 * - 默认 Agent 身份（名字 + 头像）→「模型与角色」；全局助手（名字 + 头像，留空显示「全局助手」与 sparkle，带底栏预览）→「全局助手」；
 * - 新对话默认对象 →「输入与快捷键」；我的头像 →「账户与设备」。
 * 身份接口是整体替换：每次保存都发完整对象——只改名字时带上当前头像，只改头像时带上当前名字（见 `useIdentityWriter`）。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
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
import {
	AssistantAvatar,
	IdentityAvatar,
} from "../../components/ui/IdentityAvatar";
import {
	ErrorText,
	Radio,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import { UserAvatar } from "../../components/ui/UserAvatar";
import {
	FormField,
	Group,
	GroupList,
	GroupPanel,
	GroupRow,
} from "../../components/ui/workbench";

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
		<FormField label={t("identity.name")}>
			{(p) => (
				<TextInput
					{...p}
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
			)}
		</FormField>
	);
}

/** 面板顶部：大头像 + 头像种类 + 一句说明（+ 右侧附加，如底栏预览）。 */
function AvatarHead({
	avatar,
	kind,
	hint,
	aside,
}: {
	avatar: ReactNode;
	kind: ReactNode;
	hint: string;
	aside?: ReactNode;
}): ReactNode {
	return (
		<div className="flex flex-wrap items-center gap-3">
			{avatar}
			<div className="flex min-w-40 flex-1 flex-col gap-0.5">
				<p className="truncate font-[550] text-fg text-sm">{kind}</p>
				<p className="text-[12.5px] text-muted leading-normal">{hint}</p>
			</div>
			{aside}
		</div>
	);
}

export function AgentIdentityCard(): ReactNode {
	const { t } = useTranslation();
	const q = useIdentity("agent");
	const save = useIdentityWriter("agent");
	const cur = q.data;
	return (
		<Group
			title={t("identity.agentTitle")}
			action={(save.busy || q.isPending) && <Spinner />}
		>
			<GroupPanel>
				<div data-testid="identity-agent" className="flex flex-col gap-4">
					<AvatarHead
						avatar={<IdentityAvatar identity={cur} of="agent" size={40} />}
						kind={
							<AvatarKind
								value={cur}
								unset={t("appearance.avatarKind.default")}
							/>
						}
						hint={t("identity.agentHint")}
					/>
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
			</GroupPanel>
		</Group>
	);
}

export function AssistantIdentityCard(): ReactNode {
	const { t } = useTranslation();
	const own = useIdentity("assistant");
	const effective = useAssistantIdentity();
	const save = useIdentityWriter("assistant");
	const cur = own.data;
	return (
		<Group
			title={t("identity.assistantTitle")}
			action={(save.busy || own.isPending) && <Spinner />}
		>
			<GroupPanel>
				<div data-testid="identity-assistant" className="flex flex-col gap-4">
					<AvatarHead
						avatar={<AssistantAvatar size={40} />}
						kind={
							<AvatarKind value={cur} unset={t("identity.assistantDefault")} />
						}
						hint={t("identity.assistantHint")}
						aside={
							<figure className="m-0 flex flex-col items-center gap-1">
								<div
									data-testid="assistant-tab-preview"
									className="flex w-24 flex-col items-center gap-0.5 rounded-[10px] bg-inset px-2 py-1.5"
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
						}
					/>
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
			</GroupPanel>
		</Group>
	);
}

const NEW_THREAD_TARGETS: readonly NewThreadTarget[] = ["role", "assistant"];
const NEW_THREAD_ICON = { role: "robot", assistant: "sparkle" } as const;

/** 新对话默认对象（R18）：存在服务端，首屏每次打开都按它预选。一行一个对象，行尾单选。 */
export function NewThreadTargetCard(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const q = useComposerPrefs();
	const save = useAsync();
	const cur = q.data?.defaultTarget;
	const base = useId();
	return (
		<Group
			title={t("identity.newThreadTitle")}
			action={(save.busy || q.isPending) && <Spinner />}
		>
			<div data-testid="identity-new-thread" className="flex flex-col gap-2">
				<GroupList>
					{NEW_THREAD_TARGETS.map((v) => (
						<GroupRow
							key={v}
							icon={NEW_THREAD_ICON[v]}
							title={
								<label htmlFor={`${base}-${v}`} className="cursor-pointer">
									{t(`identity.newThreadTargets.${v}`)}
								</label>
							}
							meta={t(`identity.newThreadTargets.${v}Hint`)}
							trailing={
								<Radio
									id={`${base}-${v}`}
									name={`${base}-target`}
									checked={cur === v}
									disabled={q.isPending || save.busy}
									onChange={() =>
										void save.run(() =>
											saveComposerPrefs(qc, { defaultTarget: v }),
										)
									}
								/>
							}
						/>
					))}
				</GroupList>
				<p className="px-1.5 text-[12.5px] text-muted leading-normal">
					{t("identity.newThreadHint")}
				</p>
				<ErrorText error={save.error ?? q.error} />
			</div>
		</Group>
	);
}

export function UserAvatarCard(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const q = useIdentity("user");
	const save = useAsync();
	return (
		<Group
			title={t("appearance.userAvatar")}
			action={(save.busy || q.isPending) && <Spinner />}
		>
			<GroupPanel>
				<div data-testid="identity-user" className="flex flex-col gap-4">
					<AvatarHead
						avatar={<UserAvatar size={40} />}
						kind={
							<AvatarKind
								value={q.data}
								unset={t("appearance.userAvatarDefault")}
							/>
						}
						hint={t("appearance.userAvatarHint")}
					/>
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
			</GroupPanel>
		</Group>
	);
}
