/**
 * 文件拖拽投放：统一处理 `dragenter` / `dragover` / `dragleave` / `drop` 的嵌套计数与兜底清理，
 * 以及拖入时的玻璃虚线高亮遮罩。只在拖拽携带 `Files` 时激活，不干扰排队消息排序或文本选区拖拽。
 */
import {
	type DragEvent,
	type ReactNode,
	useCallback,
	useEffect,
	useRef,
	useState,
} from "react";
import { useTranslation } from "react-i18next";
import { Icon } from "../ui/icons";

/** 拖拽负载是否携带文件（兼容 Safari / WKWebView 的 `DOMStringList` 与标准数组）。 */
export function hasDraggedFiles(dt: DataTransfer | null): boolean {
	if (!dt?.types) return false;
	return Array.from(dt.types).includes("Files");
}

export interface FileDropBindings {
	onDragEnter: (e: DragEvent) => void;
	onDragOver: (e: DragEvent) => void;
	onDragLeave: (e: DragEvent) => void;
	onDrop: (e: DragEvent) => void;
}

/**
 * 绑定到一个容器上的文件拖放状态。用进入深度计数抵消子元素切换触发的成对 `dragenter`/`dragleave`，
 * 并在窗口失焦、拖拽结束或禁用时归零，避免遮罩卡住。
 */
export function useFileDrop({
	enabled,
	onFiles,
}: {
	enabled: boolean;
	onFiles: (files: File[]) => void;
}): {
	dragging: boolean;
	bind: FileDropBindings;
} {
	const [dragging, setDragging] = useState(false);
	const depthRef = useRef(0);
	const onFilesRef = useRef(onFiles);
	onFilesRef.current = onFiles;

	const reset = useCallback(() => {
		depthRef.current = 0;
		setDragging(false);
	}, []);

	useEffect(() => {
		if (!enabled) reset();
	}, [enabled, reset]);

	useEffect(() => {
		if (!dragging) return;
		window.addEventListener("dragend", reset);
		window.addEventListener("blur", reset);
		window.addEventListener("drop", reset);
		return () => {
			window.removeEventListener("dragend", reset);
			window.removeEventListener("blur", reset);
			window.removeEventListener("drop", reset);
		};
	}, [dragging, reset]);

	const onDragEnter = useCallback(
		(e: DragEvent) => {
			if (!enabled || !hasDraggedFiles(e.dataTransfer)) return;
			e.preventDefault();
			depthRef.current += 1;
			setDragging(true);
		},
		[enabled],
	);

	const onDragOver = useCallback(
		(e: DragEvent) => {
			if (!enabled || !hasDraggedFiles(e.dataTransfer)) return;
			e.preventDefault();
			// macOS / WebKit 下显式设 copy 才会显示带「+」的投放光标
			e.dataTransfer.dropEffect = "copy";
			if (depthRef.current === 0) depthRef.current = 1;
			setDragging(true);
		},
		[enabled],
	);

	const onDragLeave = useCallback((e: DragEvent) => {
		if (!hasDraggedFiles(e.dataTransfer)) return;
		depthRef.current = Math.max(0, depthRef.current - 1);
		if (depthRef.current === 0) setDragging(false);
	}, []);

	const onDrop = useCallback(
		(e: DragEvent) => {
			const hadFiles =
				hasDraggedFiles(e.dataTransfer) || e.dataTransfer.files.length > 0;
			reset();
			if (!enabled || !hadFiles) return;
			const files = [...e.dataTransfer.files];
			if (files.length === 0) return;
			e.preventDefault();
			e.stopPropagation();
			onFilesRef.current(files);
		},
		[enabled, reset],
	);

	return {
		dragging: enabled && dragging,
		bind: { onDragEnter, onDragOver, onDragLeave, onDrop },
	};
}

/**
 * 拖拽文件悬停在可投放区域时的视觉提示层：柔和主题色虚线描边 + 居中玻璃提示胶囊，
 * `pointer-events-none` 不截获底层 `drop` 事件。
 */
export function FileDropOverlay({
	active,
	compact = false,
	className = "",
}: {
	active: boolean;
	/** 紧凑样式：贴合输入区小托盘（单行胶囊）；否则用于整页对话区。 */
	compact?: boolean;
	className?: string;
}): ReactNode {
	const { t } = useTranslation();
	if (!active) return null;
	return (
		<div
			aria-live="polite"
			data-testid="file-drop-overlay"
			className={`file-drop-overlay pointer-events-none absolute inset-0 z-40 flex items-center justify-center rounded-[inherit] p-3 ${className}`}
		>
			<div
				className={`flex h-full w-full items-center justify-center rounded-xl border-2 border-dashed border-accent/65 bg-accent-soft/75 backdrop-blur-[3px] ${
					compact ? "px-3 py-2" : "p-6"
				}`}
			>
				<div
					className={`glass flex items-center rounded-2xl border border-accent/35 shadow-sm ${
						compact ? "gap-2 px-3.5 py-2" : "flex-col gap-2 px-6 py-4"
					}`}
				>
					<span
						className={`file-drop-icon inline-flex shrink-0 items-center justify-center rounded-xl bg-accent/15 text-accent-ink ${
							compact ? "h-7 w-7" : "h-11 w-11"
						}`}
					>
						<Icon name="upload" size={compact ? 15 : 22} weight="bold" />
					</span>
					<div className={compact ? "text-left" : "text-center"}>
						<p className="font-medium text-fg text-sm leading-5">
							{t("attachments.dropHint")}
						</p>
						{!compact && (
							<p className="mt-0.5 text-muted text-xs leading-4">
								{t("attachments.dropHintSub")}
							</p>
						)}
					</div>
				</div>
			</div>
		</div>
	);
}
