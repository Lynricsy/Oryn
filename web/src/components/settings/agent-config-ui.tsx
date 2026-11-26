/**
 * 设置 → Agent 各子页共用的小件：页头（作用范围说明 + 下发状态）、错误行（写入冲突、config.yml 不合法有专门文案）、
 * 删除确认、懒加载的代码编辑器，以及整文件编辑对话框（提示词文件、扩展 / hook / 工具源码）。
 */
import {
	type ComponentProps,
	lazy,
	type ReactNode,
	Suspense,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	ConfigInvalidError,
	isModifiedError,
	useAgentFileActions,
	useAgentText,
} from "../../api/agent-config-queries";
import { Button, Dialog, ErrorText, Spinner } from "../ui/primitives";
import { ApplyStatusBanner } from "./ApplyStatusBanner";

const CodeEditor = lazy(() => import("../editor/CodeEditor"));

/**
 * R31：各子页都标明不作用于全局助手；保存即下发。只改 `config.yml`、且所改的键对全局助手同样有效的页面
 * （模型角色）传 `reachesAssistant`：`config.yml` 也下发给全局助手。中性说明，一行次要灰字，不用 Callout。
 */
export function AgentScopeNote({
	children,
	reachesAssistant = false,
}: {
	children?: ReactNode;
	reachesAssistant?: boolean;
}) {
	const { t } = useTranslation();
	return (
		<p className="px-1.5 text-[12.5px] text-muted leading-normal">
			{t(reachesAssistant ? "agentPages.scopeConfig" : "agentPages.scope")}
			{children && <> {children}</>}
		</p>
	);
}

/** 分组下方的中性说明（12.5、次要灰，与分组标题左对齐）：放不进行 meta 的长说明。 */
export function GroupNote({ children }: { children: ReactNode }) {
	return (
		<p className="px-1.5 text-[12.5px] text-muted leading-normal">{children}</p>
	);
}

/** 分组里的空状态 / 单句提示：一块毛玻璃里的一行次要灰字（代替空的 `GroupList`）。 */
export function EmptyPanel({ children }: { children: ReactNode }) {
	return (
		<div className="glass rounded-[22px] px-4 py-3.5 text-[12.5px] text-muted">
			{children}
		</div>
	);
}

/**
 * 带头像的行：与 `GroupRow` 同一套行高、内边距与悬停底色，行首是头像（role、子 agent）而不是图标方块。
 * 其余 `li` 属性（`data-*`、拖放事件）原样透传。
 */
export function AvatarRow({
	leading,
	title,
	meta,
	trailing,
	className = "",
	...li
}: Omit<ComponentProps<"li">, "title"> & {
	leading: ReactNode;
	title: ReactNode;
	meta?: ReactNode;
	trailing?: ReactNode;
}) {
	return (
		<li
			{...li}
			className={`group/row transition-colors duration-150 hover:bg-raised ${className}`}
		>
			<div className="flex min-h-14 flex-wrap items-center gap-x-3 gap-y-2 py-2 pr-3 pl-3.5">
				{leading}
				<div className="flex min-w-40 flex-1 flex-col gap-0.5">
					<div className="flex min-w-0 items-center gap-1.5 font-[550] text-fg text-sm">
						{title}
					</div>
					{meta && (
						<div className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-[12.5px] text-muted leading-normal">
							{meta}
						</div>
					)}
				</div>
				{trailing && (
					<div className="ml-auto flex shrink-0 items-center gap-2">
						{trailing}
					</div>
				)}
			</div>
		</li>
	);
}

/**
 * Agent 子页的页头：作用范围说明 + 下发状态横幅（没有待办时不渲染）。每页只放一次，放在第一个 `Group` 之前。
 */
export function AgentPageHead({
	children,
	reachesAssistant = false,
}: {
	children?: ReactNode;
	reachesAssistant?: boolean;
}) {
	return (
		<div className="flex flex-col gap-2">
			<AgentScopeNote reachesAssistant={reachesAssistant}>
				{children}
			</AgentScopeNote>
			<ApplyStatusBanner />
		</div>
	);
}

/**
 * 编辑对话框的保存冲突：打开后文件在别处被改过。草稿保留，但基线仍是打开时的原文，再点保存还会冲突，
 * 不会把草稿静默盖到新内容上；只能明确「载入最新内容」（放弃草稿）后重做。
 */
export function EditConflict({ onReload }: { onReload(): void }) {
	const { t } = useTranslation();
	return (
		<div className="flex flex-wrap items-center gap-2">
			<ErrorText error={new Error(t("agentPages.errors.editConflict"))} />
			<Button variant="ghost" onClick={onReload}>
				{t("agentPages.reloadLatest")}
			</Button>
		</div>
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
 * 开始编辑时冻结基线：之后查询刷新（窗口聚焦、冲突后的重取）不会挪动它，保存只在文件仍是基线时写入。
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
	/** 开始编辑时的原文（null = 文件当时不存在）；未编辑时跟随查询。 */
	const [pinned, setPinned] = useState<{ text: string | null } | null>(null);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const base = pinned ? pinned.text : q.data;
	const value = draft ?? base ?? "";
	const edit = (next: string) => {
		if (!pinned && base !== undefined) setPinned({ text: base });
		setDraft(next);
	};
	const reload = () => {
		setPinned(null);
		setDraft(null);
		setError(null);
	};
	const save = async () => {
		if (base === undefined || busy) return;
		setBusy(true);
		setError(null);
		try {
			await actions.write(path, value, base);
			onClose();
		} catch (e) {
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
						onChange={edit}
						onSave={() => void save()}
					/>
				)}
				{isModifiedError(error) ? (
					<EditConflict onReload={reload} />
				) : (
					<AgentErrorText error={error} />
				)}
			</div>
		</Dialog>
	);
}
