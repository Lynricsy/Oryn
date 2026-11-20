/**
 * 解释卡：选区旁路提问（解释 / 翻译 / 就此提问 / 自定义动作）的结果，悬浮在选区旁。
 * 宽屏是锚在选区上下方的玻璃浮卡（打开时与窗口尺寸变化时定位一次，不跟随滚动；点卡外不关）；
 * 窄屏、或从别的对话框里发起时（浮卡会被那个对话框的遮罩盖住）改用 `Dialog` 外壳。
 * 内容复用 btw 对话框的各轮问答（`BtwTurns`），页脚可追问、停止、复制回答、转去 btw 对话框继续看。
 */
import {
	type CSSProperties,
	type FormEvent,
	type ReactNode,
	useLayoutEffect,
	useRef,
	useState,
} from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { useStore } from "zustand";
import { useContextTarget } from "../../lib/context-menu/registry";
import { anchorOf } from "../../lib/context-menu/selection";
import { copyWithToast } from "../../lib/context-menu/selection-groups";
import { AROUND_MAX, parseSideQuestion } from "../../lib/side-ask";
import { useBreakpoint } from "../../lib/viewport";
import { realtime } from "../../realtime/socket";
import {
	closeSideAsk,
	followUpSideAsk,
	runSideAsk,
	type SideAskCard,
	sideAskStore,
} from "../../state/side-ask";
import { useThread } from "../../state/stores";
import { uiStore } from "../../state/ui";
import {
	Button,
	Dialog,
	ErrorText,
	IconButton,
	Spinner,
	TextInput,
} from "../ui/primitives";
import {
	BtwTurns,
	copyableAnswer,
	latestTurn,
	useFollowTail,
	useSideActionLabel,
} from "./BtwTopic";

const EDGE = 8;
const GAP = 8;
/** 选区下方至少留这么高才放在下方（否则比较上下哪边空间大） */
const BELOW_MIN = 280;

export function SideAskHost({ threadId }: { threadId: string }): ReactNode {
	const card = useStore(sideAskStore, (s) => s.card);
	if (card?.threadId !== threadId) return null;
	return <CardView card={card} />;
}

function CardView({ card }: { card: SideAskCard }): ReactNode {
	const { t } = useTranslation();
	const compact = useBreakpoint() === "compact";
	const labelOf = useSideActionLabel();
	const { threadId } = card;
	const record = useThread(threadId, (s) =>
		card.recordId ? s.btw[card.recordId] : undefined,
	);
	const parsed = card.question ? parseSideQuestion(card.question) : null;
	const label = card.compose
		? t("sideAsk.action.ask")
		: parsed
			? labelOf(parsed)
			: "";
	const excerpt = card.compose?.text ?? parsed?.selected ?? "";
	const latest = record ? latestTurn(record) : undefined;
	const running = latest?.status === "running";
	const [ask, setAsk] = useState("");
	const [follow, setFollow] = useState("");

	const scrollRef = useRef<HTMLDivElement | null>(null);
	const onScroll = useFollowTail(
		scrollRef,
		1 + (record?.followUps?.length ?? 0),
		latest?.answer.length ?? 0,
	);
	const rootRef = useRef<HTMLDivElement | null>(null);
	// 卡里的回答再选字：AI 动作仍归这个线程
	const ctxRef = useContextTarget<HTMLDivElement>({
		askThreadId: threadId,
		selection: (sel) => {
			const root = rootRef.current ?? sel.root;
			return {
				threadId,
				text: sel.text,
				before: sel.before(root, AROUND_MAX),
				after: sel.after(root, AROUND_MAX),
				source: { kind: "side" },
				anchor: anchorOf(sel, 0, 0),
			};
		},
	});

	const submitAsk = (e: FormEvent) => {
		e.preventDefault();
		const question = ask.trim();
		if (question && card.compose)
			void runSideAsk({ kind: "ask", question }, card.compose);
	};
	const submitFollow = (e: FormEvent) => {
		e.preventDefault();
		if (!follow.trim() || running) return;
		void followUpSideAsk(follow);
		setFollow("");
	};

	const body = card.compose ? (
		<form onSubmit={submitAsk} className="flex gap-2 px-4 py-3">
			<TextInput
				autoFocus
				aria-label={t("sideAsk.askPlaceholder")}
				placeholder={t("sideAsk.askPlaceholder")}
				value={ask}
				onChange={(e) => setAsk(e.target.value)}
			/>
			<Button type="submit" variant="primary" disabled={!ask.trim()}>
				{t("common.send")}
			</Button>
		</form>
	) : (
		<div
			ref={scrollRef}
			onScroll={onScroll}
			className="min-h-0 flex-1 overflow-y-auto px-4 py-3"
		>
			{record ? (
				<BtwTurns threadId={threadId} record={record} />
			) : (
				!card.error && <Spinner />
			)}
			<ErrorText error={card.error} />
		</div>
	);

	const footer = !card.compose && record && (
		<div className="flex flex-col gap-2 border-[var(--glass-line)] border-t px-4 py-3">
			<form onSubmit={submitFollow}>
				<TextInput
					aria-label={t("btw.followUpPlaceholder")}
					placeholder={t("btw.followUpPlaceholder")}
					value={follow}
					disabled={running}
					onChange={(e) => setFollow(e.target.value)}
				/>
			</form>
			<div className="flex flex-wrap items-center gap-2">
				{running && (
					<Button
						size="sm"
						icon="stop"
						onClick={() =>
							void realtime
								.cmd(threadId, { type: "btw_cancel", recordId: record.id })
								.catch(() => undefined)
						}
					>
						{t("sideAsk.stop")}
					</Button>
				)}
				<Button
					size="sm"
					icon="copy"
					disabled={copyableAnswer(record) === undefined}
					onClick={() => {
						const text = copyableAnswer(record);
						if (text !== undefined) void copyWithToast(text);
					}}
				>
					{t("btw.copyAnswer")}
				</Button>
				<Button
					size="sm"
					variant="ghost"
					className="ml-auto"
					onClick={() => {
						uiStore.getState().open({ kind: "btw", recordId: record.id });
						closeSideAsk({ keepRunning: true });
					}}
				>
					{t("sideAsk.openInBtw")}
				</Button>
			</div>
		</div>
	);

	const excerptLine = excerpt && (
		<p className="truncate text-faint text-xs" title={excerpt}>
			{excerpt.replace(/\s+/g, " ").trim()}
		</p>
	);

	const content = (
		<div
			data-side-ask=""
			ref={(el) => {
				rootRef.current = el;
				ctxRef(el);
			}}
			className="flex min-h-0 flex-1 flex-col"
		>
			{body}
			{footer}
		</div>
	);

	if (compact || card.overModal)
		return (
			<Dialog open onClose={() => closeSideAsk()} title={label}>
				<div className="-mx-5 -mt-1 flex max-h-[60dvh] flex-col">
					<div className="px-5">{excerptLine}</div>
					{content}
				</div>
			</Dialog>
		);
	return (
		<FloatingCard anchor={card.anchor} label={label}>
			<div className="flex items-start gap-2 px-4 pt-3">
				<div className="min-w-0 flex-1">
					<h2 className="truncate font-semibold text-fg text-sm">{label}</h2>
					{excerptLine}
				</div>
				<IconButton
					icon="close"
					size={13}
					box="sm"
					label={t("common.close")}
					onClick={() => closeSideAsk()}
				/>
			</div>
			{content}
		</FloatingCard>
	);
}

function FloatingCard({
	anchor,
	label,
	children,
}: {
	anchor: SideAskCard["anchor"];
	label: string;
	children: ReactNode;
}): ReactNode {
	const ref = useRef<HTMLDivElement>(null);
	const [pos, setPos] = useState<CSSProperties>({
		left: anchor.left,
		top: anchor.bottom + GAP,
	});
	// 打开时与窗口尺寸变化时定位：下方空间够或比上方大就放下方，水平居中于选区并夹进视口
	useLayoutEffect(() => {
		const place = () => {
			const width = ref.current?.offsetWidth ?? 0;
			const vh = window.innerHeight;
			const below =
				vh - anchor.bottom >= BELOW_MIN || vh - anchor.bottom >= anchor.top;
			const left = Math.min(
				Math.max((anchor.left + anchor.right) / 2 - width / 2, EDGE),
				Math.max(EDGE, window.innerWidth - width - EDGE),
			);
			setPos(
				below
					? { left, top: anchor.bottom + GAP }
					: { left, bottom: vh - anchor.top + GAP },
			);
		};
		place();
		window.addEventListener("resize", place);
		return () => window.removeEventListener("resize", place);
	}, [anchor]);
	return createPortal(
		<div
			ref={ref}
			role="dialog"
			aria-label={label}
			data-testid="side-ask-card"
			data-quote-affordance=""
			className="glass pop-in fixed z-40 flex max-h-[min(60dvh,32rem)] w-[min(26rem,calc(100vw-2rem))] flex-col rounded-lg shadow-lg"
			style={pos}
			onKeyDown={(e) => {
				if (e.key !== "Escape") return;
				e.stopPropagation();
				closeSideAsk();
			}}
		>
			{children}
		</div>,
		document.body,
	);
}
