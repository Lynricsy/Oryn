/**
 * artifact 的分享（计划 M14 8.3）：查看器顶栏的「分享 / 已分享」与卡片菜单的「分享…」打开同一份内容。
 * 顶栏是贴着按钮的弹层（`SharePopover`），卡片菜单没有锚点用对话框（`ArtifactShareDialog`）；`compact` 断点下两者都是底部抽屉。
 * - 未发布：三段访问级别（私有 / 访问码 / 公开，各带一句说明）与「发布」；首次发布前提示一次「已发布的页面是静态快照…」
 *   （`localStorage` 键 `oryn.artifacts.firstPublishSeen`）。
 * - 已发布：地址框（默认省略协议与结尾斜杠，聚焦时显示全文并全选）与复制（有访问码时复制「链接 + 换行 + 访问码：xxxx」）；
 *   访问级别切换（立即生效，地址不变）；访问码与「复制访问码」「换一个」；锁定警告；「版本 n，发布于…」；跟随开关；
 *   没开跟随且有新版本时「更新到版本 {head}」；跟随时被密钥扫描挡下的版本（`pendingVersion`）给「查看并发布」；
 *   「打开」（私有与访问码先取授权地址）；「取消发布…」带确认。
 * - 发布为访问码 / 公开（或换到新版本）时服务端先扫描密钥：409 `secrets_found` 时列出命中（与线程分享同一个组件），
 *   勾选「我已检查」后带 `acknowledgeFindings` 重发同一个请求。
 */
import {
	type ReactNode,
	type RefObject,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	type Artifact,
	type ArtifactAccess,
	type ArtifactPublication,
	type ArtifactPublishRequest,
	grantArtifactPublication,
	publicationSlug,
	useArtifactPublication,
	usePublishArtifact,
	useRotateArtifactCode,
	useUnpublishArtifact,
} from "../../api/artifact-queries";
import type { ShareScan } from "../../api/share-queries";
import { copyToClipboard } from "../../lib/clipboard";
import { formatRelative } from "../../lib/format";
import { useBreakpoint } from "../../lib/viewport";
import { showToast } from "../../state/toasts";
import { ScanFindings } from "../share/ScanFindings";
import { CopyButton } from "../ui/CopyButton";
import { Callout } from "../ui/data";
import { Floating } from "../ui/Floating";
import { Icon, type IconName } from "../ui/icons";
import { ManualCopy } from "../ui/ManualCopy";
import {
	Button,
	Dialog,
	describeError,
	ErrorText,
	IconButton,
	Radio,
	Spinner,
	Toggle,
} from "../ui/primitives";

export type ShareArtifact = Pick<Artifact, "id" | "title" | "version">;

/** 首次发布前的提示只显示一次。 */
export const FIRST_PUBLISH_KEY = "oryn.artifacts.firstPublishSeen";

const ACCESS: { value: ArtifactAccess; icon: IconName }[] = [
	{ value: "private", icon: "lock" },
	{ value: "code", icon: "key" },
	{ value: "public", icon: "globe" },
];

function firstPublishSeen(): boolean {
	try {
		return localStorage.getItem(FIRST_PUBLISH_KEY) === "1";
	} catch {
		return true;
	}
}

function AccessChoice({
	value,
	disabled,
	onChange,
}: {
	value: ArtifactAccess;
	disabled: boolean;
	onChange(v: ArtifactAccess): void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<fieldset className="flex flex-col gap-1.5" disabled={disabled}>
			<legend className="mb-1 font-medium text-muted text-xs">
				{t("artifacts.share.accessLabel")}
			</legend>
			{ACCESS.map((a) => (
				// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Radio 组件（内含原生 input）
				<label
					key={a.value}
					data-testid={`share-access-${a.value}`}
					className={`flex cursor-pointer gap-2.5 rounded-lg border px-3 py-2 transition-colors duration-150 ${value === a.value ? "border-accent bg-accent-soft" : "border-border bg-panel/60 hover:bg-hover"}`}
				>
					<Radio
						name="artifact-share-access"
						checked={value === a.value}
						onChange={() => onChange(a.value)}
						className="mt-0.5"
					/>
					<span className="min-w-0">
						<span className="flex items-center gap-1.5 font-medium text-sm">
							<Icon
								name={a.icon}
								size={14}
								aria-hidden
								className="text-faint"
							/>
							{t(`artifacts.share.access.${a.value}`)}
						</span>
						<span className="block text-muted text-xs">
							{t(`artifacts.share.accessHint.${a.value}`)}
						</span>
					</span>
				</label>
			))}
		</fieldset>
	);
}

/** 地址框与「复制链接」：聚焦时显示全文并全选；复制失败时给出手动复制。 */
function UrlRow({ pub }: { pub: ArtifactPublication }): ReactNode {
	const { t } = useTranslation();
	const [focused, setFocused] = useState(false);
	const [copy, setCopy] = useState<"idle" | "copied" | "failed">("idle");
	// 有访问码时第二行是访问码
	const text = pub.code
		? `${pub.url}\n${t("artifacts.share.codeLine", { code: pub.code })}`
		: pub.url;
	useEffect(() => {
		if (copy !== "copied") return;
		const h = setTimeout(() => setCopy("idle"), 1200);
		return () => clearTimeout(h);
	}, [copy]);
	return (
		<div className="flex flex-col gap-1.5">
			<div className="flex items-center gap-1.5">
				<input
					readOnly
					data-testid="share-url"
					aria-label={t("artifacts.share.urlLabel")}
					value={
						focused
							? pub.url
							: // 缺省显示省略协议与结尾斜杠
								pub.url
									.replace(/^[a-z][a-z0-9+.-]*:\/\//i, "")
									.replace(/\/$/, "")
					}
					onFocus={(e) => {
						setFocused(true);
						const el = e.currentTarget;
						requestAnimationFrame(() => el.select());
					}}
					onBlur={() => setFocused(false)}
					className="h-8 min-w-0 flex-1 rounded-md border border-border bg-inset px-2 font-mono text-xs outline-none focus-visible:border-border-strong"
				/>
				<Button
					size="sm"
					icon={copy === "copied" ? "check" : "copy"}
					data-testid="share-copy"
					onClick={() =>
						void copyToClipboard(text).then((ok) =>
							setCopy(ok ? "copied" : "failed"),
						)
					}
				>
					{copy === "copied"
						? t("artifacts.share.copied")
						: t("artifacts.share.copyLink")}
				</Button>
			</div>
			{copy === "failed" && (
				<ManualCopy
					text={text}
					label={t("artifacts.share.urlLabel")}
					message={t("artifacts.share.copyFailed")}
				/>
			)}
		</div>
	);
}

/** 发布弹层与对话框共用的内容。 */
export function SharePanel({
	artifact,
}: {
	artifact: ShareArtifact;
}): ReactNode {
	const { t } = useTranslation();
	const state = useArtifactPublication(artifact.id);
	const publish = usePublishArtifact(artifact.id);
	const rotate = useRotateArtifactCode(artifact.id);
	const unpublish = useUnpublishArtifact();
	const [choice, setChoice] = useState<ArtifactAccess>("private");
	// 被密钥扫描挡下的请求：确认后原样带 acknowledgeFindings 重发
	const [held, setHeld] = useState<{
		scan: ShareScan;
		body: ArtifactPublishRequest;
	} | null>(null);
	const [ack, setAck] = useState(false);
	const [seen, setSeen] = useState(firstPublishSeen);
	const [confirmUnpublish, setConfirmUnpublish] = useState(false);
	const busy = publish.isPending || rotate.isPending || unpublish.isPending;

	const send = async (body: ArtifactPublishRequest) => {
		try {
			const r = await publish.mutateAsync(body);
			setAck(false);
			if (r.kind === "findings") {
				setHeld({ scan: r.scan, body });
				return;
			}
			setHeld(null);
			if (!seen) {
				try {
					localStorage.setItem(FIRST_PUBLISH_KEY, "1");
				} catch {
					// 存不了就下次再提示
				}
				setSeen(true);
			}
		} catch {
			// 错误显示在面板里（`publish.error`）
		}
	};

	if (state.isPending)
		return (
			<div className="flex justify-center py-6">
				<Spinner />
			</div>
		);
	if (state.isError) return <ErrorText error={state.error} />;
	const pub = state.data.publication;

	const findings = held && (
		<div className="flex flex-col gap-2">
			<ScanFindings
				source={{ kind: "files" }}
				scan={held.scan}
				ack={ack}
				onAck={setAck}
				hint={t("artifacts.share.findingsHint")}
			/>
			<div className="flex justify-end gap-2">
				<Button variant="ghost" size="sm" onClick={() => setHeld(null)}>
					{t("common.cancel")}
				</Button>
				<Button
					variant="primary"
					size="sm"
					data-testid="share-publish-anyway"
					disabled={!ack || busy}
					onClick={() => void send({ ...held.body, acknowledgeFindings: true })}
				>
					{t("artifacts.share.publishAnyway")}
				</Button>
			</div>
		</div>
	);

	if (!pub)
		return (
			<div className="flex flex-col gap-3" data-testid="share-panel">
				<p className="text-muted text-sm">{t("artifacts.share.intro")}</p>
				<AccessChoice
					value={choice}
					disabled={busy || held !== null}
					onChange={setChoice}
				/>
				{!seen && (
					<Callout tone="neutral" role="status">
						{t("artifacts.share.firstPublish")}
					</Callout>
				)}
				{findings ?? (
					<div className="flex justify-end">
						<Button
							variant="primary"
							icon="link"
							data-testid="share-publish"
							disabled={busy}
							onClick={() => void send({ access: choice })}
						>
							{t("artifacts.share.publish")}
						</Button>
					</div>
				)}
				<ErrorText error={publish.error} />
			</div>
		);

	const head = artifact.version;
	const open = async () => {
		try {
			const slug = publicationSlug(pub.url);
			const url =
				pub.access === "public" || !slug
					? pub.url
					: await grantArtifactPublication(slug);
			window.open(url, "_blank", "noopener");
		} catch (e) {
			showToast({
				tone: "err",
				text: t("artifacts.share.openFailed", { reason: describeError(e, t) }),
			});
		}
	};
	const doUnpublish = async () => {
		try {
			await unpublish.mutateAsync(artifact.id);
			setConfirmUnpublish(false);
			showToast({
				text: t("artifacts.share.unpublished", { title: artifact.title }),
			});
		} catch {
			// 错误显示在面板里
		}
	};

	return (
		<div className="flex flex-col gap-3" data-testid="share-panel">
			<UrlRow pub={pub} />
			<AccessChoice
				value={pub.access}
				disabled={busy || held !== null}
				onChange={(access) => {
					if (access !== pub.access) void send({ access });
				}}
			/>
			{pub.access === "code" && pub.code && (
				<div className="flex flex-col gap-1">
					<div className="flex items-center gap-2">
						<span className="text-muted text-xs">
							{t("artifacts.share.codeLabel")}
						</span>
						<code
							data-testid="share-code"
							className="rounded-sm bg-inset px-1.5 font-mono text-sm tracking-widest"
						>
							{pub.code}
						</code>
						<CopyButton text={pub.code} label={t("artifacts.share.copyCode")} />
						<span className="flex-1" />
						<Button
							size="sm"
							variant="ghost"
							icon="refresh"
							data-testid="share-rotate"
							disabled={busy}
							title={t("artifacts.share.rotateHint")}
							onClick={() => rotate.mutate()}
						>
							{t("artifacts.share.rotate")}
						</Button>
					</div>
					<p className="text-faint text-xs">
						{t("artifacts.share.rotateHint")}
					</p>
				</div>
			)}
			{pub.access === "code" && pub.codeLocked && (
				<Callout tone="warn" role="alert">
					<span data-testid="share-locked">{t("artifacts.share.locked")}</span>
				</Callout>
			)}
			<p className="text-muted text-xs" data-testid="share-version">
				{t("artifacts.share.published", {
					version: pub.version,
					when: formatRelative(pub.updatedAt),
				})}
			</p>
			<Toggle
				checked={pub.follow}
				disabled={busy || held !== null}
				label={t("artifacts.share.follow")}
				onChange={(follow) => void send({ access: pub.access, follow })}
			/>
			{!pub.follow && head > pub.version && (
				<div>
					<Button
						size="sm"
						icon="refresh"
						data-testid="share-update"
						disabled={busy || held !== null}
						onClick={() => void send({ access: pub.access, version: head })}
					>
						{t("artifacts.share.update", { head })}
					</Button>
				</div>
			)}
			{pub.pendingVersion !== undefined && held === null && (
				<Callout
					tone="warn"
					role="status"
					actions={
						<Button
							size="sm"
							data-testid="share-review"
							disabled={busy}
							onClick={() =>
								void send({
									access: pub.access,
									version: pub.pendingVersion,
								})
							}
						>
							{t("artifacts.share.review")}
						</Button>
					}
				>
					{t("artifacts.share.pending", { version: pub.pendingVersion })}
				</Callout>
			)}
			{findings}
			<ErrorText error={publish.error ?? rotate.error ?? unpublish.error} />
			{confirmUnpublish ? (
				<div
					role="alert"
					className="flex flex-col gap-2 rounded-md border border-border bg-inset p-3"
				>
					<p className="text-sm">{t("artifacts.share.unpublishBody")}</p>
					<div className="flex justify-end gap-2">
						<Button
							size="sm"
							variant="ghost"
							onClick={() => setConfirmUnpublish(false)}
						>
							{t("common.cancel")}
						</Button>
						<Button
							size="sm"
							variant="danger"
							data-testid="share-unpublish-confirm"
							disabled={busy}
							onClick={() => void doUnpublish()}
						>
							{t("artifacts.share.unpublishConfirm")}
						</Button>
					</div>
				</div>
			) : (
				<div className="flex items-center gap-2">
					<Button
						size="sm"
						icon="external"
						data-testid="share-open"
						onClick={() => void open()}
					>
						{t("artifacts.share.open")}
					</Button>
					<span className="flex-1" />
					<Button
						size="sm"
						variant="ghost"
						data-testid="share-unpublish"
						disabled={busy}
						onClick={() => setConfirmUnpublish(true)}
					>
						{t("artifacts.share.unpublish")}
					</Button>
				</div>
			)}
		</div>
	);
}

/** 卡片菜单的「分享…」：对话框（compact 下是底部抽屉）。 */
export function ArtifactShareDialog({
	artifact,
	onClose,
}: {
	artifact: ShareArtifact;
	onClose(): void;
}): ReactNode {
	const { t } = useTranslation();
	return (
		<Dialog
			open
			onClose={onClose}
			title={t("artifacts.share.title", { title: artifact.title })}
		>
			<SharePanel artifact={artifact} />
		</Dialog>
	);
}

/**
 * 查看器顶栏的分享弹层：顶层浮层（ui/Floating）贴在 `anchor` 下方右对齐，不被面板裁切；外壳不滚动、内容在内层滚动。
 * 点外面或 `Esc` 关闭，关闭后焦点回到按钮。compact 断点下换成底部抽屉。
 */
export function SharePopover({
	artifact,
	anchor,
	onClose,
}: {
	artifact: ShareArtifact;
	anchor: RefObject<HTMLElement | null>;
	onClose(): void;
}): ReactNode {
	const { t } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const ref = useRef<HTMLDivElement>(null);
	const onCloseRef = useRef(onClose);
	onCloseRef.current = onClose;

	useEffect(() => {
		if (compact) return;
		const onDown = (e: MouseEvent) => {
			const target = e.target as Node;
			if (ref.current?.contains(target) || anchor.current?.contains(target))
				return;
			// 弹层里的对话框（如有）不算点外面
			if ((target as Element).closest?.("[role=dialog]")) return;
			onCloseRef.current();
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key !== "Escape") return;
			e.stopPropagation();
			onCloseRef.current();
			anchor.current?.querySelector<HTMLElement>("button")?.focus();
		};
		document.addEventListener("mousedown", onDown);
		document.addEventListener("keydown", onKey, true);
		return () => {
			document.removeEventListener("mousedown", onDown);
			document.removeEventListener("keydown", onKey, true);
		};
	}, [anchor, compact]);

	if (compact)
		return <ArtifactShareDialog artifact={artifact} onClose={onClose} />;
	const title = t("artifacts.share.title", { title: artifact.title });
	return (
		<Floating
			ref={ref}
			anchor={anchor}
			align="end"
			gap={6}
			maxHeight={720}
			role="dialog"
			aria-label={title}
			data-testid="share-popover"
			className="w-[22rem] max-w-[calc(100vw-1rem)]"
			bodyClassName="p-4"
		>
			<h2 className="mb-3 truncate font-semibold text-sm">{title}</h2>
			<SharePanel artifact={artifact} />
		</Floating>
	);
}

/**
 * 查看器顶栏的分享入口：访客数（大于 0 时「{n} 位访客」，悬停说明「在看分享链接，只读」）与「分享 / 已分享」按钮，
 * 按钮打开 `SharePopover`。访客数取发布状态，观看期间随 `artifact.activity` 更新。
 * 顶栏放不下文字时（`compact`：手机，或查看器本身很窄，由查看器判断）访客数只留眼睛与数字，按钮只留图标
 * （文案作读屏名与悬停提示）。
 */
export function ShareControl({
	artifact,
	compact = false,
}: {
	artifact: ShareArtifact & Partial<Pick<Artifact, "published">>;
	compact?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const anchor = useRef<HTMLSpanElement>(null);
	const published = artifact.published !== undefined;
	const state = useArtifactPublication(artifact.id, published);
	const visitors = published ? (state.data?.publication?.visitors ?? 0) : 0;
	const label = published
		? t("artifacts.share.buttonShared")
		: t("artifacts.share.button");
	const visitorsText = t("artifacts.share.visitors", { count: visitors });
	return (
		<>
			{visitors > 0 && (
				<span
					data-testid="artifact-visitors"
					title={
						compact
							? `${visitorsText} · ${t("artifacts.share.visitorsHint")}`
							: t("artifacts.share.visitorsHint")
					}
					className="inline-flex shrink-0 items-center gap-1 px-1 text-muted text-xs tabular-nums"
				>
					<Icon name="eye" size={13} aria-hidden />
					{compact ? (
						<>
							<span aria-hidden>{visitors}</span>
							<span className="sr-only">{visitorsText}</span>
						</>
					) : (
						visitorsText
					)}
				</span>
			)}
			<span ref={anchor} className="inline-flex shrink-0">
				{compact ? (
					<IconButton
						icon={published ? "globe" : "share"}
						box="sm"
						size={15}
						label={label}
						data-testid="artifact-share"
						aria-expanded={open}
						aria-haspopup="dialog"
						onClick={() => setOpen((o) => !o)}
					/>
				) : (
					<Button
						size="sm"
						variant="ghost"
						icon={published ? "globe" : "share"}
						data-testid="artifact-share"
						aria-expanded={open}
						aria-haspopup="dialog"
						onClick={() => setOpen((o) => !o)}
					>
						{label}
					</Button>
				)}
			</span>
			{open && (
				<SharePopover
					artifact={artifact}
					anchor={anchor}
					onClose={() => setOpen(false)}
				/>
			)}
		</>
	);
}
