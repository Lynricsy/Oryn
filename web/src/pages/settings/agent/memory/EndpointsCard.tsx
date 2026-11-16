/**
 * 记忆页「嵌入与重排」卡（T4、T5、R17、v5）：嵌入走 OpenAI 兼容接口、重排走 Cohere 兼容接口，经 orynd 中转。
 * key 只写不读（回报是否已设置；留空保留原值，可勾选清除）。bank 有记忆之后（`embedLocked`）嵌入模型与维度只读，
 * 要换须按部署文档执行 reembed。重排可选：未配置 = 不重排，Hindsight 用自带的 rrf；重排接口可删除（两步确认）。
 * 重排模式与 `.env` 里已部署的不同时（`needsRestart`），给出 `.env` 片段、「需要重启 Hindsight」与「我已重启」。
 */
import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	type MemoryEndpoint,
	type MemoryEndpointKind,
	type MemoryEndpoints,
	useConfirmMemoryModelsDeployed,
	useDeleteMemoryRerank,
	useMemoryEndpoints,
	usePutMemoryEndpoint,
} from "../../../../api/memory-queries";
import { ConfirmButton } from "../../../../components/share/ConfirmButton";
import { CopyButton } from "../../../../components/ui/CopyButton";
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
					{d.needsRestart && <RerankRestart endpoints={d} />}
				</div>
			) : null}
		</Card>
	);
}

/** 重排模式变了：要改进 `.env` 的行、「需要重启 Hindsight」与「我已重启」（与模型卡同一套确认接口）。 */
function RerankRestart({
	endpoints,
}: {
	endpoints: MemoryEndpoints;
}): ReactNode {
	const { t } = useTranslation();
	const confirm = useConfirmMemoryModelsDeployed();
	return (
		<Callout
			tone="warn"
			role="status"
			title={t("memoryPage.models.needsRestart")}
		>
			<div className="flex flex-col gap-2">
				<p>
					{t(`memoryPage.endpoints.rerank.restartHint.${endpoints.rerankMode}`)}
				</p>
				{endpoints.envSnippet && (
					<div className="flex flex-col gap-1 text-fg">
						<div className="flex justify-end">
							<CopyButton
								text={endpoints.envSnippet}
								label={t("common.copy")}
							/>
						</div>
						<pre
							data-testid="memory-rerank-env-snippet"
							className="overflow-auto whitespace-pre rounded-md bg-inset px-3 py-2 font-mono text-xs"
						>
							{endpoints.envSnippet}
						</pre>
					</div>
				)}
				<div>
					<Button
						size="sm"
						variant="primary"
						disabled={confirm.isPending}
						onClick={() => confirm.mutate()}
					>
						{t("memoryPage.models.restarted")}
					</Button>
				</div>
				<MemoryError step="confirmDeployed" error={confirm.error} />
			</div>
		</Callout>
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
	const del = useDeleteMemoryRerank();
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
				{kind === "rerank" && (
					<Badge>{t("memoryPage.endpoints.rerank.optional")}</Badge>
				)}
				{locked && (
					<Badge icon="lock">{t("memoryPage.endpoints.lockedBadge")}</Badge>
				)}
			</div>
			{kind === "rerank" && (
				<p className="text-faint text-xs">
					{endpoint
						? t("memoryPage.endpoints.rerank.configuredHint")
						: t("memoryPage.endpoints.rerank.unsetHint")}
				</p>
			)}
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
			<div className="flex items-center justify-end gap-2">
				{kind === "rerank" && endpoint && (
					<ConfirmButton
						label={t("memoryPage.endpoints.rerank.delete")}
						confirmLabel={t("memoryPage.endpoints.rerank.deleteConfirm")}
						disabled={del.isPending}
						onConfirm={() => del.mutate()}
					/>
				)}
				<Button
					type="submit"
					variant="primary"
					disabled={!canSave || put.isPending}
				>
					{t("common.save")}
				</Button>
			</div>
			<MemoryError step="saveEndpoint" error={put.error} />
			<MemoryError step="deleteEndpoint" error={del.error} />
		</form>
	);
}
