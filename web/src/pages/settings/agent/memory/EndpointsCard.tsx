/**
 * 记忆页「嵌入与重排」卡（T4、T5、R17）：嵌入走 OpenAI 兼容接口、重排走 Cohere 兼容接口，经 orynd 中转。
 * key 只写不读（回报是否已设置；留空保留原值，可勾选清除）。bank 有记忆之后（`embedLocked`）嵌入模型与维度只读，
 * 要换须按部署文档执行 reembed。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryEndpoint,
	type MemoryEndpointKind,
	useMemoryEndpoints,
	usePutMemoryEndpoint,
} from "../../../../api/memory-queries";
import { Badge, Callout, Card } from "../../../../components/ui/data";
import {
	Button,
	Checkbox,
	Field,
	Spinner,
	TextInput,
} from "../../../../components/ui/primitives";
import { MemoryError } from "./errors";
import { MEMORY_REEMBED_DOC_URL } from "./shared";

export function EndpointsCard(): ReactNode {
	const { t } = useTranslation();
	const q = useMemoryEndpoints();
	const d = q.data;
	return (
		<Card title={t("memoryPage.endpoints.title")} icon="plugs">
			{q.isPending ? (
				<Spinner />
			) : q.error ? (
				<MemoryError step="loadEndpoints" error={q.error} />
			) : d ? (
				<div className="flex flex-col gap-4">
					<EndpointForm
						kind="embed"
						endpoint={d.embed}
						locked={d.embedLocked}
					/>
					<div className="border-border border-t" />
					<EndpointForm kind="rerank" endpoint={d.rerank} locked={false} />
				</div>
			) : null}
		</Card>
	);
}

function EndpointForm({
	kind,
	endpoint,
	locked,
}: {
	kind: MemoryEndpointKind;
	endpoint: MemoryEndpoint | undefined;
	/** 嵌入已锁定：模型与维度只读，勾选「换嵌入模型（reembed）」后才能改 */
	locked: boolean;
}): ReactNode {
	const { t } = useTranslation();
	const put = usePutMemoryEndpoint();
	const [baseUrl, setBaseUrl] = useState(endpoint?.baseUrl ?? "");
	const [model, setModel] = useState(endpoint?.model ?? "");
	const [dims, setDims] = useState(endpoint?.dimensions?.toString() ?? "");
	const [apiKey, setApiKey] = useState("");
	const [clearKey, setClearKey] = useState(false);
	const [reembed, setReembed] = useState(false);
	useEffect(() => {
		setBaseUrl(endpoint?.baseUrl ?? "");
		setModel(endpoint?.model ?? "");
		setDims(endpoint?.dimensions?.toString() ?? "");
		setApiKey("");
		setClearKey(false);
		setReembed(false);
	}, [endpoint]);
	const readOnly = locked && !reembed;
	const isEmbed = kind === "embed";
	const dimsNum = dims.trim() === "" ? undefined : Number(dims);
	const dimsValid =
		dimsNum === undefined || (Number.isInteger(dimsNum) && dimsNum > 0);
	const dirty =
		baseUrl.trim() !== (endpoint?.baseUrl ?? "") ||
		model.trim() !== (endpoint?.model ?? "") ||
		(isEmbed && dimsNum !== endpoint?.dimensions) ||
		apiKey !== "" ||
		clearKey;
	const canSave =
		dirty && baseUrl.trim() !== "" && model.trim() !== "" && dimsValid;
	const save = () =>
		put.mutate({
			kind,
			body: {
				baseUrl: baseUrl.trim(),
				model: model.trim(),
				...(isEmbed && dimsNum !== undefined ? { dimensions: dimsNum } : {}),
				...(apiKey ? { apiKey } : {}),
				...(clearKey ? { clearKey: true } : {}),
				...(locked && reembed ? { reembed: true } : {}),
			},
		});
	return (
		<form
			className="flex flex-col gap-2.5"
			data-endpoint={kind}
			onSubmit={(e) => {
				e.preventDefault();
				if (canSave) save();
			}}
		>
			<div className="flex flex-wrap items-center gap-1.5">
				<h4 className="font-medium text-sm">
					{t(`memoryPage.endpoints.${kind}.title`)}
				</h4>
				<Badge>{t(`memoryPage.endpoints.${kind}.protocol`)}</Badge>
				{locked && (
					<Badge icon="lock">{t("memoryPage.endpoints.lockedBadge")}</Badge>
				)}
			</div>
			{locked && (
				<Callout tone="neutral" icon="lock">
					{t("memoryPage.endpoints.lockedHint")}{" "}
					<a
						href={MEMORY_REEMBED_DOC_URL}
						target="_blank"
						rel="noopener"
						className="text-accent-ink underline"
					>
						{t("memoryPage.endpoints.reembedDoc")}
					</a>
				</Callout>
			)}
			{locked && (
				// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input）
				<label className="inline-flex items-center gap-2 text-sm">
					<Checkbox
						checked={reembed}
						onChange={(e) => {
							setReembed(e.target.checked);
							// 取消时丢掉改到一半的模型与维度
							if (!e.target.checked) {
								setModel(endpoint?.model ?? "");
								setDims(endpoint?.dimensions?.toString() ?? "");
							}
						}}
					/>
					{t("memoryPage.endpoints.reembed")}
				</label>
			)}
			{locked && reembed && (
				<Callout tone="warn" role="status">
					{t("memoryPage.endpoints.reembedHint")}
				</Callout>
			)}
			<Field label={t("memoryPage.endpoints.baseUrl")}>
				<TextInput
					value={baseUrl}
					onChange={(e) => setBaseUrl(e.target.value)}
					placeholder={t(`memoryPage.endpoints.${kind}.urlPlaceholder`)}
					inputMode="url"
					autoComplete="off"
					spellCheck={false}
					className="font-mono"
				/>
			</Field>
			<div className="grid gap-2.5 sm:grid-cols-[1fr_8rem]">
				<Field label={t("memoryPage.endpoints.model")}>
					<TextInput
						value={model}
						onChange={(e) => setModel(e.target.value)}
						readOnly={readOnly}
						aria-readonly={readOnly}
						autoComplete="off"
						spellCheck={false}
						className={`font-mono ${readOnly ? "text-muted" : ""}`}
					/>
				</Field>
				{isEmbed && (
					<Field label={t("memoryPage.endpoints.dimensions")}>
						<TextInput
							value={dims}
							onChange={(e) => setDims(e.target.value)}
							readOnly={readOnly}
							aria-readonly={readOnly}
							inputMode="numeric"
							className={`tabular-nums ${readOnly ? "text-muted" : ""}`}
						/>
					</Field>
				)}
			</div>
			<Field
				label={t("memoryPage.endpoints.apiKey")}
				hint={
					endpoint?.hasKey
						? t("memoryPage.endpoints.keySet")
						: t("memoryPage.endpoints.keyUnset")
				}
			>
				<TextInput
					type="password"
					value={apiKey}
					onChange={(e) => {
						setApiKey(e.target.value);
						if (e.target.value) setClearKey(false);
					}}
					disabled={clearKey}
					autoComplete="new-password"
					placeholder={
						endpoint?.hasKey ? "••••••••" : t("memoryPage.endpoints.keyEmpty")
					}
				/>
			</Field>
			{endpoint?.hasKey && (
				// biome-ignore lint/a11y/noLabelWithoutControl: 控件为 Checkbox 组件（内含原生 input）
				<label className="inline-flex items-center gap-2 text-sm">
					<Checkbox
						checked={clearKey}
						onChange={(e) => {
							setClearKey(e.target.checked);
							if (e.target.checked) setApiKey("");
						}}
					/>
					{t("memoryPage.endpoints.clearKey")}
				</label>
			)}
			{!dimsValid && (
				<p className="text-err text-xs">
					{t("memoryPage.endpoints.dimensionsInvalid")}
				</p>
			)}
			<div className="flex justify-end">
				<Button
					type="submit"
					variant="primary"
					disabled={!canSave || put.isPending}
				>
					{t("common.save")}
				</Button>
			</div>
			<MemoryError step="saveEndpoint" error={put.error} />
		</form>
	);
}
