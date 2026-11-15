/**
 * Core `/omfg` 的候选 TTSR 规则卡（托盘 widget 与 confirm 对话框共用；解析见 `lib/omfg-rule.ts`）。
 * 规则名作标题，校验状态徽标，描述，触发条件逐条等宽小标签（正则原样），作用范围徽标，
 * 其余未知 frontmatter 字段进同一张键值网格，最后是 Markdown 正文。
 */
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
	type FrontmatterDoc,
	fieldValue,
	valueList,
} from "../../lib/frontmatter";
import type { RuleValidation } from "../../lib/omfg-rule";
import { FrontmatterValueView } from "../markdown/Frontmatter";
import { Markdown } from "../markdown/Markdown";
import { escapeAngles } from "../transcript/NoticeLine";
import { Badge } from "../ui/data";
import { Icon } from "../ui/icons";
import { Spinner } from "../ui/primitives";

/** 卡片自己排版的字段；其余字段进键值网格。 */
const OWN_FIELDS: Record<string, true> = {
	name: true,
	description: true,
	condition: true,
	astCondition: true,
	question: true,
	scope: true,
};

/** 触发条件小标签：等宽、保留空白、长正则任意处折行。 */
const CHIP =
	"rounded-xs border border-border bg-inset px-1.5 font-mono text-2xs leading-[18px] text-fg whitespace-pre-wrap [overflow-wrap:anywhere]";

function ValidationBadge({
	validation,
	label,
}: {
	validation: RuleValidation | null;
	label: string;
}): ReactNode {
	const { t } = useTranslation();
	switch (validation) {
		case "pending":
			return (
				<Badge>
					<span className="inline-flex items-center gap-1">
						<Spinner size={9} />
						{t("ttsrRule.validation.pending")}
					</span>
				</Badge>
			);
		case "matched":
			return (
				<Badge tone="ok" icon="checkCircle">
					{t("ttsrRule.validation.matched")}
				</Badge>
			);
		case "unconfirmed":
			return (
				<Badge tone="warn" icon="alert">
					{t("ttsrRule.validation.unconfirmed")}
				</Badge>
			);
		default:
			return <Badge title={label}>{label}</Badge>;
	}
}

export function RuleCard({
	rule,
	validation,
	label,
}: {
	rule: FrontmatterDoc;
	/** 托盘 widget 有校验状态；confirm 对话框里没有（不传则不显示徽标）。 */
	validation?: RuleValidation | null;
	/** 校验状态原文（Core 换了文案、`validation` 为 null 时显示）。 */
	label?: string;
}): ReactNode {
	const { t } = useTranslation();
	const name = fieldValue(rule, "name");
	const description = fieldValue(rule, "description");
	const conditions = valueList(fieldValue(rule, "condition"));
	const astConditions = valueList(fieldValue(rule, "astCondition"));
	const question = fieldValue(rule, "question");
	const scopes = valueList(fieldValue(rule, "scope"));
	const extra = rule.fields.filter((f) => !OWN_FIELDS[f.key]);
	const chips = (items: string[]) => (
		<span className="flex flex-wrap gap-1">
			{items.map((c, i) => (
				// biome-ignore lint/suspicious/noArrayIndexKey: 条件可能重复，无稳定 id
				<code key={i} className={CHIP}>
					{c}
				</code>
			))}
		</span>
	);
	const rows: { key: string; label: string; value: ReactNode }[] = [];
	if (conditions.length > 0)
		rows.push({
			key: "condition",
			label: t("ttsrRule.field.condition"),
			value: chips(conditions),
		});
	if (astConditions.length > 0)
		rows.push({
			key: "astCondition",
			label: t("ttsrRule.field.astCondition"),
			value: chips(astConditions),
		});
	if (question)
		rows.push({
			key: "question",
			label: t("ttsrRule.field.question"),
			value: <span className="whitespace-pre-wrap">{String(question)}</span>,
		});
	if (scopes.length > 0)
		rows.push({
			key: "scope",
			label: t("ttsrRule.field.scope"),
			value: (
				<span className="flex flex-wrap gap-1">
					{scopes.map((s, i) => (
						// biome-ignore lint/suspicious/noArrayIndexKey: 范围可能重复，无稳定 id
						<Badge key={i} title={s}>
							<span className="font-mono">{s}</span>
						</Badge>
					))}
				</span>
			),
		});
	extra.forEach((f, i) => {
		rows.push({
			key: `x${i}-${f.key}`,
			label: f.key,
			value: <FrontmatterValueView value={f.value} chipClass={CHIP} />,
		});
	});
	return (
		<section data-rule-card="" className="flex min-w-0 flex-col gap-2 text-xs">
			<header className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
				<span className="inline-flex min-w-0 grow items-center gap-1.5">
					<Icon
						name="fileText"
						size={13}
						aria-hidden
						className="shrink-0 text-faint"
					/>
					<span className="shrink-0 text-faint">{t("ttsrRule.kind")}</span>
					<h4 className="min-w-0 font-medium font-mono text-fg [overflow-wrap:anywhere]">
						{String(name ?? "")}
					</h4>
				</span>
				{label !== undefined && validation !== undefined && (
					<span data-validation={validation ?? "unknown"} className="contents">
						<ValidationBadge validation={validation} label={label} />
					</span>
				)}
			</header>
			{description && (
				<p className="whitespace-pre-wrap text-fg leading-5">
					{String(description)}
				</p>
			)}
			{rows.length > 0 && (
				<dl className="grid grid-cols-[max-content_minmax(0,1fr)] items-baseline gap-x-3 gap-y-1.5">
					{rows.map((r) => (
						<div key={r.key} data-field={r.key} className="contents">
							<dt className="text-faint">{r.label}</dt>
							<dd className="m-0 min-w-0 text-fg">{r.value}</dd>
						</div>
					))}
				</dl>
			)}
			{rule.body.trim() && (
				<div className="border-border border-t pt-2 text-muted [&_.md]:text-xs! [&_.md]:leading-5!">
					<Markdown text={escapeAngles(rule.body)} />
				</div>
			)}
		</section>
	);
}
