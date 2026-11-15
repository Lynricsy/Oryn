/**
 * 设置 → Agent 各子页共用的小件：作用范围说明、错误行（写入冲突、config.yml 不合法有专门文案）、
 * 删除确认、懒加载的代码编辑器，以及整文件编辑对话框（提示词文件、扩展 / hook / 工具源码）。
 */
import { lazy, type ReactNode, Suspense, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	ConfigInvalidError,
	isModifiedError,
	useAgentFileActions,
	useAgentText,
} from "../../api/agent-config-queries";
import { Callout } from "../ui/data";
import { Button, Dialog, ErrorText, Spinner } from "../ui/primitives";

const CodeEditor = lazy(() => import("../editor/CodeEditor"));

/** R31：各子页都标明不作用于全局助手；保存即下发。 */
export function AgentScopeNote({ children }: { children?: ReactNode }) {
	const { t } = useTranslation();
	return (
		<Callout tone="neutral">
			{t("agentPages.scope")}
			{children && <> {children}</>}
		</Callout>
	);
}

/** 错误行：冲突与 config.yml 不合法给出可操作的说明，其余走通用的 `ErrorText`。 */
export function AgentErrorText({ error }: { error: unknown }) {
	const { t } = useTranslation();
	if (!error) return null;
	if (isModifiedError(error))
		return <ErrorText error={new Error(t("agentPages.errors.modified"))} />;
	if (error instanceof ConfigInvalidError)
		return (
			<ErrorText
				error={
					new Error(
						t("agentPages.errors.configInvalid", { reason: error.message }),
					)
				}
			/>
		);
	return <ErrorText error={error} />;
}

/** CodeMirror（懒加载），外框是输入框同款内嵌井。 */
export function CodeField({
	value,
	path,
	onChange,
	onSave,
	tall,
}: {
	value: string;
	/** 用于选语言与无障碍名称 */
	path: string;
	onChange(v: string): void;
	onSave?(): void;
	tall?: boolean;
}) {
	const { t } = useTranslation();
	return (
		<div
			className={`overflow-hidden rounded-md border border-border bg-inset ${tall ? "h-[26rem]" : "h-64"}`}
		>
			<Suspense
				fallback={
					<div className="p-3 text-faint text-xs">{t("editor.loading")}</div>
				}
			>
				<CodeEditor
					value={value}
					path={path}
					onChange={onChange}
					onSave={onSave}
				/>
			</Suspense>
		</div>
	);
}

/** 删除确认。`paths` 多于一个时说明会删掉整个目录。 */
export function ConfirmDeleteDialog({
	name,
	paths,
	onClose,
	onDeleted,
}: {
	name: string;
	paths: readonly string[];
	onClose(): void;
	onDeleted?(): void;
}) {
	const { t } = useTranslation();
	const actions = useAgentFileActions();
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const remove = async () => {
		setBusy(true);
		setError(null);
		try {
			await actions.remove(paths);
			onDeleted?.();
			onClose();
		} catch (e) {
			setError(e);
			setBusy(false);
		}
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("agentPages.deleteTitle", { name })}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="danger"
						disabled={busy}
						onClick={() => void remove()}
					>
						{busy && <Spinner size={12} />}
						{t("common.delete")}
					</Button>
				</>
			}
		>
			<p className="text-muted text-sm">
				{paths.length === 1
					? t("agentPages.deleteFile", { path: paths[0] })
					: t("agentPages.deleteFiles", { count: paths.length })}
			</p>
			<div className="mt-2">
				<AgentErrorText error={error} />
			</div>
		</Dialog>
	);
}

/**
 * 整文件编辑：读当前原文（不存在则从空白开始，保存即创建），保存带冲突检查。
 * `before` 放在编辑器上方（如 SYSTEM.md 的警示）。
 */
export function TextFileDialog({
	path,
	title,
	before,
	onClose,
}: {
	path: string;
	title: ReactNode;
	before?: ReactNode;
	onClose(): void;
}) {
	const { t } = useTranslation();
	const q = useAgentText(path);
	const actions = useAgentFileActions();
	const [draft, setDraft] = useState<string | null>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const base = q.data;
	const value = draft ?? base ?? "";
	const save = async () => {
		if (base === undefined || busy) return;
		setBusy(true);
		setError(null);
		try {
			await actions.write(path, value, base);
			onClose();
		} catch (e) {
			// 冲突：缓存已换成最新内容，丢掉草稿让编辑器显示它
			if (isModifiedError(e)) setDraft(null);
			setError(e);
			setBusy(false);
		}
	};
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={title}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						disabled={base === undefined || busy}
						onClick={() => void save()}
					>
						{busy && <Spinner size={12} />}
						{t("common.save")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3">
				{before}
				{q.error ? (
					<ErrorText error={q.error} />
				) : base === undefined ? (
					<div className="flex h-64 items-center justify-center">
						<Spinner />
					</div>
				) : (
					<CodeField
						tall
						value={value}
						path={path}
						onChange={setDraft}
						onSave={() => void save()}
					/>
				)}
				<AgentErrorText error={error} />
			</div>
		</Dialog>
	);
}
