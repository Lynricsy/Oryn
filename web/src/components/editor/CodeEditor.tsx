/**
 * CodeMirror 6 编辑器封装。整个模块经 `React.lazy` 按需加载，不进首屏包；
 * 语言包再按文件扩展名经 `@codemirror/language-data` 懒加载。
 * `value` 变化且与当前文档不同时（重新加载）整体替换文档；`Mod-s` 触发 `onSave`。
 */
import { indentWithTab } from "@codemirror/commands";
import { LanguageDescription } from "@codemirror/language";
import { languages } from "@codemirror/language-data";
import { Compartment, EditorState, Prec } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { type ReactNode, useEffect, useRef } from "react";

export interface CodeEditorProps {
	value: string;
	/** 用于按扩展名选择语言；也作为无障碍名称 */
	path: string;
	readOnly?: boolean;
	onChange?(value: string): void;
	onSave?(): void;
}

const theme = EditorView.theme({
	"&": {
		height: "100%",
		fontSize: "12.5px",
		backgroundColor: "var(--inset)",
		color: "var(--text)",
	},
	".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.55" },
	".cm-content": { caretColor: "var(--brand)" },
	".cm-gutters": {
		backgroundColor: "var(--raised)",
		color: "var(--text-faint)",
		borderRight: "1px solid var(--line)",
	},
	".cm-activeLine, .cm-activeLineGutter": { backgroundColor: "var(--hover)" },
	"&.cm-focused": { outline: "none" },
	"&.cm-focused .cm-cursor": { borderLeftColor: "var(--brand)" },
	".cm-panels": { backgroundColor: "var(--panel)", color: "var(--text)" },
});

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
					basicSetup,
					keymap.of([indentWithTab]),
					theme,
					EditorView.lineWrapping,
					language.current.of([]),
					editable.current.of([
						EditorState.readOnly.of(readOnly),
						EditorView.editable.of(!readOnly),
					]),
					EditorView.contentAttributes.of({ "aria-label": path }),
					EditorView.updateListener.of((u) => {
						if (u.docChanged)
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
			v.dispatch({ changes: { from: 0, to: cur.length, insert: value } });
	}, [value]);

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
