/**
 * 新建 Box 对话框（M23 步骤 7.3）：名字（本地按正则校验，错误就地显示）、规格（启用中的 provider × 类型；缺省「默认」
 * = 不传 provider / type，由控制面按默认落点解析）；页脚「交给 Agent」（把提示交给首屏并跳过去）与「创建」。
 * 创建失败时对话框保持打开、输入不丢，错误显示在表单下方；成功后进入 box 页。`expanded` 宽度下右侧放裁剪过的演示。
 */
import { type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { useCreateBox } from "../../api/box-queries";
import { useProviders } from "../../api/queries";
import { formatMemoryMB } from "../../lib/format";
import { useBreakpoint } from "../../lib/viewport";
import { uiStore } from "../../state/ui";
import {
	Button,
	Dialog,
	ErrorText,
	Field,
	Select,
	TextInput,
} from "../ui/primitives";
import { BoxesHeroPreview, BoxesPoints } from "./BoxesHero";
import { validBoxName } from "./box-model";

export function NewBoxDialog({ onClose }: { onClose: () => void }): ReactNode {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const wide = useBreakpoint() === "expanded";
	const providers = useProviders();
	const create = useCreateBox();
	const [name, setName] = useState("");
	const [size, setSize] = useState("");
	const [touched, setTouched] = useState(false);
	const nameError = touched && name !== "" && !validBoxName(name);
	const errId = useId();
	// 可选规格：不是 @host、不是 runner、没有停用的 provider 的全部类型
	const sizes = (providers.data?.providers ?? [])
		.filter((p) => p.name !== "@host" && !p.disabledAt && p.driver !== "runner")
		.flatMap((p) =>
			p.orbTypes.map((ot) => ({
				key: `${p.name}\u0000${ot.name}`,
				provider: p.name,
				type: ot.name,
				label: t("boxes.new.sizeOption", {
					provider: p.name,
					type: ot.name,
					cpus: ot.cpus,
					memory: formatMemoryMB(ot.memoryMB),
				}),
			})),
		);
	const submit = () => {
		setTouched(true);
		if (!validBoxName(name)) return;
		const pick = sizes.find((s) => s.key === size);
		create.mutate(
			{
				name,
				...(pick ? { provider: pick.provider, type: pick.type } : {}),
			},
			{ onSuccess: (box) => navigate(`/boxes/${box.id}`) },
		);
	};
	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={t("boxes.new.title")}
			bodyTestId="new-box-dialog-body"
			footer={
				<>
					<Button
						onClick={() => {
							uiStore.getState().seedHome({ text: t("boxes.new.seedPrompt") });
							onClose();
							navigate("/");
						}}
					>
						{t("boxes.new.handOff")}
					</Button>
					<Button
						variant="primary"
						disabled={create.isPending}
						onClick={submit}
					>
						{t("boxes.new.create")}
					</Button>
				</>
			}
		>
			<div
				className={`grid gap-6 ${wide ? "grid-cols-[minmax(0,1fr)_minmax(0,1fr)]" : ""}`}
			>
				<form
					className="flex min-w-0 flex-col gap-4"
					onSubmit={(e) => {
						e.preventDefault();
						submit();
					}}
				>
					<p className="text-muted text-sm leading-6">{t("boxes.hero.body")}</p>
					<BoxesPoints />
					<Field
						label={t("boxes.new.name")}
						hint={nameError ? undefined : t("boxes.new.nameHint")}
					>
						<TextInput
							autoFocus
							value={name}
							maxLength={30}
							spellCheck={false}
							autoCapitalize="off"
							autoComplete="off"
							placeholder="workbench"
							aria-invalid={nameError || undefined}
							aria-describedby={nameError ? errId : undefined}
							onChange={(e) => setName(e.target.value.trim())}
							onBlur={() => setTouched(true)}
						/>
					</Field>
					{nameError && (
						<p id={errId} className="-mt-3 text-err text-xs">
							{t("boxes.new.invalidName")}
						</p>
					)}
					<Field label={t("boxes.new.size")}>
						<Select
							value={size}
							onChange={setSize}
							options={[
								{ value: "", label: t("boxes.new.sizeDefault") },
								...sizes.map((s) => ({ value: s.key, label: s.label })),
							]}
						/>
					</Field>
					<ErrorText error={create.error} />
				</form>
				{wide && (
					<div className="flex min-w-0 items-center">
						<BoxesHeroPreview clip />
					</div>
				)}
			</div>
		</Dialog>
	);
}
