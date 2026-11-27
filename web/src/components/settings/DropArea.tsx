/**
 * 设置里多文件条目的拖放区：接收文件与文件夹（文件夹经 `webkitGetAsEntry` 递归，路径保留目录层级），
 * 另带「选择文件 / 选择文件夹」两个隐藏的选择框，由调用方的按钮触发。
 */
import {
	type DragEvent,
	type ReactNode,
	type RefObject,
	useRef,
	useState,
} from "react";
import { batchFromDrop } from "../files/UploadQueue";

export interface PickedFile {
	file: File;
	/** 相对路径：拖入的文件夹与文件夹选择框保留目录层级（以所选文件夹名开头） */
	path: string;
}

/** 拖放区；`children` 是区内内容。 */
export function DropArea({
	onFiles,
	disabled,
	className = "",
	activeClassName = "",
	children,
	label,
}: {
	onFiles(files: PickedFile[]): void;
	disabled?: boolean;
	className?: string;
	/** 拖到区内时追加的样式 */
	activeClassName?: string;
	children: ReactNode;
	label?: string;
}) {
	const [over, setOver] = useState(false);
	const accepts = (e: DragEvent) =>
		!disabled && e.dataTransfer.types.includes("Files");
	return (
		<section
			aria-label={label}
			className={`${className} ${over ? activeClassName : ""}`}
			onDragOver={(e) => {
				if (!accepts(e)) return;
				e.preventDefault();
				setOver(true);
			}}
			onDragLeave={(e) => {
				if (!e.currentTarget.contains(e.relatedTarget as Node | null))
					setOver(false);
			}}
			onDrop={(e) => {
				if (!accepts(e)) return;
				e.preventDefault();
				setOver(false);
				// 条目须在 drop 的同步阶段取出：batchFromDrop 在第一个 await 之前完成
				void batchFromDrop(e.dataTransfer, "").then((b) => onFiles(b.files));
			}}
		>
			{children}
		</section>
	);
}

/** 两个隐藏的选择框：多文件与文件夹（`webkitdirectory`）。返回触发函数与要渲染的元素。 */
export function usePickers(onFiles: (files: PickedFile[]) => void): {
	pickFiles(): void;
	pickFolder(): void;
	inputs: ReactNode;
} {
	const filesRef = useRef<HTMLInputElement>(null);
	const folderRef = useRef<HTMLInputElement>(null);
	const take = (el: HTMLInputElement) => {
		const list = [...(el.files ?? [])].map((file) => ({
			file,
			path: file.webkitRelativePath || file.name,
		}));
		el.value = "";
		if (list.length > 0) onFiles(list);
	};
	return {
		pickFiles: () => filesRef.current?.click(),
		pickFolder: () => folderRef.current?.click(),
		inputs: (
			<>
				<HiddenInput inputRef={filesRef} onPick={take} />
				<HiddenInput inputRef={folderRef} onPick={take} folder />
			</>
		),
	};
}

function HiddenInput({
	inputRef,
	onPick,
	folder,
}: {
	inputRef: RefObject<HTMLInputElement | null>;
	onPick(el: HTMLInputElement): void;
	folder?: boolean;
}) {
	return (
		<input
			ref={(el) => {
				inputRef.current = el;
				// React 不认 webkitdirectory 属性的类型，直接设到 DOM 上
				if (el && folder) el.setAttribute("webkitdirectory", "");
			}}
			type="file"
			multiple
			hidden
			data-testid={folder ? "pick-folder-input" : "pick-files-input"}
			onChange={(e) => onPick(e.currentTarget)}
		/>
	);
}

/** 单个 zip 选择框。 */
export function useZipPicker(onZip: (file: File) => void): {
	pickZip(): void;
	input: ReactNode;
} {
	const ref = useRef<HTMLInputElement>(null);
	return {
		pickZip: () => ref.current?.click(),
		input: (
			<input
				ref={ref}
				type="file"
				hidden
				accept=".zip,.skill,application/zip"
				data-testid="pick-zip-input"
				onChange={(e) => {
					const el = e.currentTarget;
					const file = el.files?.[0];
					el.value = "";
					if (file) onZip(file);
				}}
			/>
		),
	};
}
