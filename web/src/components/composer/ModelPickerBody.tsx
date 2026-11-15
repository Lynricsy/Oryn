/**
 * 模型选择的正文（线程里的模型对话框、首屏 role 胶囊的「指定模型…」共用）：上方是调用方给的 role 列表，
 * 下方是中央模型目录里的对话模型（按输入框筛选）。`free` 时输入框里写的 `provider/id` 目录里没有也能直接用。
 * 页脚（目录的刷新与过期提示）由调用方放：对话框放在页脚栏，弹层放在列表末尾。
 */
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { chatModels, useModelCatalog } from "../../api/model-catalog-queries";
import type { ModelRef } from "../../lib/model-roles";
import { useBreakpoint } from "../../lib/viewport";
import { Icon } from "../ui/icons";
import { ModelLogo } from "../ui/ModelLogo";
import { ErrorText, Spinner, TextInput } from "../ui/primitives";

/** 输入框里的 `provider/id`（可带 `:级别`，级别不算进模型）；不是这个形状时为 undefined。 */
export function parseModelInput(value: string): ModelRef | undefined {
	const v = value.trim().replace(/:[^/]*$/, "");
	const slash = v.indexOf("/");
	if (slash <= 0 || slash === v.length - 1) return undefined;
	return { provider: v.slice(0, slash), id: v.slice(slash + 1) };
}

export function ModelPickerBody({
	roles,
	current,
	onPick,
	initialQuery,
	error,
	free = false,
}: {
	/** 模型列表上方的 role 列表 */
	roles?: ReactNode;
	/** 当前选中的模型（行尾打勾） */
	current?: { provider: string; id: string };
	onPick: (m: ModelRef) => void;
	/** 打开时预填的筛选词 */
	initialQuery?: string;
	/** 调用方的错误（如选择命令失败），与目录读取失败一起显示 */
	error?: unknown;
	/** 允许把输入框里的 `provider/id` 直接当模型用（目录里没有时多出一行「使用 …」） */
	free?: boolean;
}): ReactNode {
	const { t } = useTranslation();
	// 手机上自动聚焦搜索框会弹出软键盘，把顶部的 role 列表挤出视野
	const compact = useBreakpoint() === "compact";
	const models = useModelCatalog();
	const [q, setQ] = useState(initialQuery ?? "");
	const all = chatModels(models.data);
	const needle = q.trim().toLowerCase();
	const list = all.filter((m) =>
		`${m.selector} ${m.name}`.toLowerCase().includes(needle),
	);
	const typed = free ? parseModelInput(q) : undefined;
	const custom =
		typed &&
		!all.some((m) => m.provider === typed.provider && m.id === typed.id)
			? typed
			: undefined;
	const placeholder = free ? t("roleCapsule.modelInput") : t("model.search");
	return (
		<>
			{roles}
			{/* 思考级别在别处调（输入坞的圆圈、胶囊弹层的思考强度），这里只选模型 */}
			<h3 className="mb-1 px-2 font-medium text-2xs text-faint">
				{t("model.allModels")}
			</h3>
			{/* 桌面打开即可输入筛选；手机上不自动聚焦，免得软键盘把 role 列表挤出视野 */}
			<TextInput
				autoFocus={!compact}
				aria-label={placeholder}
				placeholder={placeholder}
				value={q}
				onChange={(e) => setQ(e.target.value)}
				className="mb-2"
			/>
			<ErrorText error={error ?? models.error} />
			{models.isPending && (
				<div className="flex items-center gap-2 px-2 py-3 text-muted text-sm">
					<Spinner />
					{t("model.loading")}
				</div>
			)}
			{models.isSuccess && list.length === 0 && !custom && (
				<p className="px-2 py-3 text-muted text-sm">
					{all.length === 0
						? t("model.noModels")
						: t("model.noMatch", { q: q.trim() })}
				</p>
			)}
			<ul className="flex flex-col gap-px">
				{custom && (
					<li>
						<button
							type="button"
							data-model-custom={`${custom.provider}/${custom.id}`}
							className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm transition-colors duration-150 hover:bg-hover"
							onClick={() => onPick(custom)}
						>
							<ModelLogo
								model={custom.id}
								provider={custom.provider}
								className="text-muted"
							/>
							<span className="min-w-0 flex-1 truncate">
								{t("roleCapsule.useModel", {
									model: `${custom.provider}/${custom.id}`,
								})}
							</span>
						</button>
					</li>
				)}
				{list.map((m) => {
					const selected =
						current?.provider === m.provider && current?.id === m.id;
					return (
						<li key={`${m.provider}/${m.id}`}>
							<button
								type="button"
								data-model={`${m.provider}/${m.id}`}
								aria-current={selected || undefined}
								className={`flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm transition-colors duration-150 hover:bg-hover ${selected ? "bg-hover font-medium" : ""}`}
								onClick={() =>
									onPick({ provider: m.provider, id: m.id, name: m.name })
								}
							>
								<ModelLogo
									model={m.id}
									provider={m.provider}
									className="text-muted"
								/>
								{/* 主名称优先：桌面同一行、空间不够先截 id；手机分两行，id 作次要文字放第二行 */}
								<span
									className={`flex min-w-0 flex-1 ${compact ? "flex-col" : "items-center gap-2"}`}
								>
									<span
										className={`min-w-0 truncate ${compact ? "" : "max-w-[60%] shrink-0"}`}
									>
										{m.name || m.id}
									</span>
									<span
										className={`min-w-0 truncate font-mono text-faint text-xs ${compact ? "" : "flex-1 text-right"}`}
									>{`${m.provider}/${m.id}`}</span>
								</span>
								{/* 与 role 列表同一种选中样式：灰底 + 行尾中性勾 */}
								{selected && (
									<Icon
										name="check"
										size={13}
										weight="bold"
										className="shrink-0 text-fg"
									/>
								)}
							</button>
						</li>
					);
				})}
			</ul>
		</>
	);
}
