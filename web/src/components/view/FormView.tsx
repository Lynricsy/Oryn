/**
 * 视图的 `form` 块。初值取 `defaultValue`；提交前只校验必填与数值范围；提交后（含在途）锁定并显示提交的值，
 * 提交失败时解锁并显示错误。
 */
import type { TFunction } from "i18next";
import { type FormEvent, type ReactNode, useId, useState } from "react";
import { useTranslation } from "react-i18next";
import { formatFormAnswer } from "../../lib/view/answer";
import {
	draftFromValues,
	type FieldError,
	type FormDraft,
	formValues,
	initialFormDraft,
	validateForm,
} from "../../lib/view/form";
import type { FieldValue, FormBlock, FormField } from "../../lib/view/spec";
import { Icon } from "../ui/icons";
import {
	Button,
	Checkbox,
	describeError,
	Radio,
	Select,
	Spinner,
	TextArea,
	TextInput,
	Toggle,
} from "../ui/primitives";
import { useView } from "./context";

const GRID: Record<FormBlock["columns"], string> = {
	1: "grid-cols-1",
	2: "grid-cols-1 @md:grid-cols-2",
	3: "grid-cols-1 @md:grid-cols-2 @xl:grid-cols-3",
};

const INPUT_TYPE: Partial<Record<FormField["kind"], string>> = {
	text: "text",
	email: "email",
	url: "url",
	date: "date",
	time: "time",
	number: "number",
};

interface FieldProps {
	field: FormField;
	value: FieldValue;
	onChange: (v: FieldValue) => void;
	disabled: boolean;
	error?: string;
	id: string;
}

function errorText(e: FieldError, t: TFunction): string {
	switch (e.kind) {
		case "required":
			return t("view.form.errRequired");
		case "number":
			return t("view.form.errNumber");
		case "min":
			return t("view.form.errMin", { min: e.min });
		case "max":
			return t("view.form.errMax", { max: e.max });
	}
}

function LabelText({ field }: { field: FormField }): ReactNode {
	const { t } = useTranslation();
	return (
		<>
			{field.label}
			{field.required && (
				<span className="text-faint" title={t("view.form.required")}>
					{" *"}
				</span>
			)}
		</>
	);
}

/** 字段下方的说明与错误。 */
function FieldFoot({
	field,
	error,
	id,
}: {
	field: FormField;
	error?: string;
	id: string;
}): ReactNode {
	return (
		<>
			{field.description && (
				<span id={`${id}-d`} className="text-faint text-xs">
					{field.description}
				</span>
			)}
			{error && (
				<span id={`${id}-e`} className="text-err text-xs">
					{error}
				</span>
			)}
		</>
	);
}

function describedBy(field: FormField, id: string, error?: string) {
	const ids = [field.description && `${id}-d`, error && `${id}-e`].filter(
		Boolean,
	);
	return ids.length ? ids.join(" ") : undefined;
}

/** 选项组（单选 / 多选）：fieldset + legend，选项横排，放不下换行。 */
function ChoiceGroup({
	field,
	value,
	onChange,
	disabled,
	error,
	id,
}: FieldProps) {
	const options = field.options ?? [];
	const multi = field.kind === "multiselect";
	const selected = multi
		? new Set(Array.isArray(value) ? value : [])
		: new Set(typeof value === "string" ? [value] : []);
	return (
		<fieldset
			className="m-0 flex min-w-0 flex-col gap-[7px] border-0 p-0"
			aria-describedby={describedBy(field, id, error)}
			aria-invalid={error ? true : undefined}
			disabled={disabled}
		>
			<legend className="mb-[7px] p-0 font-medium text-fg text-sm">
				<LabelText field={field} />
			</legend>
			<div className="flex flex-wrap gap-x-4 gap-y-2">
				{options.map((o, i) => {
					const on = selected.has(o.value);
					// 多选按选项顺序保存，不按点击顺序
					const toggle = () =>
						onChange(
							!multi
								? o.value
								: options
										.filter((x) =>
											x.value === o.value ? !on : selected.has(x.value),
										)
										.map((x) => x.value),
						);
					const optionId = `${id}-o${i}`;
					return (
						<label
							key={o.value}
							htmlFor={optionId}
							className="inline-flex min-w-0 cursor-pointer items-center gap-2 text-fg text-sm has-disabled:cursor-default"
						>
							{multi ? (
								<Checkbox id={optionId} checked={on} onChange={toggle} />
							) : (
								<Radio id={optionId} name={id} checked={on} onChange={toggle} />
							)}
							<span className="min-w-0 break-words">{o.label}</span>
						</label>
					);
				})}
			</div>
			<FieldFoot field={field} error={error} id={id} />
		</fieldset>
	);
}

function FieldControl(props: FieldProps): ReactNode {
	const { field, value, onChange, disabled, error, id } = props;
	const { t, i18n } = useTranslation();
	if (field.kind === "radio" || field.kind === "multiselect")
		return <ChoiceGroup {...props} />;
	const aria = {
		"aria-describedby": describedBy(field, id, error),
		"aria-invalid": error ? true : undefined,
		"aria-required": field.required || undefined,
	};
	if (field.kind === "checkbox")
		return (
			<div className="flex flex-col gap-[7px]">
				<label
					htmlFor={id}
					className="inline-flex cursor-pointer items-center gap-2 text-fg text-sm has-disabled:cursor-default"
				>
					<Checkbox
						id={id}
						checked={value === true}
						disabled={disabled}
						onChange={(e) => onChange(e.target.checked)}
						{...aria}
					/>
					<span className="min-w-0 break-words">
						<LabelText field={field} />
					</span>
				</label>
				<FieldFoot field={field} error={error} id={id} />
			</div>
		);
	if (field.kind === "switch")
		return (
			<div className="flex flex-col gap-[7px]">
				<Toggle
					checked={value === true}
					onChange={onChange}
					disabled={disabled}
					label={<LabelText field={field} />}
				/>
				<FieldFoot field={field} error={error} id={id} />
			</div>
		);

	let control: ReactNode;
	if (field.kind === "select") {
		control = (
			<Select
				id={id}
				value={typeof value === "string" ? value : ""}
				options={field.options ?? []}
				onChange={(v) => onChange(v)}
				placeholder={field.placeholder ?? t("view.form.choose")}
				disabled={disabled}
				aria-labelledby={`${id}-l`}
				aria-describedby={aria["aria-describedby"]}
				aria-invalid={aria["aria-invalid"]}
			/>
		);
	} else if (field.kind === "slider") {
		const n = typeof value === "number" ? value : (field.min ?? 0);
		control = (
			<div className="flex items-center gap-3">
				<input
					id={id}
					type="range"
					min={field.min ?? 0}
					max={field.max ?? 100}
					step={field.step ?? 1}
					value={n}
					disabled={disabled}
					onChange={(e) => onChange(Number(e.target.value))}
					className="h-5 min-w-0 flex-1 cursor-pointer accent-[var(--brand)] disabled:cursor-default disabled:opacity-60"
					{...aria}
				/>
				<output
					htmlFor={id}
					className="min-w-10 shrink-0 text-right text-fg text-sm tabular-nums"
				>
					{new Intl.NumberFormat(i18n.language).format(n)}
				</output>
			</div>
		);
	} else if (field.kind === "textarea") {
		control = (
			<TextArea
				id={id}
				mono={false}
				rows={3}
				value={typeof value === "string" ? value : ""}
				placeholder={field.placeholder}
				disabled={disabled}
				onChange={(e) => onChange(e.target.value)}
				className="resize-y"
				{...aria}
			/>
		);
	} else {
		control = (
			<TextInput
				id={id}
				type={INPUT_TYPE[field.kind] ?? "text"}
				inputMode={field.kind === "number" ? "decimal" : undefined}
				min={field.kind === "number" ? field.min : undefined}
				max={field.kind === "number" ? field.max : undefined}
				step={field.kind === "number" ? (field.step ?? "any") : undefined}
				value={typeof value === "string" ? value : ""}
				placeholder={field.placeholder}
				disabled={disabled}
				onChange={(e) => onChange(e.target.value)}
				{...aria}
			/>
		);
	}
	return (
		<div className="flex min-w-0 flex-col gap-[7px]">
			<label
				id={`${id}-l`}
				htmlFor={id}
				className="font-medium text-fg text-sm"
			>
				<LabelText field={field} />
			</label>
			{control}
			<FieldFoot field={field} error={error} id={id} />
		</div>
	);
}

interface Local {
	status: "pending" | "done";
	values: Record<string, FieldValue>;
}

export function FormView({ block }: { block: FormBlock }): ReactNode {
	const { t } = useTranslation();
	const view = useView();
	const baseId = useId();
	// 只记主人改过的字段：流式中后到的字段与默认值仍按规格取
	const [edits, setEdits] = useState<FormDraft>({});
	const [errors, setErrors] = useState<Record<string, FieldError>>({});
	const [local, setLocal] = useState<Local | null>(null);
	const [failure, setFailure] = useState<unknown>(null);

	const answer = view.answers.get(block.path);
	const lockedValues = answer?.kind === "form" ? answer.values : local?.values;
	const locked = lockedValues !== undefined;
	const draft = locked
		? draftFromValues(block, lockedValues)
		: { ...initialFormDraft(block), ...edits };
	const canSubmit = !locked && view.interactive && !!view.onAnswer;
	const pending = local?.status === "pending" && answer === undefined;

	const submit = async (e: FormEvent) => {
		e.preventDefault();
		if (!canSubmit || !view.onAnswer) return;
		const found = validateForm(block, draft);
		setErrors(found);
		const first = block.fields.find((f) => found[f.name]);
		if (first) {
			document.getElementById(`${baseId}-${first.name}`)?.focus();
			return;
		}
		const values = formValues(block, draft);
		setFailure(null);
		setLocal({ status: "pending", values });
		try {
			await view.onAnswer(
				{ block: block.path, kind: "form", values },
				formatFormAnswer(block, values),
			);
			setLocal({ status: "done", values });
		} catch (err) {
			setLocal(null);
			setFailure(err);
		}
	};

	return (
		<form
			noValidate
			onSubmit={submit}
			aria-label={block.title}
			className="@container flex min-w-0 flex-col gap-3"
		>
			{(block.title || block.description) && (
				<header className="flex flex-col gap-0.5">
					{block.title && (
						<h4 className="m-0 font-semibold text-fg text-sm">{block.title}</h4>
					)}
					{block.description && (
						<p className="m-0 text-muted text-sm">{block.description}</p>
					)}
				</header>
			)}
			<div className={`grid gap-x-4 gap-y-3.5 ${GRID[block.columns]}`}>
				{block.fields.map((f) => {
					const err = errors[f.name];
					return (
						<div
							key={f.name}
							className={`min-w-0 ${f.span === "full" ? "col-span-full" : ""}`}
						>
							<FieldControl
								field={f}
								id={`${baseId}-${f.name}`}
								value={draft[f.name] ?? null}
								disabled={!canSubmit}
								error={err && !locked ? errorText(err, t) : undefined}
								onChange={(v) => {
									setEdits((prev) => ({ ...prev, [f.name]: v }));
									if (err)
										setErrors((prev) => {
											const { [f.name]: _, ...rest } = prev;
											return rest;
										});
								}}
							/>
						</div>
					);
				})}
			</div>
			<div className="flex min-h-8 flex-wrap items-center gap-x-3 gap-y-1.5">
				{locked ? (
					<span
						role="status"
						className="inline-flex items-center gap-1.5 text-muted text-xs"
					>
						{pending ? (
							<>
								<Spinner size={12} />
								{t("view.form.submitting")}
							</>
						) : (
							<>
								<Icon name="checkCircle" size={14} className="text-ok" />
								{t("view.form.submitted")}
							</>
						)}
					</span>
				) : (
					<Button type="submit" variant="accent" disabled={!canSubmit}>
						{block.submitLabel ?? t("view.form.submit")}
					</Button>
				)}
				{failure !== null && !locked && (
					<span role="alert" className="text-err text-xs">
						{t("view.form.failed", { error: describeError(failure, t) })}
					</span>
				)}
			</div>
		</form>
	);
}
