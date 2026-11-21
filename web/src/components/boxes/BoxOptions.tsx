/**
 * 首屏执行位置弹层里的「Boxes」分组（M23 步骤 7.5）：每台 box 一组（含停着的），一行「<box> · 机器」加每个有 live 的
 * app 一行。`desired = stopped` 的 box 整组禁用并带「已停止」——看得见、点不了（与服务端 `ThreadTarget` 的判定一致）。
 * 没有 box 时整个分组不渲染。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useBoxes } from "../../api/box-queries";
import { Badge, ProjectBadge } from "../ui/data";
import { Icon } from "../ui/icons";
import { Option, PopoverLabel } from "../ui/PickerPopover";
import type { BoxPick } from "./box-model";

export function BoxOptions({
	pick,
	onPick,
}: {
	pick: BoxPick | null;
	onPick(p: BoxPick | null): void;
}): ReactNode {
	const { t } = useTranslation();
	const boxes = useBoxes().data?.boxes ?? [];
	if (boxes.length === 0) return null;
	return (
		<>
			<PopoverLabel>{t("boxes.picker.label")}</PopoverLabel>
			{boxes.map((b) => {
				const stopped = b.desired === "stopped";
				const badge = stopped ? (
					<Badge>{t("boxes.picker.stopped")}</Badge>
				) : undefined;
				return (
					<div key={b.id}>
						<Option
							selected={pick?.boxId === b.id && !pick.app}
							disabled={stopped}
							leading={<Icon name="hardDrives" size={14} />}
							hint={t("boxes.picker.machine")}
							trailing={badge}
							onClick={() => onPick({ boxId: b.id })}
						>
							{b.name}
						</Option>
						{b.apps
							.filter((a) => a.live)
							.map((a) => (
								<Option
									key={a.name}
									selected={pick?.boxId === b.id && pick.app === a.name}
									disabled={stopped}
									leading={<ProjectBadge name={a.name} size={16} />}
									hint={b.name}
									trailing={badge}
									onClick={() => onPick({ boxId: b.id, app: a.name })}
								>
									{a.name}
								</Option>
							))}
					</div>
				);
			})}
		</>
	);
}
