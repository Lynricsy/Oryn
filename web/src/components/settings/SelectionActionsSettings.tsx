/**
 * 设置 → Agent → 选区快捷动作（M22）：自定义的右键菜单动作，名称 + 提示词，选中文字后连同选区交给模型。
 * 列表可上移、下移、编辑、删除（删除先 `window.confirm`）；新增与编辑在对话框里填。每次改动都整体 PUT
 * （`saveSelectionActions`，成功后写回查询缓存，菜单读的副本由 AppShell 的 `useSelectionActionsSync` 跟上）。
 * 上限 12 条、名称 ≤ 24 字、提示词 ≤ 2000 字，与服务端校验一致。
 */
import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	saveSelectionActions,
	useSelectionActions,
} from "../../api/selection-actions-queries";
import type { SelectionAction } from "../../state/selection-actions";
import { Card, ListRow } from "../ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	IconButton,
	Spinner,
	TextArea,
	TextInput,
} from "../ui/primitives";

const MAX_ACTIONS = 12;
const ID_CHARS = "abcdefghijklmnopqrstuvwxyz0123456789";

/** 8 位 `[a-z0-9]` 的新 id，避开已有的。 */
function newActionId(taken: readonly SelectionAction[]): string {
	for (;;) {
		const bytes = crypto.getRandomValues(new Uint8Array(8));
		const id = Array.from(bytes, (b) => ID_CHARS[b % ID_CHARS.length]).join("");
		if (!taken.some((a) => a.id === id)) return id;
	}
}

/** 正在编辑的条目；`id` 为 null 表示新增。 */
interface Draft {
	id: string | null;
	name: string;
	prompt: string;
}

export default function SelectionActionsSettings(): ReactNode {
	const { t } = useTranslation();
	const qc = useQueryClient();
	const q = useSelectionActions();
	const actions = q.data?.actions ?? [];
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<unknown>(null);
	const [draft, setDraft] = useState<Draft | null>(null);
	const [draftError, setDraftError] = useState<unknown>(null);
	const full = actions.length >= MAX_ACTIONS;

	/** 整体保存；成功返回 true，失败把错误交给 `fail`。 */
	const save = async (
		next: SelectionAction[],
		fail: (e: unknown) => void,
	): Promise<boolean> => {
		setPending(true);
		try {
			await saveSelectionActions(qc, { actions: next });
			return true;
		} catch (e) {
			fail(e);
			return false;
		} finally {
			setPending(false);
		}
	};
	const saveList = (next: SelectionAction[]) => {
		setError(null);
		void save(next, setError);
	};
	const move = (i: number, by: -1 | 1) => {
		const j = i + by;
		const a = actions[i];
		const b = actions[j];
		if (!a || !b) return;
		saveList(actions.with(i, b).with(j, a));
	};
	const remove = (action: SelectionAction) => {
		if (!window.confirm(t("selectionActions.deleteConfirm"))) return;
		saveList(actions.filter((a) => a.id !== action.id));
	};
	const openDraft = (next: Draft) => {
		setDraftError(null);
		setDraft(next);
	};
	const submitDraft = async (e: FormEvent) => {
		e.preventDefault();
		if (!draft || pending) return;
		const name = draft.name.trim();
		const prompt = draft.prompt.trim();
		if (!name || !prompt) return;
		const next =
			draft.id === null
				? [...actions, { id: newActionId(actions), name, prompt }]
				: actions.map((a) => (a.id === draft.id ? { ...a, name, prompt } : a));
		setDraftError(null);
		if (await save(next, setDraftError)) setDraft(null);
	};
	const valid =
		draft !== null && draft.name.trim() !== "" && draft.prompt.trim() !== "";

	return (
		<Card
			title={t("selectionActions.title")}
			icon="bolt"
			action={
				<span className="flex items-center gap-2">
					{pending && <Spinner />}
					{full && (
						<span className="font-normal text-faint">
							{t("selectionActions.limit")}
						</span>
					)}
					<Button
						size="sm"
						icon="plus"
						disabled={full || pending || !q.data}
						onClick={() => openDraft({ id: null, name: "", prompt: "" })}
					>
						{t("selectionActions.add")}
					</Button>
				</span>
			}
		>
			<p className="pb-2 text-faint text-xs">{t("selectionActions.hint")}</p>
			{q.error ? (
				<ErrorText error={q.error} />
			) : !q.data ? (
				<div className="flex justify-center py-6">
					<Spinner />
				</div>
			) : actions.length === 0 ? (
				<p className="text-faint text-sm">{t("selectionActions.empty")}</p>
			) : (
				<ul
					className="-mx-2 flex flex-col"
					aria-label={t("selectionActions.title")}
				>
					{actions.map((a, i) => (
						<li key={a.id} data-testid="selection-action-row">
							<ListRow
								title={<span className="truncate font-medium">{a.name}</span>}
								meta={
									<span className="min-w-0 truncate">
										{a.prompt.split("\n", 1)[0]}
									</span>
								}
								trailing={
									<>
										<IconButton
											icon="chevronUp"
											label={t("selectionActions.moveUp")}
											disabled={pending || i === 0}
											onClick={() => move(i, -1)}
										/>
										<IconButton
											icon="chevronDown"
											label={t("selectionActions.moveDown")}
											disabled={pending || i === actions.length - 1}
											onClick={() => move(i, 1)}
										/>
										<IconButton
											icon="edit"
											label={t("common.edit")}
											disabled={pending}
											onClick={() =>
												openDraft({ id: a.id, name: a.name, prompt: a.prompt })
											}
										/>
										<IconButton
											icon="trash"
											label={t("common.delete")}
											disabled={pending}
											onClick={() => remove(a)}
										/>
									</>
								}
							/>
						</li>
					))}
				</ul>
			)}
			<ErrorText error={error} />
			<Dialog
				open={draft !== null}
				onClose={() => setDraft(null)}
				title={
					draft?.id === null ? t("selectionActions.add") : t("common.edit")
				}
				footer={
					<>
						<Button variant="ghost" onClick={() => setDraft(null)}>
							{t("common.cancel")}
						</Button>
						<Button
							type="submit"
							form="selection-action-form"
							variant="primary"
							disabled={!valid || pending}
						>
							{t("common.save")}
						</Button>
					</>
				}
			>
				{draft && (
					<form
						id="selection-action-form"
						className="flex flex-col gap-3"
						onSubmit={(e) => void submitDraft(e)}
					>
						<Field label={t("selectionActions.name")}>
							<TextInput
								autoFocus
								maxLength={24}
								value={draft.name}
								onChange={(e) => setDraft({ ...draft, name: e.target.value })}
							/>
						</Field>
						<Field
							label={t("selectionActions.prompt")}
							hint={t("selectionActions.promptHint")}
						>
							<TextArea
								mono={false}
								rows={6}
								maxLength={2000}
								value={draft.prompt}
								onChange={(e) => setDraft({ ...draft, prompt: e.target.value })}
							/>
						</Field>
						<ErrorText error={draftError} />
					</form>
				)}
			</Dialog>
		</Card>
	);
}
