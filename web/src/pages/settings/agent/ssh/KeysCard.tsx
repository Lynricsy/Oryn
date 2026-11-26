/**
 * 「钥匙」分组与新建钥匙对话框。钥匙只有全局一层；私钥只存控制面（加密），页面只见公钥与指纹。
 * 生成：只填名称，成功后显示公钥并提示加到目标机的 authorized_keys。导入：名称、私钥、口令（可空；只用于解开，不落库）。
 * 行 = 名称与类型 · 指纹、在用主机数 · 复制公钥（悬停出现）· ⋯（重命名；删除，先确认，被主机引用时的拒绝原因显示在行下）。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type SshKey,
	useCreateSshKey,
	useDeleteSshKey,
	useRenameSshKey,
	useSshKeys,
} from "../../../../api/ssh-queries";
import { CopyButton } from "../../../../components/ui/CopyButton";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Menu,
	Spinner,
	TextArea,
	TextInput,
} from "../../../../components/ui/primitives";
import {
	Group,
	GroupList,
	GroupRow,
	Reveal,
} from "../../../../components/ui/workbench";
import { confirmAction } from "../../../../lib/confirm";

export type KeyMode = "generate" | "import";

/** 生成或导入一把钥匙；成功后留在对话框里显示公钥，「完成」才关闭（`onCreated` 在成功时就调用）。 */
export function KeyDialog({
	mode,
	onClose,
	onCreated,
}: {
	mode: KeyMode;
	onClose: () => void;
	onCreated?: (key: SshKey) => void;
}): ReactNode {
	const { t } = useTranslation();
	const create = useCreateSshKey();
	const [name, setName] = useState("");
	const [privateKey, setPrivateKey] = useState("");
	const [passphrase, setPassphrase] = useState("");
	const created = create.data;
	const ready =
		name.trim() !== "" && (mode === "generate" || privateKey.trim() !== "");
	return (
		<Dialog
			open
			onClose={onClose}
			title={t(`sshPage.keys.${mode}Title`)}
			footer={
				created ? (
					<Button variant="primary" onClick={onClose}>
						{t("sshPage.keys.done")}
					</Button>
				) : (
					<>
						<Button variant="ghost" onClick={onClose}>
							{t("common.cancel")}
						</Button>
						<Button
							variant="primary"
							type="submit"
							form="ssh-key-form"
							disabled={!ready || create.isPending}
						>
							{create.isPending && <Spinner size={12} />}
							{t(`sshPage.keys.${mode}`)}
						</Button>
					</>
				)
			}
		>
			{created ? (
				<div className="flex flex-col gap-2" data-testid="ssh-key-created">
					<p className="text-muted text-sm">{t("sshPage.keys.createdHint")}</p>
					<div className="flex items-start gap-2">
						<code className="min-w-0 flex-1 break-all rounded-sm bg-inset px-2 py-1 font-mono text-fg text-xs">
							{created.publicKey}
						</code>
						<CopyButton text={created.publicKey} />
					</div>
					<p className="text-faint text-xs">
						{created.type} · {created.fingerprint}
					</p>
				</div>
			) : (
				<form
					id="ssh-key-form"
					className="flex flex-col gap-3"
					onSubmit={(e) => {
						e.preventDefault();
						if (!ready) return;
						create.mutate(
							mode === "generate"
								? { name: name.trim(), mode }
								: {
										name: name.trim(),
										mode,
										privateKey,
										...(passphrase ? { passphrase } : {}),
									},
							{ onSuccess: (key) => onCreated?.(key) },
						);
					}}
				>
					<Field
						label={t("sshPage.keys.name")}
						hint={t("sshPage.keys.nameHint")}
					>
						<TextInput
							value={name}
							onChange={(e) => setName(e.target.value)}
							spellCheck={false}
							autoFocus
						/>
					</Field>
					{mode === "import" && (
						<>
							<Field label={t("sshPage.keys.privateKey")}>
								<TextArea
									rows={6}
									value={privateKey}
									onChange={(e) => setPrivateKey(e.target.value)}
									spellCheck={false}
									placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
								/>
							</Field>
							<Field
								label={t("sshPage.keys.passphrase")}
								hint={t("sshPage.keys.passphraseHint")}
							>
								<TextInput
									type="password"
									autoComplete="off"
									value={passphrase}
									onChange={(e) => setPassphrase(e.target.value)}
								/>
							</Field>
						</>
					)}
					<ErrorText error={create.error} />
				</form>
			)}
		</Dialog>
	);
}

function RenameDialog({
	sshKey,
	onClose,
}: {
	sshKey: SshKey;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const rename = useRenameSshKey();
	const [name, setName] = useState(sshKey.name);
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("sshPage.keys.renameTitle")}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						type="submit"
						form="ssh-key-rename"
						disabled={!name.trim() || rename.isPending}
					>
						{t("common.save")}
					</Button>
				</>
			}
		>
			<form
				id="ssh-key-rename"
				className="flex flex-col gap-3"
				onSubmit={(e) => {
					e.preventDefault();
					if (name.trim())
						rename.mutate(
							{ id: sshKey.id, name: name.trim() },
							{ onSuccess: onClose },
						);
				}}
			>
				<Field label={t("sshPage.keys.name")} hint={t("sshPage.keys.nameHint")}>
					<TextInput
						value={name}
						onChange={(e) => setName(e.target.value)}
						spellCheck={false}
						autoFocus
					/>
				</Field>
				<ErrorText error={rename.error} />
			</form>
		</Dialog>
	);
}

function KeyRow({
	sshKey: k,
	onRename,
}: {
	sshKey: SshKey;
	onRename: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const del = useDeleteSshKey();
	return (
		<GroupRow
			icon="key"
			title={
				<>
					<span className="min-w-0 truncate font-mono">{k.name}</span>
					<span className="shrink-0 font-medium text-faint text-xs">
						{k.type}
					</span>
				</>
			}
			meta={
				<>
					<span className="min-w-0 truncate font-mono text-xs">
						{k.fingerprint}
					</span>
					<span className="shrink-0">
						{t("sshPage.keys.hostCount", { count: k.hostCount })}
					</span>
				</>
			}
			trailing={
				<>
					<Reveal>
						<CopyButton
							text={k.publicKey}
							ariaLabel={t("sshPage.keys.copyPublic")}
						/>
					</Reveal>
					<Menu
						items={[
							{
								key: "rename",
								icon: "edit",
								label: t("sshPage.keys.rename"),
								onSelect: onRename,
							},
							{
								key: "delete",
								icon: "trash",
								danger: true,
								label: t("common.delete"),
								disabled: del.isPending,
								onSelect: async () => {
									if (
										await confirmAction({
											title: t("sshPage.keys.deleteTitle", { name: k.name }),
											body: t("sshPage.keys.deleteHint"),
											action: t("common.delete"),
											danger: true,
										})
									)
										del.mutate(k.id);
								},
							},
						]}
						trigger={(p) => (
							<IconButton
								{...p}
								icon="more"
								box="sm"
								label={t("sshPage.keys.actions", { name: k.name })}
							/>
						)}
					/>
				</>
			}
		>
			{del.error ? <ErrorText error={del.error} /> : undefined}
		</GroupRow>
	);
}

type KeyAction =
	| { kind: "create"; mode: KeyMode }
	| { kind: "rename"; key: SshKey };

export function KeysCard(): ReactNode {
	const { t } = useTranslation();
	const keys = useSshKeys();
	const [action, setAction] = useState<KeyAction | null>(null);
	const list = keys.data?.keys ?? [];
	const close = () => setAction(null);
	return (
		<div data-testid="ssh-keys" className="contents">
			<Group
				title={t("sshPage.keys.title")}
				action={
					<>
						<Button
							size="sm"
							icon="plus"
							onClick={() => setAction({ kind: "create", mode: "generate" })}
						>
							{t("sshPage.keys.generate")}
						</Button>
						<Button
							size="sm"
							variant="ghost"
							icon="upload"
							onClick={() => setAction({ kind: "create", mode: "import" })}
						>
							{t("sshPage.keys.import")}
						</Button>
					</>
				}
			>
				{keys.isPending && <Spinner size={12} />}
				<ErrorText error={keys.error} />
				{keys.isSuccess && list.length === 0 && (
					<p className="px-1.5 text-faint text-sm">{t("sshPage.keys.empty")}</p>
				)}
				{list.length > 0 && (
					<GroupList>
						{list.map((k) => (
							<KeyRow
								key={k.id}
								sshKey={k}
								onRename={() => setAction({ kind: "rename", key: k })}
							/>
						))}
					</GroupList>
				)}
				{action?.kind === "create" && (
					<KeyDialog mode={action.mode} onClose={close} />
				)}
				{action?.kind === "rename" && (
					<RenameDialog sshKey={action.key} onClose={close} />
				)}
			</Group>
		</div>
	);
}
