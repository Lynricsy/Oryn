/**
 * 导入一个目录式条目（技能、扩展模块目录、自定义工具目录）：选择或拖入文件夹，或选择 / 拖入 .zip（含 .skill）。
 * - 所有文件都在同一个顶层文件夹下时去掉这一层，并以它的名字作建议名称；zip 没有顶层文件夹时以 zip 文件名作建议；
 * - 技能：根下没有 SKILL.md 但恰有一个子目录含 SKILL.md 时收窄到它（GitHub 仓库下载的 zip 常见）；多个时请逐个导入；
 * - 目标目录已存在时须勾选「替换」：同一批里先删除该目录再写入，整批一次请求；
 * - 条目入口缺失（技能缺 description、扩展缺 index / package.json、工具缺 index.ts）只提示，不阻止导入。
 */
import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
	useAgentFileActions,
	useAgentFiles,
} from "../../api/agent-config-queries";
import {
	type BundleFile,
	filesFromList,
	filesFromZip,
	narrowToSkill,
	skillDescription,
	stripCommonRoot,
	suggestName,
	toBatchFiles,
	totalBytes,
} from "../../lib/agent-bundle";
import { formatBytes } from "../../lib/format";
import { Callout } from "../ui/data";
import { Icon } from "../ui/icons";
import {
	Button,
	Checkbox,
	Dialog,
	ErrorText,
	Field,
	Spinner,
	TextInput,
} from "../ui/primitives";
import { AgentErrorText } from "./agent-config-ui";
import {
	DropArea,
	type PickedFile,
	usePickers,
	useZipPicker,
} from "./DropArea";

export type ImportKind = "skill" | "extension" | "tool";

const KIND_DIR: Record<ImportKind, string> = {
	skill: "skills",
	extension: "extensions",
	tool: "tools",
};

/** 技能名同 Core 托管技能的命名规则；扩展与工具目录名同其它条目。 */
const KIND_NAME: Record<ImportKind, RegExp> = {
	skill: /^[a-z0-9][a-z0-9-]{0,63}$/,
	extension: /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/,
	tool: /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/,
};

interface Parsed {
	files: BundleFile[];
}

export function EntryImportDialog({
	kind,
	sectionTitle,
	nameHint,
	onClose,
}: {
	kind: ImportKind;
	/** 分组标题（技能、扩展模块、自定义工具），用于对话框标题 */
	sectionTitle: string;
	nameHint: string;
	onClose(): void;
}) {
	const { t } = useTranslation();
	const files = useAgentFiles();
	const actions = useAgentFileActions();
	const dir = KIND_DIR[kind];
	const [parsed, setParsed] = useState<Parsed | null>(null);
	const [reading, setReading] = useState(false);
	const [parseError, setParseError] = useState<string | null>(null);
	const [name, setName] = useState("");
	const [replace, setReplace] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<unknown>(null);

	/** 去掉公共顶层目录，技能再收窄到 SKILL.md 所在目录；成功时更新建议名。 */
	const accept = (all: BundleFile[], fallbackName: string) => {
		if (all.length === 0) {
			setParseError(t("agentPages.import.errors.empty"));
			return;
		}
		const stripped = stripCommonRoot(all);
		let rootName = stripped.root ?? fallbackName;
		let chosen = stripped.files;
		if (kind === "skill") {
			const narrowed = narrowToSkill(chosen);
			if (!narrowed.ok) {
				setParseError(
					narrowed.candidates.length === 0
						? t("agentPages.import.errors.noSkill")
						: t("agentPages.import.errors.multipleSkills", {
								names: narrowed.candidates.join("、"),
							}),
				);
				return;
			}
			chosen = narrowed.files;
			rootName = narrowed.root ?? rootName;
		}
		setParsed({ files: chosen });
		setName(suggestName(rootName, kind === "skill"));
		setReplace(false);
	};

	const load = async (job: () => Promise<void>) => {
		setReading(true);
		setParseError(null);
		setParsed(null);
		setError(null);
		try {
			await job();
		} catch (e) {
			setParseError(
				t("agentPages.import.errors.read", {
					reason: e instanceof Error ? e.message : String(e),
				}),
			);
		} finally {
			setReading(false);
		}
	};

	const readZip = (file: File) =>
		void load(async () =>
			accept(filesFromZip(new Uint8Array(await file.arrayBuffer())), file.name),
		);

	const readPicked = (list: PickedFile[]) => {
		const only = list.length === 1 ? list[0] : undefined;
		// 拖入单个 zip：解包
		if (
			only &&
			!only.path.includes("/") &&
			/\.(zip|skill)$/i.test(only.file.name)
		) {
			readZip(only.file);
			return;
		}
		void load(async () => accept(await filesFromList(list), ""));
	};

	const pickers = usePickers(readPicked);
	const zip = useZipPicker(readZip);

	const trimmed = name.trim();
	const target = `${dir}/${trimmed}`;
	const all = files.data ?? [];
	const exists = all.some((f) => f.path.startsWith(`${target}/`));
	const fileTaken = all.some((f) => f.path === target);
	const nameError = !trimmed
		? t("agentPages.errors.nameRequired")
		: !KIND_NAME[kind].test(trimmed)
			? t("agentPages.errors.nameInvalid")
			: fileTaken
				? t("agentPages.import.fileTaken", { path: target })
				: null;

	const warning = parsed ? entryWarning(kind, parsed.files) : null;
	const canImport =
		parsed !== null && nameError === null && (!exists || replace) && !busy;

	const submit = async () => {
		if (!parsed || !canImport) return;
		setBusy(true);
		setError(null);
		try {
			await actions.apply({
				...(exists ? { delete: [target] } : {}),
				files: toBatchFiles(target, parsed.files),
			});
			onClose();
		} catch (e) {
			setError(e);
			setBusy(false);
		}
	};

	return (
		<Dialog
			open
			wide
			onClose={onClose}
			title={t("agentPages.import.title", { section: sectionTitle })}
			footer={
				<>
					<Button variant="ghost" onClick={onClose}>
						{t("common.cancel")}
					</Button>
					<Button
						variant="primary"
						disabled={!canImport}
						onClick={() => void submit()}
					>
						{busy && <Spinner size={12} />}
						{t("agentPages.import.submit")}
					</Button>
				</>
			}
		>
			<div className="flex flex-col gap-3">
				<DropArea
					label={t("agentPages.import.dropLabel")}
					onFiles={readPicked}
					disabled={busy || reading}
					className="flex flex-col items-center gap-2 rounded-md border border-border border-dashed px-4 py-5 text-center"
					activeClassName="border-accent bg-hover"
				>
					<Icon name="upload" size={20} className="text-muted" aria-hidden />
					<p className="text-[12.5px] text-muted">
						{t("agentPages.import.dropHint")}
					</p>
					<div className="flex flex-wrap justify-center gap-1.5">
						<Button
							size="sm"
							icon="folderOpen"
							disabled={busy || reading}
							onClick={pickers.pickFolder}
						>
							{t("agentPages.import.pickFolder")}
						</Button>
						<Button
							size="sm"
							icon="archive"
							disabled={busy || reading}
							onClick={zip.pickZip}
						>
							{t("agentPages.import.pickZip")}
						</Button>
					</div>
					{pickers.inputs}
					{zip.input}
				</DropArea>
				{reading && <Spinner />}
				{parseError && <ErrorText error={new Error(parseError)} />}
				{parsed && (
					<>
						<Field label={t("agentPages.name")} hint={nameHint}>
							<TextInput
								value={name}
								aria-invalid={nameError !== null}
								onChange={(e) => {
									setName(e.target.value);
									setReplace(false);
								}}
							/>
						</Field>
						{nameError && <p className="-mt-2 text-err text-xs">{nameError}</p>}
						<p className="text-[12.5px] text-muted">
							{t("agentPages.import.summary", {
								count: parsed.files.length,
								size: formatBytes(totalBytes(parsed.files)),
								path: `${target}/`,
							})}
						</p>
						<ul className="max-h-40 overflow-y-auto rounded-md border border-border bg-inset px-2.5 py-1.5 font-mono text-[12px] text-muted">
							{parsed.files.map((f) => (
								<li key={f.path} className="truncate">
									{f.path}
								</li>
							))}
						</ul>
						{warning && (
							<Callout tone="warn" icon="alert">
								{t(warning)}
							</Callout>
						)}
						{exists && nameError === null && (
							<Callout tone="warn" icon="alert">
								<div className="flex flex-col gap-1.5">
									<span>
										{t("agentPages.import.exists", { path: `${target}/` })}
									</span>
									{/* biome-ignore lint/a11y/noLabelWithoutControl: Checkbox 渲染原生 input */}
									<label className="flex items-center gap-2 text-fg">
										<Checkbox
											checked={replace}
											onChange={(e) => setReplace(e.target.checked)}
										/>
										{t("agentPages.import.replace")}
									</label>
								</div>
							</Callout>
						)}
					</>
				)}
				<AgentErrorText error={error} />
			</div>
		</Dialog>
	);
}

/** 条目入口缺失时的提示键（不阻止导入）。 */
function entryWarning(
	kind: ImportKind,
	files: readonly BundleFile[],
):
	| "agentPages.import.warn.skillDescription"
	| "agentPages.import.warn.extensionEntry"
	| "agentPages.import.warn.toolEntry"
	| null {
	const has = (p: string) => files.some((f) => f.path === p);
	switch (kind) {
		case "skill":
			return skillDescription(files)
				? null
				: "agentPages.import.warn.skillDescription";
		case "extension":
			return has("index.ts") || has("index.js") || has("package.json")
				? null
				: "agentPages.import.warn.extensionEntry";
		case "tool":
			return has("index.ts") ? null : "agentPages.import.warn.toolEntry";
	}
}
