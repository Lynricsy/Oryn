/**
 * 首页 = 新建线程框（居中卡片，按最新 Amp）：提示词 + 附件（图片另带内联副本）；底部胶囊——执行位置（Ctrl+M）、项目（Ctrl+J）、
 * 模型与思考级别（Ctrl+S）、发送。省略 provider / 类型时由控制面依次取项目默认、orb_defaults、provider 默认。
 */
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { Schemas } from "../api/client";
import { useOrbs, useProjects, useProviders } from "../api/queries";
import {
	AttachmentStrip,
	useAttachments,
} from "../components/composer/attachments";
import { Icon, type IconName } from "../components/ui/icons";
import {
	IconButton,
	Kbd,
	Spinner,
	TextInput,
} from "../components/ui/primitives";
import { useBreakpoint } from "../lib/viewport";
import { createThread } from "../state/actions";
import { useIndex } from "../state/stores";
import { uiStore } from "../state/ui";

type Pill = "executor" | "project" | "model";

const THINKING_LEVELS = [
	"auto",
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
] as const;

type Executor = Schemas["ThreadExecutor"];

function PillButton({
	icon,
	label,
	shortcut,
	onClick,
	active,
}: {
	icon: IconName;
	label: ReactNode;
	shortcut: string;
	onClick: () => void;
	active: boolean;
}) {
	return (
		<button
			type="button"
			onClick={onClick}
			aria-expanded={active}
			className={`inline-flex h-7 max-w-56 items-center gap-1.5 rounded-full border px-2.5 text-xs transition-colors ${active ? "border-accent/60 bg-hover text-fg" : "border-border text-muted hover:text-fg"}`}
		>
			<Icon name={icon} size={12} />
			<span className="truncate">{label}</span>
			<span className="hidden sm:inline">
				<Kbd>{shortcut}</Kbd>
			</span>
		</button>
	);
}

function Popover({
	children,
	onClose,
}: {
	children: ReactNode;
	onClose: () => void;
}) {
	const ref = useRef<HTMLDivElement>(null);
	useEffect(() => {
		const onDown = (e: MouseEvent) => {
			if (ref.current && !ref.current.contains(e.target as Node)) onClose();
		};
		document.addEventListener("mousedown", onDown);
		return () => document.removeEventListener("mousedown", onDown);
	}, [onClose]);
	return (
		<div
			ref={ref}
			className="absolute bottom-full left-0 z-30 mb-2 max-h-80 w-72 overflow-y-auto rounded-lg border border-border bg-panel p-1 shadow-lg"
		>
			{children}
		</div>
	);
}

function Option({
	selected,
	onClick,
	children,
	hint,
}: {
	selected: boolean;
	onClick: () => void;
	children: ReactNode;
	hint?: ReactNode;
}) {
	return (
		<button
			type="button"
			onClick={onClick}
			className={`flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-hover ${selected ? "text-accent" : ""}`}
		>
			<span className="min-w-0 flex-1 truncate">{children}</span>
			{hint && <span className="shrink-0 text-faint text-xs">{hint}</span>}
			{selected && <Icon name="check" size={13} />}
		</button>
	);
}

export function HomePage(): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const bp = useBreakpoint();
	const providers = useProviders();
	const projects = useProjects();
	const orbs = useOrbs();
	const threads = useIndex((s) => s.threads);
	const att = useAttachments();
	const [prompt, setPrompt] = useState("");
	const [pill, setPill] = useState<Pill | null>(null);
	const [executor, setExecutor] = useState<Executor>({ kind: "new_orb" });
	const [projectId, setProjectId] = useState<string | undefined>(undefined);
	const [model, setModel] = useState("");
	const [thinking, setThinking] = useState<string>("");
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const ref = useRef<HTMLTextAreaElement>(null);

	useEffect(() => {
		ref.current?.focus();
	}, []);

	useEffect(() => {
		const onKey = (e: KeyboardEvent) => {
			if (!(e.ctrlKey || e.metaKey)) return;
			const k = e.key.toLowerCase();
			const map: Record<string, Pill> = {
				m: "executor",
				j: "project",
				s: "model",
			};
			const p = map[k];
			if (p) {
				e.preventDefault();
				setPill((cur) => (cur === p ? null : p));
			}
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, []);

	const recentModels = useMemo(() => {
		const set = new Set<string>();
		for (const th of Object.values(threads))
			if (th.model) set.add(`${th.model.provider}/${th.model.id}`);
		return [...set].slice(0, 8);
	}, [threads]);

	const liveOrbs = (orbs.data?.orbs ?? []).filter(
		(o) =>
			o.state !== "deleted" && o.state !== "deleting" && o.state !== "failed",
	);
	const executorLabel =
		executor.kind === "orb"
			? t("newThread.existingOrb", { id: executor.orbId.slice(0, 12) })
			: executor.provider
				? `${executor.provider}${executor.type ? ` · ${executor.type}` : ""}`
				: t("newThread.newOrbDefault");
	const projectLabel = projectId
		? (projects.data?.projects.find((p) => p.id === projectId)?.name ??
			projectId)
		: t("newThread.noProject");
	const modelLabel = model
		? `${model}${thinking ? ` · ${thinking}` : ""}`
		: thinking
			? thinking
			: t("newThread.defaultModel");

	const submit = async () => {
		const text = prompt.trim();
		if (!text && att.attachmentIds.length === 0) return;
		if (att.uploading) {
			setError(t("attachments.waitUpload"));
			return;
		}
		setBusy(true);
		setError(null);
		try {
			const slash = model.indexOf("/");
			const body: Schemas["CreateThreadRequest"] = {
				prompt: text,
				executor,
				...(projectId ? { projectId } : {}),
				...(att.contents.length > 0 ? { images: att.contents } : {}),
				...(att.attachmentIds.length > 0
					? { attachments: att.attachmentIds }
					: {}),
				...(slash > 0
					? {
							model: {
								provider: model.slice(0, slash),
								id: model.slice(slash + 1),
							},
						}
					: {}),
				...(thinking ? { thinkingLevel: thinking } : {}),
			};
			const thread = await createThread(body);
			setPrompt("");
			att.clear();
			navigate(`/t/${thread.id}`);
		} catch (e) {
			setError(e instanceof Error ? e.message : String(e));
		} finally {
			setBusy(false);
		}
	};

	return (
		<div className="flex h-full flex-col">
			{bp !== "expanded" && (
				<header className="flex h-12 shrink-0 items-center gap-2 px-2">
					<IconButton
						icon="menu"
						label={t("nav.sidebar")}
						onClick={() =>
							uiStore.getState().setSidebar(!uiStore.getState().sidebarOpen)
						}
					/>
					<span className="font-semibold text-sm">{t("app.name")}</span>
				</header>
			)}
			<div className="flex flex-1 items-start justify-center overflow-y-auto px-3 pt-[12vh] pb-keyboard sm:items-center sm:pt-0">
				<div className="w-full max-w-2xl">
					<h1 className="mb-4 text-center font-semibold text-muted text-xl tracking-tight">
						{t("newThread.heading")}
					</h1>
					<div className="relative rounded-2xl border border-border bg-panel shadow-sm focus-within:border-accent/60">
						<AttachmentStrip items={att.items} onRemove={att.remove} />
						<textarea
							onDragOver={(e) => {
								if (e.dataTransfer.types.includes("Files")) e.preventDefault();
							}}
							onDrop={(e) => {
								if (e.dataTransfer.files.length === 0) return;
								e.preventDefault();
								void att.addFiles(e.dataTransfer.files);
							}}
							ref={ref}
							rows={4}
							value={prompt}
							aria-label={t("newThread.prompt")}
							placeholder={t("newThread.placeholder")}
							className="block w-full resize-none bg-transparent px-4 pt-3.5 pb-2 text-sm outline-none placeholder:text-faint"
							onChange={(e) => setPrompt(e.target.value)}
							onPaste={(e) => {
								const files = [...e.clipboardData.files];
								if (files.length > 0) {
									e.preventDefault();
									void att.addFiles(files);
								}
							}}
							onKeyDown={(e) => {
								if (
									e.key === "Enter" &&
									!e.shiftKey &&
									!e.nativeEvent.isComposing
								) {
									e.preventDefault();
									void submit();
								}
							}}
						/>
						<div className="relative flex flex-wrap items-center gap-1.5 px-3 pb-3">
							<PillButton
								icon="box"
								label={executorLabel}
								shortcut="Ctrl+M"
								active={pill === "executor"}
								onClick={() => setPill(pill === "executor" ? null : "executor")}
							/>
							<PillButton
								icon="folder"
								label={projectLabel}
								shortcut="Ctrl+J"
								active={pill === "project"}
								onClick={() => setPill(pill === "project" ? null : "project")}
							/>
							<PillButton
								icon="sparkle"
								label={modelLabel}
								shortcut="Ctrl+S"
								active={pill === "model"}
								onClick={() => setPill(pill === "model" ? null : "model")}
							/>
							<label className="inline-flex h-7 w-7 cursor-pointer items-center justify-center rounded-full text-muted hover:bg-hover hover:text-fg">
								<Icon name="file" size={14} />
								<span className="sr-only">{t("attachments.attach")}</span>
								<input
									type="file"
									multiple
									className="hidden"
									onChange={(e) => {
										if (e.target.files) void att.addFiles(e.target.files);
										e.target.value = "";
									}}
								/>
							</label>
							<div className="flex-1" />
							<button
								type="button"
								disabled={
									busy ||
									att.uploading ||
									(!prompt.trim() && att.attachmentIds.length === 0)
								}
								onClick={() => void submit()}
								aria-label={t("composer.send")}
								className="inline-flex h-8 items-center gap-1.5 rounded-lg bg-accent px-3 text-accent-fg text-sm disabled:opacity-40"
							>
								{busy ? <Spinner size={13} /> : <Icon name="send" size={14} />}
								<span>{t("newThread.start")}</span>
							</button>
							{pill === "executor" && (
								<Popover onClose={() => setPill(null)}>
									<div className="px-2 py-1 text-2xs text-faint uppercase">
										{t("newThread.newOrb")}
									</div>
									<Option
										selected={executor.kind === "new_orb" && !executor.provider}
										onClick={() => {
											setExecutor({ kind: "new_orb" });
											setPill(null);
										}}
									>
										{t("newThread.newOrbDefault")}
									</Option>
									{(providers.data?.providers ?? []).map((p) =>
										p.orbTypes.map((ot) => (
											<Option
												key={`${p.id}-${ot.name}`}
												selected={
													executor.kind === "new_orb" &&
													executor.provider === p.name &&
													executor.type === ot.name
												}
												hint={`${ot.cpus}C/${Math.round(ot.memoryMB / 1024)}G${p.online ? "" : ` · ${t("providers.offline")}`}`}
												onClick={() => {
													setExecutor({
														kind: "new_orb",
														provider: p.name,
														type: ot.name,
													});
													setPill(null);
												}}
											>
												{`${p.name} · ${ot.name}`}
											</Option>
										)),
									)}
									{liveOrbs.length > 0 && (
										<div className="px-2 pt-2 pb-1 text-2xs text-faint uppercase">
											{t("newThread.existingOrbs")}
										</div>
									)}
									{liveOrbs.map((o) => (
										<Option
											key={o.id}
											selected={
												executor.kind === "orb" && executor.orbId === o.id
											}
											hint={t(`orbState.${o.state}`)}
											onClick={() => {
												setExecutor({ kind: "orb", orbId: o.id });
												setPill(null);
											}}
										>
											{`${o.id.slice(0, 16)} · ${o.type}`}
										</Option>
									))}
								</Popover>
							)}
							{pill === "project" && (
								<Popover onClose={() => setPill(null)}>
									<Option
										selected={!projectId}
										onClick={() => {
											setProjectId(undefined);
											setPill(null);
										}}
									>
										{t("newThread.noProject")}
									</Option>
									{(projects.data?.projects ?? [])
										.filter((p) => !p.archivedAt)
										.map((p) => (
											<Option
												key={p.id}
												selected={projectId === p.id}
												hint={p.repoUrl?.replace(/^https?:\/\//, "")}
												onClick={() => {
													setProjectId(p.id);
													setPill(null);
												}}
											>
												{p.name}
											</Option>
										))}
								</Popover>
							)}
							{pill === "model" && (
								<Popover onClose={() => setPill(null)}>
									<div className="p-1">
										<TextInput
											autoFocus
											placeholder={t("newThread.modelPlaceholder")}
											value={model}
											onChange={(e) => setModel(e.target.value)}
										/>
									</div>
									<Option selected={!model} onClick={() => setModel("")}>
										{t("newThread.defaultModel")}
									</Option>
									{recentModels.map((m) => (
										<Option
											key={m}
											selected={model === m}
											onClick={() => setModel(m)}
										>
											{m}
										</Option>
									))}
									<div className="px-2 pt-2 pb-1 text-2xs text-faint uppercase">
										{t("model.thinking")}
									</div>
									<div className="flex flex-wrap gap-1 px-2 pb-2">
										{["", ...THINKING_LEVELS].map((l) => (
											<button
												key={l || "default"}
												type="button"
												onClick={() => setThinking(l)}
												className={`rounded border px-1.5 py-0.5 text-xs ${thinking === l ? "border-accent text-accent" : "border-border text-muted"}`}
											>
												{l || t("newThread.defaultThinking")}
											</button>
										))}
									</div>
								</Popover>
							)}
						</div>
					</div>
					{(error || att.error) && (
						<p className="mt-2 text-center text-err text-sm">
							{error ?? att.error}
						</p>
					)}
					<p className="mt-3 text-center text-faint text-xs">
						{t("newThread.hint")}
					</p>
				</div>
			</div>
		</div>
	);
}
