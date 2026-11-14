/**
 * 新建 / 编辑 Space：名称、目标、项目（含「无项目」）。新建需要协调宿主可用：不可用时禁用提交并给出原因；
 * 服务端 409 `coordinator_unavailable` 显示其 `message`，`space_name_taken` 标在名称框上。新建成功后跳到 Space 页。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { ApiError } from "../../api/client";
import { useCoordinatorStatus } from "../../api/coordinator-queries";
import { useProjects } from "../../api/queries";
import {
	type Space,
	useCreateSpace,
	useUpdateSpace,
} from "../../api/space-queries";
import { Callout } from "../ui/data";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	Select,
	TextArea,
	TextInput,
} from "../ui/primitives";

export function SpaceDialog({
	space,
	onClose,
}: {
	/** 给出时为编辑 */
	space?: Space;
	onClose: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const projects = useProjects();
	const coordinator = useCoordinatorStatus();
	const create = useCreateSpace();
	const update = useUpdateSpace(space?.id ?? "");
	const [name, setName] = useState(space?.name ?? "");
	const [goal, setGoal] = useState(space?.goal ?? "");
	const [projectId, setProjectId] = useState(space?.projectId ?? "");
	const unavailable = !space && coordinator.data?.available === false;
	const busy = create.isPending || update.isPending;
	const error = create.error ?? update.error;
	const nameTaken =
		error instanceof ApiError && error.code === "space_name_taken";
	const submit = async () => {
		const trimmed = name.trim();
		if (!trimmed) return;
		if (space) {
			await update.mutateAsync({
				...(trimmed !== space.name ? { name: trimmed } : {}),
				...(goal !== space.goal ? { goal } : {}),
				...(projectId !== (space.projectId ?? "") ? { projectId } : {}),
			});
			onClose();
			return;
		}
		const created = await create.mutateAsync({
			name: trimmed,
			...(goal.trim() ? { goal: goal.trim() } : {}),
			...(projectId ? { projectId } : {}),
		});
		onClose();
		navigate(`/space/${created.id}`);
	};
	return (
		<Dialog
			open
			onClose={onClose}
			title={space ? t("space.edit") : t("space.new")}
			footer={
				<>
					<Button onClick={onClose}>{t("common.cancel")}</Button>
					<Button
						variant="primary"
						disabled={busy || unavailable || !name.trim()}
						onClick={() => void submit().catch(() => undefined)}
					>
						{space ? t("common.save") : t("common.create")}
					</Button>
				</>
			}
		>
			<form
				className="flex flex-col gap-3"
				onSubmit={(e) => {
					e.preventDefault();
					void submit().catch(() => undefined);
				}}
			>
				{unavailable && (
					<Callout tone="warn" title={t("coordinator.unavailableTitle")}>
						{coordinator.data?.reason}
					</Callout>
				)}
				<Field
					label={t("space.name")}
					hint={nameTaken ? undefined : t("space.nameHint")}
				>
					<TextInput
						autoFocus
						value={name}
						maxLength={80}
						aria-invalid={nameTaken || undefined}
						onChange={(e) => setName(e.target.value)}
					/>
				</Field>
				{nameTaken && (
					<p role="alert" className="-mt-2 text-err text-xs">
						{t("space.nameTaken")}
					</p>
				)}
				<Field label={t("space.goal")} hint={t("space.goalHint")}>
					<TextArea
						rows={3}
						className="font-sans"
						value={goal}
						onChange={(e) => setGoal(e.target.value)}
					/>
				</Field>
				<Field label={t("space.project")} hint={t("space.projectHint")}>
					<Select
						value={projectId}
						onChange={(e) => setProjectId(e.target.value)}
					>
						<option value="">{t("sidebar.noProject")}</option>
						{(projects.data?.projects ?? [])
							.filter((p) => !p.archivedAt || p.id === projectId)
							.map((p) => (
								<option key={p.id} value={p.id}>
									{p.name}
								</option>
							))}
					</Select>
				</Field>
				{!nameTaken && <ErrorText error={error} />}
			</form>
		</Dialog>
	);
}
