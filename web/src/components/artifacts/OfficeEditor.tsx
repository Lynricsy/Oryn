/**
 * Office 编辑器（计划 M32 步骤 12）：在查看器里用 WOPI 引擎（Collabora）编辑 Word / Excel / PowerPoint artifact。
 *
 * 挂载时建编辑会话（`POST …/office-sessions`），用隐藏表单把 `access_token` 与 `access_token_ttl` POST 到引擎的
 * `actionUrl`，目标是同名 iframe；每 `OFFICE_BEAT_MS` 发一次心跳。引擎的 postMessage 是 JSON 字符串，
 * 只接受来源等于引擎源（`actionUrl` 的 origin）且发自这个 iframe 的消息：`App_LoadingStatus` 的 `Frame_Ready`
 * 回 `Host_PostmessageReady`，`Document_Loaded` 撤掉加载遮罩；`UI_Close` 等同「完成」。
 *
 * 「完成」：文档加载过就先发 `Action_Save`（不结束编辑、未修改不保存、要回执），最多等 `OFFICE_SAVE_WAIT_MS`
 * 的 `Action_Save_Resp`：明确 `success: true` 才关闭会话（`DELETE`）、让 artifact 查询失效并回到查看器；
 * 回执是失败（含缺字段）时不关闭，编辑器留着并提示原因，「完成」可再试；等不到回执照常关闭并提示保存未确认。
 * 卸载时同样先发 `Action_Save`，在下一个宏任务里关闭（StrictMode 的模拟卸载随即重新挂载，关闭被取消，会话照用）：
 * iframe 随之移除，引擎做一次退出保存，服务端在关闭宽限期内仍接受它。
 * 自动保存只更新工作副本；显式保存、退出保存与会话过期时才由服务端落用户版本。
 */
import { useQueryClient } from "@tanstack/react-query";
import {
	type ReactNode,
	useEffect,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import {
	type ArtifactOfficeSession,
	artifactQk,
	beatArtifactOfficeSession,
	closeArtifactOfficeSession,
	openArtifactOfficeSession,
} from "../../api/artifact-queries";
import { showToast } from "../../state/toasts";
import { Icon } from "../ui/icons";
import { Button, describeError, EmptyState, Spinner } from "../ui/primitives";

/** 心跳间隔：服务端在最后一次活动 30 分钟后放开占用。 */
export const OFFICE_BEAT_MS = 60_000;
/** 「完成」时等 `Action_Save_Resp` 的上限；超时照常关闭会话。 */
export const OFFICE_SAVE_WAIT_MS = 10_000;
/** 保存失败提示里引擎原因的上限（码元）。 */
const MAX_REASON_CHARS = 200;

/** 一次「完成」等保存回执的结果；`gone` = 等待期间编辑器已卸载。 */
type SaveOutcome =
	| { kind: "saved" }
	| { kind: "failed"; reason: string }
	| { kind: "timeout" }
	| { kind: "gone" };

/** 能在引擎里编辑的类型（PDF 只读）。 */
const EDITABLE: Readonly<Record<string, true>> = {
	docx: true,
	docm: true,
	xlsx: true,
	xlsm: true,
	pptx: true,
	pptm: true,
};

export function isOfficeEditable(type: string | undefined): boolean {
	return type !== undefined && Object.hasOwn(EDITABLE, type);
}

/** 显式保存：不结束编辑、未修改时不保存、保存后回 `Action_Save_Resp`。 */
const SAVE_VALUES = {
	DontTerminateEdit: true,
	DontSaveIfUnmodified: true,
	Notify: true,
} as const;

export interface EngineMessage {
	id: string;
	values: Readonly<Record<string, unknown>>;
}

/** 解析引擎发来的 postMessage（JSON 字符串 `{MessageId, Values}`）；不合形状的回 `null`。 */
export function parseEngineMessage(data: unknown): EngineMessage | null {
	if (typeof data !== "string") return null;
	let v: unknown;
	try {
		v = JSON.parse(data);
	} catch {
		return null;
	}
	if (v === null || typeof v !== "object" || Array.isArray(v)) return null;
	const { MessageId, Values } = v as Record<string, unknown>;
	if (typeof MessageId !== "string") return null;
	return {
		id: MessageId,
		values:
			Values !== null && typeof Values === "object" && !Array.isArray(Values)
				? (Values as Record<string, unknown>)
				: {},
	};
}

/** 向引擎帧发一条消息（只发往引擎源）。 */
function postToEngine(
	frame: HTMLIFrameElement | null,
	origin: string,
	id: string,
	values?: Readonly<Record<string, unknown>>,
): void {
	frame?.contentWindow?.postMessage(
		JSON.stringify({
			MessageId: id,
			SendTime: Date.now(),
			...(values ? { Values: values } : {}),
		}),
		origin,
	);
}

export interface OfficeEditorProps {
	artifactId: string;
	title: string;
	/** 编辑结束（已保存并关闭会话）或打不开时返回查看器。 */
	onClose(): void;
}

export function OfficeEditor({
	artifactId,
	title,
	onClose,
}: OfficeEditorProps): ReactNode {
	const [attempt, setAttempt] = useState(0);
	return (
		<OfficeSession
			key={attempt}
			artifactId={artifactId}
			title={title}
			onClose={onClose}
			onRetry={() => setAttempt((n) => n + 1)}
		/>
	);
}

type Acquire =
	| { state: "pending" }
	| { state: "ready"; session: ArtifactOfficeSession; origin: string }
	| { state: "error"; error: unknown };

/** 一次会话申请：重试 = 换 key 重建。 */
function OfficeSession({
	artifactId,
	title,
	onClose,
	onRetry,
}: OfficeEditorProps & { onRetry(): void }): ReactNode {
	const { t, i18n } = useTranslation();
	const [acq, setAcq] = useState<Acquire>({ state: "pending" });
	// 编辑中切换界面语言不重建会话
	const localeRef = useRef(i18n.language);

	useEffect(() => {
		let live = true;
		openArtifactOfficeSession(artifactId, localeRef.current).then(
			(session) => {
				if (!live) {
					void closeArtifactOfficeSession(session.sessionId);
					return;
				}
				let origin: string;
				try {
					origin = new URL(session.actionUrl).origin;
				} catch (error) {
					void closeArtifactOfficeSession(session.sessionId);
					setAcq({ state: "error", error });
					return;
				}
				setAcq({ state: "ready", session, origin });
			},
			(error: unknown) => {
				if (live) setAcq({ state: "error", error });
			},
		);
		return () => {
			live = false;
		};
	}, [artifactId]);

	if (acq.state === "ready")
		return (
			<LiveEditor
				session={acq.session}
				origin={acq.origin}
				title={title}
				onClose={onClose}
			/>
		);
	if (acq.state === "error")
		return (
			<div data-testid="office-editor-error" className="h-full">
				<EmptyState icon="alert" title={t("artifacts.office.openFailed")}>
					<div className="flex flex-col items-center gap-3">
						<span className="break-words">{describeError(acq.error, t)}</span>
						<div className="flex items-center gap-2">
							<Button size="sm" variant="ghost" icon="back" onClick={onClose}>
								{t("artifacts.office.back")}
							</Button>
							<Button size="sm" icon="refresh" onClick={onRetry}>
								{t("artifacts.viewer.retry")}
							</Button>
						</div>
					</div>
				</EmptyState>
			</div>
		);
	return (
		<div
			data-testid="office-editor-opening"
			role="status"
			className="flex h-full items-center justify-center gap-2 text-faint text-sm"
		>
			<Spinner size={16} />
			{t("artifacts.office.opening")}
		</div>
	);
}

function LiveEditor({
	session,
	origin,
	title,
	onClose,
}: {
	session: ArtifactOfficeSession;
	origin: string;
	title: string;
	onClose(): void;
}): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const frameRef = useRef<HTMLIFrameElement>(null);
	const formRef = useRef<HTMLFormElement>(null);
	const [loaded, setLoaded] = useState(false);
	const [saving, setSaving] = useState(false);
	// 引擎回了保存失败：编辑器留着，提示原因，「完成」可再试
	const [saveFailed, setSaveFailed] = useState<string | null>(null);
	// 文档加载过才发 Action_Save（没加载完的帧不会回执）
	const loadedRef = useRef(false);
	// 会话已关闭（「完成」或卸载，只做一次）
	const closedRef = useRef(false);
	// 卸载清理排下的关闭：同一轮里又挂载回来（StrictMode 的模拟卸载）就取消，会话照用
	const pendingCloseRef = useRef<number | null>(null);
	// 表单只提交一次（StrictMode 下 effect 会重跑）
	const submittedRef = useRef(false);
	// 正在等 Action_Save_Resp：调用即结束等待
	const saveWaitRef = useRef<((outcome: SaveOutcome) => void) | null>(null);
	const frameName = `oryn-office-${session.sessionId}`;
	const { sessionId } = session;

	const finish = async () => {
		if (closedRef.current || saveWaitRef.current) return;
		setSaving(true);
		setSaveFailed(null);
		if (loadedRef.current) {
			const outcome = await new Promise<SaveOutcome>((resolve) => {
				const done = (o: SaveOutcome) => {
					clearTimeout(timer);
					saveWaitRef.current = null;
					resolve(o);
				};
				const timer = setTimeout(
					() => done({ kind: "timeout" }),
					OFFICE_SAVE_WAIT_MS,
				);
				saveWaitRef.current = done;
				postToEngine(frameRef.current, origin, "Action_Save", SAVE_VALUES);
			});
			// 等待期间已卸载：卸载时已经关过
			if (outcome.kind === "gone") return;
			if (outcome.kind === "failed") {
				setSaving(false);
				setSaveFailed(
					outcome.reason || t("artifacts.office.saveFailedGeneric"),
				);
				return;
			}
			// 没等到回执：照常关闭，iframe 移除时引擎做退出保存
			if (outcome.kind === "timeout")
				showToast({ text: t("artifacts.office.saveUnconfirmed") });
		}
		if (closedRef.current) return;
		closedRef.current = true;
		await closeArtifactOfficeSession(sessionId);
		void qc.invalidateQueries({ queryKey: artifactQk.all });
		onClose();
	};
	const finishRef = useRef(finish);
	finishRef.current = finish;

	// 表单 POST 到引擎帧（令牌不进地址栏）
	useEffect(() => {
		if (submittedRef.current) return;
		submittedRef.current = true;
		formRef.current?.submit();
	}, []);

	// 布局阶段的清理在 iframe 移除之前运行：卸载时还能把 Action_Save 发进去。
	// 关闭会话推到下一个宏任务：StrictMode 的模拟卸载会在同一轮里重新挂载并取消它
	useLayoutEffect(() => {
		if (pendingCloseRef.current !== null) {
			clearTimeout(pendingCloseRef.current);
			pendingCloseRef.current = null;
		}
		const frame = frameRef.current;
		const onMessage = (e: MessageEvent) => {
			if (e.origin !== origin || !frame || e.source !== frame.contentWindow)
				return;
			const msg = parseEngineMessage(e.data);
			if (!msg) return;
			switch (msg.id) {
				case "App_LoadingStatus":
					if (msg.values.Status === "Frame_Ready")
						postToEngine(frame, origin, "Host_PostmessageReady");
					else if (msg.values.Status === "Document_Loaded") {
						loadedRef.current = true;
						setLoaded(true);
					}
					break;
				case "Action_Save_Resp":
					// 只有明确的 success: true 算保存成功；其余（含缺字段）都按失败
					saveWaitRef.current?.(
						msg.values.success === true
							? { kind: "saved" }
							: {
									kind: "failed",
									reason:
										typeof msg.values.errorMsg === "string"
											? msg.values.errorMsg.trim().slice(0, MAX_REASON_CHARS)
											: "",
								},
					);
					break;
				case "UI_Close":
					void finishRef.current();
					break;
			}
		};
		window.addEventListener("message", onMessage);
		const beat = setInterval(() => {
			// 失败无需处理：会话失效时引擎的 WOPI 请求同样被拒，服务端清扫会话与占用
			beatArtifactOfficeSession(sessionId).catch(() => undefined);
		}, OFFICE_BEAT_MS);
		return () => {
			window.removeEventListener("message", onMessage);
			clearInterval(beat);
			if (closedRef.current) return;
			if (loadedRef.current)
				postToEngine(frame, origin, "Action_Save", SAVE_VALUES);
			pendingCloseRef.current = window.setTimeout(() => {
				pendingCloseRef.current = null;
				// 真的卸载了：结束「完成」的等待，由这里关闭
				saveWaitRef.current?.({ kind: "gone" });
				if (closedRef.current) return;
				closedRef.current = true;
				void closeArtifactOfficeSession(sessionId).then(() =>
					qc.invalidateQueries({ queryKey: artifactQk.all }),
				);
			}, 0);
		};
	}, [sessionId, origin, qc]);

	return (
		<div data-testid="office-editor" className="flex h-full min-h-0 flex-col">
			<div className="flex shrink-0 items-center gap-2 pb-2 text-muted text-xs">
				<Icon name="edit" size={13} className="shrink-0" />
				<span className="min-w-0 flex-1 truncate">
					{t("artifacts.office.hint")}
				</span>
				<Button
					size="sm"
					variant="primary"
					icon="check"
					data-testid="office-editor-done"
					disabled={saving}
					onClick={() => void finish()}
				>
					{saving ? t("artifacts.office.saving") : t("artifacts.office.done")}
				</Button>
			</div>
			{saveFailed !== null && (
				<div
					role="alert"
					data-testid="office-editor-save-failed"
					className="mb-2 flex shrink-0 items-center gap-2 rounded-lg border border-err/30 bg-err/5 px-3 py-1.5 text-err text-sm"
				>
					<Icon name="alert" size={14} className="shrink-0" />
					<span className="min-w-0 flex-1 break-words">
						{t("artifacts.office.saveFailed", { reason: saveFailed })}
					</span>
				</div>
			)}
			<div className="relative min-h-0 flex-1 overflow-hidden rounded-lg border border-border bg-inset">
				<form
					ref={formRef}
					action={session.actionUrl}
					method="post"
					target={frameName}
					className="hidden"
				>
					<input
						type="hidden"
						name="access_token"
						value={session.accessToken}
					/>
					<input
						type="hidden"
						name="access_token_ttl"
						value={String(session.accessTokenTtl)}
					/>
				</form>
				<iframe
					ref={frameRef}
					name={frameName}
					data-testid="office-editor-frame"
					title={t("artifacts.office.frameTitle", { title })}
					allow="clipboard-read; clipboard-write"
					className="block h-full w-full border-0 bg-white"
				/>
				{!loaded && (
					<div
						data-testid="office-editor-loading"
						role="status"
						className="absolute inset-0 flex items-center justify-center gap-2 bg-inset text-faint text-sm"
					>
						<Spinner size={16} />
						{t("artifacts.office.opening")}
					</div>
				)}
			</div>
		</div>
	);
}
