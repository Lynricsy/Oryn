/**
 * CodeMirror 6 编辑器封装。整个模块经 `React.lazy` 按需加载，不进首屏包；
 * 语言包再按文件扩展名经 `@codemirror/language-data` 懒加载。
 * `value` 变化且与当前文档不同时（重新加载）整体替换文档，这类替换不触发 `onChange`；`Mod-s` 触发 `onSave`。
 * CodeMirror 自带界面（搜索 / 替换面板、跳转行、折叠提示等）的文案经 `EditorState.phrases` 跟随界面语言。
 */
import { indentWithTab } from "@codemirror/commands";
import {
	HighlightStyle,
	LanguageDescription,
	syntaxHighlighting,
} from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import {
	Annotation,
	Compartment,
	EditorState,
	type Extension,
	Prec,
} from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { basicSetup } from "codemirror";
import type { TFunction } from "i18next";
import { type ReactNode, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";

export interface CodeEditorProps {
	value: string;
	/** 用于按扩展名选择语言；也作为无障碍名称 */
	path: string;
	readOnly?: boolean;
	onChange?(value: string): void;
	onSave?(): void;
}

/** CodeMirror 内置文案（英文原文）→ `editor.phrases.*` 键。`$` 是 CodeMirror 的参数占位符。 */
export const PHRASE_KEYS = {
	Find: "find",
	Replace: "replace",
	next: "next",
	previous: "previous",
	all: "all",
	"match case": "matchCase",
	regexp: "regexp",
	"by word": "byWord",
	replace: "replaceOne",
	"replace all": "replaceAll",
	close: "close",
	"current match": "currentMatch",
	"on line": "onLine",
	"replaced $ matches": "replacedMatches",
	"replaced match on line $": "replacedMatchOnLine",
	"Go to line": "goToLine",
	go: "go",
	"Folded lines": "foldedLines",
	"Unfolded lines": "unfoldedLines",
	to: "to",
	"folded code": "foldedCode",
	unfold: "unfold",
	"Fold line": "foldLine",
	"Unfold line": "unfoldLine",
	"Control character": "controlCharacter",
	Completions: "completions",
	Diagnostics: "diagnostics",
	"No diagnostics": "noDiagnostics",
	"Selection deleted": "selectionDeleted",
} as const;

function phrasesFor(t: TFunction): Extension {
	const out: Record<string, string> = {};
	for (const [phrase, key] of Object.entries(PHRASE_KEYS))
		out[phrase] = t(`editor.phrases.${key}`);
	return EditorState.phrases.of(out);
}

/** 由外部 `value` 引起的整体替换（不是用户编辑）。 */
const external = Annotation.define<boolean>();

// 只引原始令牌变量（--r-* / --mono / --sans / --sh-*），Tailwind 的 inline 主题变量不保证输出到 :root
const FIELD = {
	border: "1px solid var(--line)",
	borderRadius: "var(--r-md)",
	fontFamily: "var(--sans)",
	fontSize: "12px",
};

const theme = EditorView.theme({
	"&": {
		height: "100%",
		fontSize: "12.5px",
		backgroundColor: "var(--inset)",
		color: "var(--text)",
	},
	".cm-scroller": { fontFamily: "var(--mono)", lineHeight: "1.55" },
	".cm-content": { caretColor: "var(--brand)" },
	".cm-gutters": {
		backgroundColor: "var(--raised)",
		color: "var(--text-faint)",
		borderRight: "1px solid var(--line)",
	},
	".cm-activeLine, .cm-activeLineGutter": { backgroundColor: "var(--hover)" },
	"&.cm-focused": { outline: "none" },
	"&.cm-focused .cm-cursor": { borderLeftColor: "var(--brand)" },
	"&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection":
		{ backgroundColor: "color-mix(in oklab, var(--brand) 22%, transparent)" },
	".cm-searchMatch": {
		backgroundColor: "color-mix(in oklab, var(--caution) 28%, transparent)",
		outline: "none",
		borderRadius: "var(--r-xs)",
	},
	".cm-searchMatch.cm-searchMatch-selected": {
		backgroundColor: "color-mix(in oklab, var(--caution) 55%, transparent)",
	},
	".cm-selectionMatch": {
		backgroundColor: "color-mix(in oklab, var(--brand) 12%, transparent)",
	},
	".cm-foldPlaceholder": {
		backgroundColor: "var(--raised)",
		border: "1px solid var(--line)",
		borderRadius: "var(--r-xs)",
		color: "var(--text-muted)",
	},
	".cm-panels": {
		backgroundColor: "var(--panel)",
		color: "var(--text)",
		fontFamily: "var(--sans)",
	},
	".cm-panels.cm-panels-top": { borderBottom: "1px solid var(--line)" },
	".cm-panels.cm-panels-bottom": { borderTop: "1px solid var(--line)" },
	".cm-panel.cm-search": { padding: "6px 28px 6px 8px" },
	".cm-panel.cm-search label": {
		fontSize: "12px",
		color: "var(--text-muted)",
	},
	".cm-textfield": {
		...FIELD,
		backgroundColor: "var(--inset)",
		color: "var(--text)",
		padding: "3px 8px",
	},
	".cm-textfield:focus-visible": {
		outline: "2px solid var(--ring)",
		outlineOffset: "0",
	},
	".cm-button": {
		...FIELD,
		backgroundImage: "none",
		backgroundColor: "var(--panel)",
		color: "var(--text)",
		padding: "3px 10px",
		boxShadow: "var(--sh-xs)",
	},
	".cm-button:hover": { backgroundColor: "var(--hover)" },
	".cm-button:active": {
		backgroundImage: "none",
		backgroundColor: "var(--active)",
	},
	".cm-tooltip": {
		backgroundColor: "var(--panel)",
		color: "var(--text)",
		border: "1px solid var(--line)",
		borderRadius: "var(--r-md)",
		boxShadow: "var(--sh-lg)",
		overflow: "hidden",
	},
	".cm-tooltip-autocomplete > ul": { fontFamily: "var(--mono)" },
	".cm-tooltip-autocomplete > ul > li[aria-selected]": {
		backgroundColor: "var(--active)",
		color: "var(--text)",
	},
});

/** 语法配色走 `--syn-*` 令牌,随明暗主题切换;覆盖 basicSetup 自带的 fallback 配色。 */
const highlight = HighlightStyle.define([
	{
		tag: [
			tags.keyword,
			tags.operatorKeyword,
			tags.modifier,
			tags.controlKeyword,
		],
		color: "var(--syn-keyword)",
	},
	{
		tag: [tags.string, tags.special(tags.string), tags.regexp, tags.inserted],
		color: "var(--syn-string)",
	},
	{
		tag: [tags.number, tags.bool, tags.null, tags.atom],
		color: "var(--syn-number)",
	},
	{
		tag: [
			tags.function(tags.variableName),
			tags.function(tags.propertyName),
			tags.definition(tags.function(tags.variableName)),
			tags.heading,
		],
		color: "var(--syn-title)",
	},
	{
		tag: [tags.typeName, tags.className, tags.namespace, tags.tagName],
		color: "var(--syn-type)",
	},
	{ tag: [tags.attributeName, tags.propertyName], color: "var(--syn-type)" },
	{
		tag: [tags.comment, tags.meta],
		color: "var(--text-faint)",
		fontStyle: "italic",
	},
	{ tag: tags.deleted, color: "var(--bad)" },
	{ tag: tags.invalid, color: "var(--bad)" },
	{ tag: tags.link, textDecoration: "underline" },
	{ tag: tags.strong, fontWeight: "600" },
	{ tag: tags.emphasis, fontStyle: "italic" },
]);

export default function CodeEditor({
	value,
	path,
	readOnly = false,
	onChange,
	onSave,
}: CodeEditorProps): ReactNode {
	const host = useRef<HTMLDivElement>(null);
	const view = useRef<EditorView | null>(null);
	const language = useRef(new Compartment());
	const editable = useRef(new Compartment());
	const phrases = useRef(new Compartment());
	const { t, i18n } = useTranslation();
	// 回调经 ref 读取，避免重建编辑器
	const handlers = useRef({ onChange, onSave });
	handlers.current = { onChange, onSave };

	// biome-ignore lint/correctness/useExhaustiveDependencies: 编辑器只在挂载时创建；value / readOnly / path 由下面的副作用同步
	useEffect(() => {
		const parent = host.current;
		if (!parent) return;
		const v = new EditorView({
			parent,
			state: EditorState.create({
				doc: value,
				extensions: [
					Prec.highest(
						keymap.of([
							{
								key: "Mod-s",
								preventDefault: true,
								run: () => {
									handlers.current.onSave?.();
									return true;
								},
							},
						]),
					),
					phrases.current.of(phrasesFor(t)),
					basicSetup,
					keymap.of([indentWithTab]),
					theme,
					syntaxHighlighting(highlight),
					EditorView.lineWrapping,
					language.current.of([]),
					editable.current.of([
						EditorState.readOnly.of(readOnly),
						EditorView.editable.of(!readOnly),
					]),
					EditorView.contentAttributes.of({ "aria-label": path }),
					EditorView.updateListener.of((u) => {
						if (
							u.docChanged &&
							!u.transactions.some((tr) => tr.annotation(external))
						)
							handlers.current.onChange?.(u.state.doc.toString());
					}),
				],
			}),
		});
		view.current = v;
		return () => {
			v.destroy();
			view.current = null;
		};
	}, []);

	useEffect(() => {
		const v = view.current;
		if (!v) return;
		const cur = v.state.doc.toString();
		if (cur !== value)
			v.dispatch({
				changes: { from: 0, to: cur.length, insert: value },
				annotations: external.of(true),
			});
	}, [value]);

	// 界面语言切换：重新配置内置文案（已打开的搜索面板随之更新）
	// biome-ignore lint/correctness/useExhaustiveDependencies: t 随 language 变化
	useEffect(() => {
		view.current?.dispatch({
			effects: phrases.current.reconfigure(phrasesFor(t)),
		});
	}, [i18n.language]);

	useEffect(() => {
		view.current?.dispatch({
			effects: editable.current.reconfigure([
				EditorState.readOnly.of(readOnly),
				EditorView.editable.of(!readOnly),
			]),
		});
	}, [readOnly]);

	useEffect(() => {
		let cancelled = false;
		const desc = LanguageDescription.matchFilename(languages, path);
		view.current?.dispatch({ effects: language.current.reconfigure([]) });
		if (!desc) return;
		void desc.load().then(
			(support) => {
				if (!cancelled)
					view.current?.dispatch({
						effects: language.current.reconfigure(support),
					});
			},
			() => {
				// 语言包加载失败时保持纯文本
			},
		);
		return () => {
			cancelled = true;
		};
	}, [path]);

	return (
		<div
			ref={host}
			data-testid="code-editor"
			className="h-full min-h-0 overflow-hidden"
		/>
	);
}
