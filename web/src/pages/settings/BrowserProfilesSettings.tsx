/**
 * 设置页「浏览器 Profile」（M8 4.6，default export，由设置页接入）：顶部新建表单（名字）；
 * 列表每行 = 名字、大小、上次保存时间、状态（空闲 / 使用中 · 线程（链接）/ 只读副本数 / 上次保存失败的原因），
 * 行内重命名与删除（`window.confirm`；被独占持有时禁用并提示先在 Desktop 面板解除）。
 */
import { type ReactNode, useState } from "react";
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
import { Badge, Card, ListRow } from "../../components/ui/data";
import { Icon } from "../../components/ui/icons";
import {
	Button,
	EmptyState,
	ErrorText,
	IconButton,
	Spinner,
	TextInput,
} from "../../components/ui/primitives";
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

function LeadIcon() {
	return (
		<span className="grid h-8 w-8 place-items-center rounded-md border border-border bg-raised text-muted">
			<Icon name="browser" size={15} />
		</span>
	);
}

function ProfileRow({ profile }: { profile: Profile }): ReactNode {
	const { t } = useTranslation();
	const rename = useRenameBrowserProfile();
	const del = useDeleteBrowserProfile();
	const [editing, setEditing] = useState<string | null>(null);
	const holder = profile.holder;
	const readers = profile.readers?.length ?? 0;
	const submitRename = () => {
		if (editing === null || !validProfileName(editing)) return;
		if (editing.trim() === profile.name) {
			setEditing(null);
			return;
		}
		rename.mutate(
			{ id: profile.id, name: editing.trim() },
			{ onSuccess: () => setEditing(null) },
		);
	};
	return (
		<li data-testid="browser-profile-row">
			{editing !== null ? (
				<form
					className="flex items-center gap-2 px-2 py-2"
					onSubmit={(e) => {
						e.preventDefault();
						submitRename();
					}}
				>
					<TextInput
						autoFocus
						aria-label={t("browserProfiles.name")}
						maxLength={40}
						value={editing}
						onChange={(e) => setEditing(e.target.value)}
						onKeyDown={(e) => {
							if (e.key === "Escape") setEditing(null);
						}}
					/>
					<Button
						type="submit"
						size="sm"
						variant="primary"
						disabled={rename.isPending || !validProfileName(editing)}
					>
						{t("common.save")}
					</Button>
					<Button size="sm" onClick={() => setEditing(null)}>
						{t("common.cancel")}
					</Button>
				</form>
			) : (
				<ListRow
					leading={<LeadIcon />}
					title={<span className="truncate font-medium">{profile.name}</span>}
					meta={
						<>
							{holder ? (
								<Badge tone="frost" icon="lock">
									{holder.threadId ? (
										<Link
											to={`/t/${holder.threadId}`}
											className="hover:underline"
										>
											{t("browserProfiles.inUse", {
												thread: holderTitle(holder, t),
											})}
										</Link>
									) : (
										t("browserProfiles.inUse", {
											thread: holderTitle(holder, t),
										})
									)}
								</Badge>
							) : (
								<Badge>{t("browserProfiles.idle")}</Badge>
							)}
							{readers > 0 && (
								<Badge icon="eye">
									{t("browserProfiles.readers", { count: readers })}
								</Badge>
							)}
							{profile.saveError && (
								<Badge tone="warn" icon="alert">
									{t("browserProfiles.saveFailed", {
										reason: t(`browserProfiles.saveError.${profile.saveError}`),
									})}
								</Badge>
							)}
							<span>
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
						<>
							<IconButton
								icon="edit"
								label={t("browserProfiles.rename")}
								onClick={() => setEditing(profile.name)}
							/>
							<IconButton
								icon="trash"
								label={
									holder ? t("browserProfiles.deleteHeld") : t("common.delete")
								}
								disabled={!!holder || del.isPending}
								onClick={() => {
									if (
										!window.confirm(
											t("browserProfiles.deleteConfirm", {
												name: profile.name,
											}),
										)
									)
										return;
									del.mutate(profile.id);
								}}
							/>
						</>
					}
				/>
			)}
			<div className="px-2 empty:hidden">
				<ErrorText error={rename.error ?? del.error} />
			</div>
		</li>
	);
}

export default function BrowserProfilesSettings(): ReactNode {
	const { t } = useTranslation();
	const profiles = useBrowserProfiles();
	const create = useCreateBrowserProfile();
	const [name, setName] = useState("");
	const list = profiles.data?.profiles ?? [];
	return (
		<>
			<Card title={t("browserProfiles.new")} icon="plus">
				<form
					className="flex flex-col gap-2 sm:flex-row sm:items-start"
					onSubmit={(e) => {
						e.preventDefault();
						if (!validProfileName(name)) return;
						create.mutate(name.trim(), { onSuccess: () => setName("") });
					}}
				>
					<div className="min-w-0 flex-1">
						<TextInput
							aria-label={t("browserProfiles.name")}
							placeholder={t("browserProfiles.namePlaceholder")}
							maxLength={40}
							value={name}
							onChange={(e) => setName(e.target.value)}
						/>
					</div>
					<Button
						type="submit"
						variant="primary"
						icon="plus"
						disabled={create.isPending || !validProfileName(name)}
					>
						{t("common.create")}
					</Button>
				</form>
				<div className="mt-2 empty:hidden">
					<ErrorText error={create.error} />
				</div>
			</Card>
			<Card title={t("browserProfiles.title")} icon="browser">
				{profiles.isPending && <Spinner />}
				<ErrorText error={profiles.error} />
				{list.length > 0 && (
					<ul className="-mx-2 flex flex-col">
						{list.map((p) => (
							<ProfileRow key={p.id} profile={p} />
						))}
					</ul>
				)}
				{profiles.isSuccess && list.length === 0 && (
					<EmptyState icon="browser" title={t("browserProfiles.emptyTitle")}>
						{t("browserProfiles.emptyHint")}
					</EmptyState>
				)}
				{list.length > 0 && (
					<p className="mt-2 text-faint text-xs">{t("browserProfiles.hint")}</p>
				)}
			</Card>
		</>
	);
}
