/**
 * 设置 → 浏览器 Profile（M8 4.6）：分组右上「新建」打开对话框（名字）；
 * 每行 = 名字 · 状态（空闲 / 使用中 · 线程（链接）/ 只读副本数 / 上次保存失败的原因）、大小、上次保存时间 ·
 * ⋯（重命名走对话框；删除先经应用内确认 `confirmAction`，被独占持有时禁用并提示先在 Desktop 面板解除）。
 */
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import type { Schemas } from "../../api/client";
import {
	useBrowserProfiles,
	useCreateBrowserProfile,
	useDeleteBrowserProfile,
	useRenameBrowserProfile,
} from "../../api/queries";
import { holderTitle } from "../../components/desktop/profile-status";
import { Badge } from "../../components/ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Menu,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
import {
	FormField,
	Group,
	GroupList,
	GroupRow,
} from "../../components/ui/workbench";
import { confirmAction } from "../../lib/confirm";
import { formatBytes, formatRelative } from "../../lib/format";

type Profile = Schemas["BrowserProfile"];

/** 名字：trim 后 1–40 个字符、不含控制字符（服务端同样校验）。 */
export function validProfileName(name: string): boolean {
	const chars = [...name.trim()];
	return (
		chars.length >= 1 &&
		chars.length <= 40 &&
		!chars.some((c) => {
			const cp = c.codePointAt(0) ?? 0;
			return cp < 0x20 || cp === 0x7f;
		})
	);
}

/** 新建或重命名：只有名字一个字段；`profile` 给出时是重命名。 */
function NameDialog({
	profile,
	onClose,
}: {
	profile?: Profile;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const formId = useId();
	const create = useCreateBrowserProfile();
	const rename = useRenameBrowserProfile();
	const [name, setName] = useState(profile?.name ?? "");
	const pending = create.isPending || rename.isPending;
	const valid = validProfileName(name);
	const submit = () => {
		if (!valid || pending) return;
		const next = name.trim();
		if (!profile) create.mutate(next, { onSuccess: onClose });
		else if (next === profile.name) onClose();
		else rename.mutate({ id: profile.id, name: next }, { onSuccess: onClose });
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={
				profile ? t("browserProfiles.renameTitle") : t("browserProfiles.new")
			}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						type="submit"
						form={formId}
						variant="primary"
						disabled={!valid || pending}
					>
						{pending && <Spinner size={13} />}
						{profile ? t("common.save") : t("common.create")}
					</Button>
				</>
			}
		>
			<form
				id={formId}
				className="flex flex-col gap-4"
				onSubmit={(e) => {
					e.preventDefault();
					submit();
				}}
			>
				<FormField label={t("browserProfiles.name")}>
					{(p) => (
						<TextInput
							{...p}
							autoFocus
							maxLength={40}
							placeholder={t("browserProfiles.namePlaceholder")}
							value={name}
							onChange={(e) => setName(e.target.value)}
						/>
					)}
				</FormField>
				<ErrorText error={create.error ?? rename.error} />
			</form>
		</Dialog>
	);
}

function ProfileRow({
	profile,
	onRename,
}: {
	profile: Profile;
	onRename: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const del = useDeleteBrowserProfile();
	const holder = profile.holder;
	const readers = profile.readers?.length ?? 0;
	const inUse = holder && (
		<Badge tone="frost" icon="lock">
			{t("browserProfiles.inUse", { thread: holderTitle(holder, t) })}
		</Badge>
	);
	return (
		<GroupRow
			icon="browser"
			title={
				<span data-testid="browser-profile-row" className="truncate">
					{profile.name}
				</span>
			}
			meta={
				<>
					{holder?.threadId ? (
						// 链接包在徽章外：徽章里的文字截断会把链接的焦点环一起切掉
						<Link
							to={`/t/${holder.threadId}`}
							className="inline-flex min-w-0 max-w-full rounded hover:[&_span]:underline"
						>
							{inUse}
						</Link>
					) : holder ? (
						inUse
					) : (
						<span>{t("browserProfiles.idle")}</span>
					)}
					{readers > 0 && (
						<Badge icon="eye">
							{t("browserProfiles.readers", { count: readers })}
						</Badge>
					)}
					<span className="tabular-nums">
						{profile.revision > 0
							? formatBytes(profile.sizeBytes)
							: t("browserProfiles.empty")}
					</span>
					<span>
						{profile.savedAt
							? t("browserProfiles.savedAt", {
									when: formatRelative(profile.savedAt),
								})
							: t("browserProfiles.neverSaved")}
					</span>
				</>
			}
			trailing={
				<Menu
					trigger={(p) => (
						<IconButton
							icon="more"
							box="sm"
							label={t("browserProfiles.more", { name: profile.name })}
							{...p}
						/>
					)}
					items={[
						{
							key: "rename",
							icon: "edit",
							label: t("browserProfiles.rename"),
							onSelect: onRename,
						},
						{
							key: "delete",
							icon: "trash",
							label: t("common.delete"),
							danger: true,
							disabled: !!holder || del.isPending,
							hint: holder ? t("browserProfiles.deleteHeld") : undefined,
							title: holder ? t("browserProfiles.deleteHeld") : undefined,
							onSelect: async () => {
								if (
									await confirmAction({
										title: t("browserProfiles.deleteConfirm", {
											name: profile.name,
										}),
										body: t("browserProfiles.deleteConfirmBody"),
										action: t("common.delete"),
										danger: true,
									})
								)
									del.mutate(profile.id);
							},
						},
					]}
				/>
			}
		>
			{(profile.saveError || del.error) && (
				<div className="flex flex-col gap-1">
					{profile.saveError && (
						<p className="text-[12.5px] text-warn-ink">
							{t("browserProfiles.saveFailed", {
								reason: t(`browserProfiles.saveError.${profile.saveError}`),
							})}
						</p>
					)}
					<ErrorText error={del.error} />
				</div>
			)}
		</GroupRow>
	);
}

export default function BrowserProfilesSettings(): ReactNode {
	const { t } = useTranslation();
	const profiles = useBrowserProfiles();
	const [editing, setEditing] = useState<Profile | "new" | null>(null);
	const list = profiles.data?.profiles ?? [];
	return (
		<Group
			title={t("browserProfiles.title")}
			action={
				<Button size="sm" icon="plus" onClick={() => setEditing("new")}>
					{t("browserProfiles.add")}
				</Button>
			}
		>
			{profiles.isPending && <Spinner />}
			<ErrorText error={profiles.error} />
			{list.length > 0 && (
				<GroupList>
					{list.map((p) => (
						<ProfileRow key={p.id} profile={p} onRename={() => setEditing(p)} />
					))}
				</GroupList>
			)}
			{profiles.isSuccess && (
				<p className="px-1.5 text-faint text-sm">
					{list.length === 0 && (
						<span className="text-muted">
							{t("browserProfiles.emptyTitle")}
							{" · "}
						</span>
					)}
					{t("browserProfiles.emptyHint")}
				</p>
			)}
			{editing && (
				<NameDialog
					profile={editing === "new" ? undefined : editing}
					onClose={() => setEditing(null)}
				/>
			)}
		</Group>
	);
}
