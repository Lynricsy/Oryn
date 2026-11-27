/**
 * 画布样机导出对话框（M29 D1）：选择画框、格式（PNG / PDF）、缩放（1x–3x）、圆角、背景与外观开关，
 * 调用 `GET /api/v1/artifacts/{id}/export?format=mockup-png|mockup-pdf&…` 下载结果并用 toast 报进度与失败。
 */
import { useQuery } from "@tanstack/react-query";
import type { TFunction } from "i18next";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type Artifact,
	fetchArtifactFileBytes,
	useArtifactCapabilities,
} from "../../api/artifact-queries";
import { api, unwrap } from "../../api/client";
import {
	archivedPages,
	CANVAS_FILE,
	parseCanvasDoc,
} from "../../lib/artifacts/canvas-doc";
import { dispositionName, downloadBlob } from "../../lib/artifacts/download";
import { showToast } from "../../state/toasts";
import {
	Button,
	Checkbox,
	Dialog,
	describeError,
	ErrorText,
	Field,
	Radio,
	TextInput,
} from "../ui/primitives";

export type MockupFormat = "mockup-png" | "mockup-pdf";
export type MockupBgMode = "light" | "dark" | "transparent" | "custom";

export interface MockupExportRequest {
	format: MockupFormat;
	/** 省略或空数组表示导出全部画框。 */
	frames?: string[];
	scale: 1 | 2 | 3;
	radius: number;
	border: boolean;
	bg: string;
	shadow: boolean;
	shell: boolean;
	long: boolean;
}

const HEX_COLOR_RE = /^#[0-9a-fA-F]{6}$/;

/** 触发样机导出请求与浏览器下载，并用 toast 报告进度、成功与失败（可重试）。 */
export async function downloadMockup(
	a: Pick<Artifact, "id" | "title" | "version">,
	req: MockupExportRequest,
	t: TFunction,
): Promise<boolean> {
	const id = `artifact-mockup:${a.id}`;
	showToast({
		id,
		busy: true,
		text: t("artifacts.mockup.running", { title: a.title }),
	});
	try {
		const res = await api.GET("/api/v1/artifacts/{id}/export", {
			params: {
				path: { id: a.id },
				query: {
					version: a.version,
					format: req.format,
					...(req.frames && req.frames.length > 0
						? { frames: req.frames.join(",") }
						: {}),
					scale: req.scale,
					radius: req.radius,
					border: req.border,
					bg: req.bg,
					shadow: req.shadow,
					shell: req.shell,
					long: req.long,
				},
			},
			parseAs: "blob",
		});
		const blob = await unwrap(Promise.resolve(res));
		const ext = req.format === "mockup-pdf" ? "pdf" : "png";
		const fallback = `${a.title.replaceAll("/", "_")}-v${a.version}-mockup.${ext}`;
		downloadBlob(
			blob,
			dispositionName(res.response.headers.get("Content-Disposition")) ??
				fallback,
		);
		showToast({ id, text: t("artifacts.mockup.done", { title: a.title }) });
		return true;
	} catch (e) {
		showToast({
			id,
			tone: "err",
			text: t("artifacts.mockup.failed", {
				title: a.title,
				reason: describeError(e, t),
			}),
			action: {
				label: t("artifacts.export.retry"),
				run: () => void downloadMockup(a, req, t),
			},
		});
		return false;
	}
}

export interface MockupDialogProps {
	artifact: Pick<Artifact, "id" | "title" | "version">;
	onClose(): void;
}

export function MockupDialog({
	artifact,
	onClose,
}: MockupDialogProps): ReactNode {
	const { t } = useTranslation();
	const caps = useArtifactCapabilities();
	const docQuery = useQuery({
		queryKey: ["artifacts", "canvas-doc", artifact.id, artifact.version],
		queryFn: async () => {
			const bytes = await fetchArtifactFileBytes(
				artifact.id,
				artifact.version,
				CANVAS_FILE,
			);
			return parseCanvasDoc(new TextDecoder().decode(bytes));
		},
		retry: false,
	});

	const [excluded, setExcluded] = useState<ReadonlySet<string>>(
		() => new Set(),
	);
	const [format, setFormat] = useState<MockupFormat>("mockup-png");
	const [scale, setScale] = useState<1 | 2 | 3>(2);
	const [radius, setRadius] = useState(12);
	const [border, setBorder] = useState(true);
	const [shadow, setShadow] = useState(true);
	const [shell, setShell] = useState(true);
	const [long, setLong] = useState(false);
	const [bgMode, setBgMode] = useState<MockupBgMode>("light");
	const [customBg, setCustomBg] = useState("#f4f4f5");
	const [busy, setBusy] = useState(false);

	// 落选方案的画框不能导出（M34 C6），不列出；全选时不传 frames，后端同样排除它们
	const doc = docQuery.data;
	const archived = doc ? archivedPages(doc) : null;
	const pages = doc?.pages.filter((p) => !archived?.has(p.id)) ?? [];
	const selectedPages = pages.filter((p) => !excluded.has(p.id));
	const allSelected = pages.length > 0 && selectedPages.length === pages.length;
	const someSelected =
		selectedPages.length > 0 && selectedPages.length < pages.length;

	const bgValue = bgMode === "custom" ? customBg.trim() : bgMode;
	const bgValid = bgMode !== "custom" || HEX_COLOR_RE.test(bgValue);
	const renderReady = caps.data?.render !== false;
	const noFramesSelected = docQuery.isSuccess && selectedPages.length === 0;
	const canSubmit =
		!busy && renderReady && bgValid && !docQuery.isPending && !noFramesSelected;

	const toggleFrame = (id: string) => {
		setExcluded((prev) => {
			const next = new Set(prev);
			if (next.has(id)) next.delete(id);
			else next.add(id);
			return next;
		});
	};

	const toggleAll = () => {
		if (allSelected) setExcluded(new Set(pages.map((p) => p.id)));
		else setExcluded(new Set());
	};

	const submit = async () => {
		if (!canSubmit) return;
		setBusy(true);
		try {
			const req: MockupExportRequest = {
				format,
				...(pages.length > 0 && !allSelected
					? { frames: selectedPages.map((p) => p.id) }
					: {}),
				scale,
				radius,
				border,
				bg: bgValue,
				shadow,
				shell,
				long,
			};
			const ok = await downloadMockup(artifact, req, t);
			if (ok) onClose();
		} finally {
			setBusy(false);
		}
	};

	const bgChoices: { key: MockupBgMode; label: string }[] = [
		{ key: "light", label: t("artifacts.mockup.bgLight") },
		{ key: "dark", label: t("artifacts.mockup.bgDark") },
		{ key: "transparent", label: t("artifacts.mockup.bgTransparent") },
		{ key: "custom", label: t("artifacts.mockup.bgCustom") },
	];

	return (
		<Dialog
			open
			onClose={onClose}
			title={t("artifacts.mockup.title")}
			footer={
				<>
					<Button variant="ghost" onClick={onClose} disabled={busy}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						data-testid="mockup-download"
						disabled={!canSubmit}
						onClick={() => void submit()}
					>
						{busy
							? t("artifacts.mockup.downloading")
							: t("artifacts.mockup.download")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-4">
				<fieldset className="flex flex-col gap-1.5 text-sm">
					<legend className="mb-1 font-medium text-muted text-xs">
						{t("artifacts.mockup.frames")}
					</legend>
					{docQuery.isPending ? (
						<p className="text-muted text-xs">
							{t("artifacts.mockup.loadingFrames")}
						</p>
					) : docQuery.isError ? (
						<ErrorText error={docQuery.error} />
					) : pages.length > 0 ? (
						<div className="flex flex-col gap-1.5 rounded-lg border border-border bg-inset/40 p-2.5">
							{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input） */}
							<label className="flex cursor-pointer items-center gap-2 border-border border-b pb-1.5 font-medium text-fg text-xs">
								<Checkbox
									checked={allSelected}
									indeterminate={someSelected}
									onChange={toggleAll}
								/>
								<span>{t("artifacts.mockup.selectAll")}</span>
								<span className="ml-auto font-normal text-muted tabular-nums">
									{selectedPages.length}/{pages.length}
								</span>
							</label>
							<div className="flex max-h-36 flex-col gap-1 overflow-y-auto pt-0.5">
								{pages.map((p) => (
									// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input）
									<label
										key={p.id}
										className="flex cursor-pointer items-center gap-2 rounded px-1 py-0.5 text-fg text-sm hover:bg-hover"
									>
										<Checkbox
											checked={!excluded.has(p.id)}
											onChange={() => toggleFrame(p.id)}
										/>
										<span className="min-w-0 flex-1 truncate">{p.name}</span>
										<span className="shrink-0 font-mono text-muted text-xs tabular-nums">
											{Math.round(p.width)}×{Math.round(p.height)}
										</span>
									</label>
								))}
							</div>
						</div>
					) : null}
					{docQuery.isSuccess && pages.length === 0 && (
						<span className="text-faint text-xs">
							{t("artifacts.mockup.noFrames")}
						</span>
					)}
					{noFramesSelected && (
						<span className="text-err text-xs">
							{t("artifacts.mockup.selectAtLeastOne")}
						</span>
					)}
				</fieldset>

				<div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
					<fieldset className="flex flex-col gap-2 text-sm">
						<legend className="mb-1 font-medium text-muted text-xs">
							{t("artifacts.mockup.format")}
						</legend>
						{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Radio 组件（内含原生 input） */}
						<label className="flex cursor-pointer items-center gap-2 text-fg text-sm">
							<Radio
								name="mockup-format"
								value="mockup-png"
								checked={format === "mockup-png"}
								onChange={() => setFormat("mockup-png")}
							/>
							<span>{t("artifacts.mockup.formatPng")}</span>
						</label>
						{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Radio 组件（内含原生 input） */}
						<label className="flex cursor-pointer items-center gap-2 text-fg text-sm">
							<Radio
								name="mockup-format"
								value="mockup-pdf"
								checked={format === "mockup-pdf"}
								onChange={() => setFormat("mockup-pdf")}
							/>
							<span>{t("artifacts.mockup.formatPdf")}</span>
						</label>
					</fieldset>

					<fieldset className="flex flex-col gap-1.5 text-sm">
						<legend className="mb-1 font-medium text-muted text-xs">
							{t("artifacts.mockup.scale")}
						</legend>
						<div className="flex gap-1.5">
							{([1, 2, 3] as const).map((s) => (
								<Button
									key={s}
									size="sm"
									variant={scale === s ? "primary" : "secondary"}
									aria-pressed={scale === s}
									disabled={format === "mockup-pdf"}
									onClick={() => setScale(s)}
								>
									{s}x
								</Button>
							))}
						</div>
						{format === "mockup-pdf" && (
							<span className="text-faint text-xs">
								{t("artifacts.mockup.scalePdfHint")}
							</span>
						)}
					</fieldset>
				</div>

				<fieldset className="flex flex-col gap-1.5 text-sm">
					<legend className="mb-1 font-medium text-muted text-xs">
						{t("artifacts.mockup.bg")}
					</legend>
					<div className="flex flex-wrap gap-1.5">
						{bgChoices.map((c) => (
							<Button
								key={c.key}
								size="sm"
								variant={bgMode === c.key ? "primary" : "secondary"}
								aria-pressed={bgMode === c.key}
								onClick={() => setBgMode(c.key)}
							>
								{c.label}
							</Button>
						))}
					</div>
					{bgMode === "custom" && (
						<div className="mt-1 flex items-center gap-2">
							<input
								type="color"
								aria-label={t("artifacts.mockup.bgCustom")}
								value={HEX_COLOR_RE.test(customBg) ? customBg : "#f4f4f5"}
								onChange={(e) => setCustomBg(e.target.value)}
								className="h-8 w-10 shrink-0 cursor-pointer rounded border border-border bg-panel p-0.5"
							/>
							<TextInput
								value={customBg}
								aria-invalid={!bgValid}
								placeholder="#f4f4f5"
								onChange={(e) => setCustomBg(e.target.value)}
							/>
						</div>
					)}
					{!bgValid && (
						<span className="text-err text-xs">
							{t("artifacts.mockup.bgCustomInvalid")}
						</span>
					)}
				</fieldset>

				<Field label={`${t("artifacts.mockup.radius")} (${radius} px)`}>
					<input
						type="range"
						aria-label={t("artifacts.mockup.radius")}
						min={0}
						max={64}
						step={1}
						value={radius}
						onChange={(e) => setRadius(Number(e.target.value))}
						className="w-full accent-ink"
					/>
				</Field>

				<fieldset className="flex flex-col gap-2 text-sm">
					<legend className="mb-1 font-medium text-muted text-xs">
						{t("artifacts.mockup.options")}
					</legend>
					{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input） */}
					<label className="flex cursor-pointer items-center gap-2 text-fg text-sm">
						<Checkbox
							checked={border}
							onChange={(e) => setBorder(e.target.checked)}
						/>
						<span>{t("artifacts.mockup.border")}</span>
					</label>
					{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input） */}
					<label className="flex cursor-pointer items-center gap-2 text-fg text-sm">
						<Checkbox
							checked={shadow}
							onChange={(e) => setShadow(e.target.checked)}
						/>
						<span>{t("artifacts.mockup.shadow")}</span>
					</label>
					{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input） */}
					<label className="flex cursor-pointer items-center gap-2 text-fg text-sm">
						<Checkbox
							checked={shell}
							onChange={(e) => setShell(e.target.checked)}
						/>
						<span>{t("artifacts.mockup.shell")}</span>
					</label>
					{/* biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input） */}
					<label className="flex cursor-pointer items-center gap-2 text-fg text-sm">
						<Checkbox
							checked={long}
							onChange={(e) => setLong(e.target.checked)}
						/>
						<span>{t("artifacts.mockup.long")}</span>
					</label>
				</fieldset>

				{!renderReady && (
					<p className="text-err text-xs">
						{caps.data?.renderReason ?? t("artifacts.menu.pngUnavailable")}
					</p>
				)}
			</div>
		</Dialog>
	);
}
