// 查看器共用的编辑浮条（底部：提示、放弃、保存为 v{n}）与保存状态浮条（顶部）。

import { type ReactNode, useEffect } from "react";
import type { SaveStatus } from "./content";
import type { ViewerStrings } from "./strings";
import "./shared.css";

export function EditBar({
	hint,
	nextVersion,
	dirty,
	saving,
	strings,
	onDiscard,
	onSave,
}: {
	hint: string;
	/** 保存后的版本号（当前最新 + 1）。 */
	nextVersion: number;
	dirty: boolean;
	saving: boolean;
	strings: ViewerStrings;
	onDiscard(): void;
	onSave(): void;
}): ReactNode {
	return (
		<div className="oryn-editbar" role="toolbar" aria-label={hint}>
			<span className="oryn-editbar-hint">{hint}</span>
			<button
				type="button"
				className="oryn-btn"
				onClick={onDiscard}
				disabled={saving}
			>
				{strings.discard}
			</button>
			<button
				type="button"
				className="oryn-btn oryn-btn-primary"
				onClick={onSave}
				disabled={!dirty || saving}
			>
				{strings.saveAs(nextVersion)}
			</button>
		</div>
	);
}

/** 「已保存」几秒后自己收起；其余状态留到用户关掉或下一次保存。 */
const SAVED_VISIBLE_MS = 4000;

export function SaveStatusBar({
	status,
	strings,
	onDismiss,
}: {
	status: SaveStatus;
	strings: ViewerStrings;
	onDismiss(): void;
}): ReactNode {
	useEffect(() => {
		if (status.kind !== "saved") return;
		const timer = setTimeout(onDismiss, SAVED_VISIBLE_MS);
		return () => clearTimeout(timer);
	}, [status, onDismiss]);

	if (status.kind === "idle") return null;
	let text: string;
	switch (status.kind) {
		case "saving":
			text = strings.saving;
			break;
		case "saved":
			text = strings.saved(status.version);
			break;
		case "conflict":
			text = strings.conflict;
			break;
		case "invalid":
		case "failed":
			text = strings.notSaved(status.message);
			break;
	}
	const error = status.kind === "invalid" || status.kind === "failed";
	return (
		<div
			className={`oryn-status${error ? " oryn-status-error" : ""}`}
			role={error ? "alert" : "status"}
			data-status={status.kind}
		>
			<span className="oryn-status-text">{text}</span>
			{status.kind !== "saving" && (
				<button
					type="button"
					className="oryn-status-close"
					aria-label={strings.dismiss}
					onClick={onDismiss}
				>
					×
				</button>
			)}
		</div>
	);
}
