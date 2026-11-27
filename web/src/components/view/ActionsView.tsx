/** 视图的 `actions` 块：点一个按钮即作答（正文是该按钮的 prompt），之后整组锁定并标出选中的那个。 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ActionsBlock } from "../../lib/view/spec";
import { Button, describeError, Spinner } from "../ui/primitives";
import { useView } from "./context";

export function ActionsView({ block }: { block: ActionsBlock }): ReactNode {
	const { t } = useTranslation();
	const view = useView();
	const [local, setLocal] = useState<{
		choice: number;
		status: "pending" | "done";
	} | null>(null);
	const [failure, setFailure] = useState<unknown>(null);

	const answer = view.answers.get(block.path);
	const chosen = answer?.kind === "action" ? answer.choice : local?.choice;
	const locked = chosen !== undefined;
	const canAnswer = !locked && view.interactive && !!view.onAnswer;
	const pending = local?.status === "pending" && answer === undefined;

	const choose = async (choice: number) => {
		const button = block.buttons[choice];
		if (!canAnswer || !view.onAnswer || !button) return;
		setFailure(null);
		setLocal({ choice, status: "pending" });
		try {
			await view.onAnswer(
				{ block: block.path, kind: "action", choice },
				button.prompt,
			);
			setLocal({ choice, status: "done" });
		} catch (err) {
			setLocal(null);
			setFailure(err);
		}
	};

	return (
		<div className="flex min-w-0 flex-col gap-1.5">
			<div className="flex flex-wrap gap-2">
				{block.buttons.map((b, i) => {
					const isChosen = locked && i === chosen;
					return (
						<Button
							// biome-ignore lint/suspicious/noArrayIndexKey: 按钮的下标就是作答里的 choice
							key={i}
							variant={
								isChosen || (!locked && b.variant === "primary")
									? "primary"
									: "secondary"
							}
							icon={isChosen && !pending ? "check" : undefined}
							title={b.label}
							// 选中的那个保持墨色（不走禁用态的灰底），只是不再响应
							disabled={locked ? !isChosen : !canAnswer}
							aria-disabled={isChosen || undefined}
							aria-pressed={locked ? isChosen : undefined}
							onClick={isChosen ? undefined : () => choose(i)}
							className="max-w-full"
						>
							{isChosen && pending && <Spinner size={12} />}
							<span className="min-w-0 truncate">{b.label}</span>
						</Button>
					);
				})}
			</div>
			{failure !== null && !locked && (
				<span role="alert" className="text-err text-xs">
					{t("view.form.failed", { error: describeError(failure, t) })}
				</span>
			)}
			{locked && !pending && (
				<span className="sr-only" role="status">
					{t("view.form.chosen", { label: block.buttons[chosen]?.label ?? "" })}
				</span>
			)}
		</div>
	);
}
