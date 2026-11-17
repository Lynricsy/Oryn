/**
 * SSH 主机的网页终端（M18，懒加载，主包不含 xterm）：取 WS 票据后连 `/api/v1/ssh/hosts/{id}/terminal`，
 * 控制面代为登录目标机并开 PTY。帧约定：发给服务端的二进制帧是输入字节，文本帧是 `{"type":"resize","cols","rows"}`；
 * 服务端只发二进制帧（PTY 输出，按 UTF-8 流式解码）。断开后显示原因与「重新连接」，不自动重连。
 * mock 模式不建 WS：写一行已连接并把输入原样回显。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, unwrap } from "../../api/client";
import type { SshHost } from "../../api/ssh-queries";
import { createXterm } from "../terminal/xterm";
import { Button, describeError, Spinner } from "../ui/primitives";

type Status = "connecting" | "open" | "closed";

/** 缺省尺寸与服务端夹取范围一致（cols 20–500、rows 5–200）。 */
const DEFAULT_COLS = 120;
const DEFAULT_ROWS = 30;

function terminalUrl(
	hostId: string,
	ticket: string,
	cols: number,
	rows: number,
): string {
	const proto = location.protocol === "https:" ? "wss:" : "ws:";
	const q = new URLSearchParams({
		ticket,
		cols: String(cols),
		rows: String(rows),
	});
	return `${proto}//${location.host}/api/v1/ssh/hosts/${encodeURIComponent(hostId)}/terminal?${q}`;
}

/** 「重新连接」= 以新的 key 重建整个会话（终端与连接）。 */
export default function HostTerminal({ host }: { host: SshHost }): ReactNode {
	const [attempt, setAttempt] = useState(0);
	return (
		<TerminalSession
			key={attempt}
			host={host}
			onReconnect={() => setAttempt((n) => n + 1)}
		/>
	);
}

function TerminalSession({
	host,
	onReconnect,
}: {
	host: SshHost;
	onReconnect: () => void;
}): ReactNode {
	const { t } = useTranslation();
	const boxRef = useRef<HTMLDivElement>(null);
	const [status, setStatus] = useState<Status>("connecting");
	const [reason, setReason] = useState("");

	useEffect(() => {
		const box = boxRef.current;
		if (!box) return;
		const mock = import.meta.env.VITE_MOCK === "1";
		const encoder = new TextEncoder();
		const decoder = new TextDecoder("utf-8");
		let ws: WebSocket | null = null;
		let disposed = false;
		let timer: ReturnType<typeof setTimeout> | undefined;
		const term = createXterm({
			onInput: (data) => {
				if (mock) term.write(data === "\r" ? "\r\n" : data);
				else if (ws?.readyState === WebSocket.OPEN)
					ws.send(encoder.encode(data));
			},
			onFocus: () => {},
		});
		term.mount?.(box);
		const size = term.propose?.();
		if (size) term.resize(size.cols, size.rows);
		const ro = new ResizeObserver(() => {
			clearTimeout(timer);
			timer = setTimeout(() => {
				const next = term.propose?.();
				if (!next || (next.cols === term.cols && next.rows === term.rows))
					return;
				term.resize(next.cols, next.rows);
				if (ws?.readyState === WebSocket.OPEN)
					ws.send(
						JSON.stringify({
							type: "resize",
							cols: next.cols,
							rows: next.rows,
						}),
					);
			}, 100);
		});
		ro.observe(box);

		const closeReason = (code: number, raw: string): string => {
			switch (code) {
				case 1000:
					return t("sshPage.terminal.ended");
				case 4401:
					return t("sshPage.terminal.revoked");
				case 4429:
					return t("sshPage.terminal.tooMany");
				case 4502:
					return t("sshPage.terminal.unreachable", { reason: raw });
				default:
					return raw || t("sshPage.terminal.closedCode", { code });
			}
		};

		if (mock) {
			term.write(`${t("sshPage.terminal.mock", { name: host.name })}\r\n`);
			setStatus("open");
		} else {
			void (async () => {
				try {
					const { ticket } = await unwrap(api.POST("/api/v1/ws-ticket"));
					if (disposed) return;
					const sock = new WebSocket(
						terminalUrl(
							host.id,
							ticket,
							term.cols || DEFAULT_COLS,
							term.rows || DEFAULT_ROWS,
						),
					);
					ws = sock;
					sock.binaryType = "arraybuffer";
					sock.onopen = () => {
						setStatus("open");
						term.focus?.();
					};
					sock.onmessage = (e) => {
						if (e.data instanceof ArrayBuffer)
							term.write(
								decoder.decode(new Uint8Array(e.data), { stream: true }),
							);
					};
					sock.onclose = (e) => {
						if (disposed) return;
						setStatus("closed");
						setReason(closeReason(e.code, e.reason));
					};
				} catch (e) {
					if (disposed) return;
					setStatus("closed");
					setReason(describeError(e, t));
				}
			})();
		}
		return () => {
			disposed = true;
			clearTimeout(timer);
			ro.disconnect();
			ws?.close(1000);
			term.dispose();
		};
	}, [host.id, host.name, t]);

	return (
		<div className="flex flex-col gap-2">
			<div className="flex min-h-7 items-center gap-2 text-xs">
				{status === "connecting" && <Spinner size={12} />}
				<span className={status === "closed" ? "text-err" : "text-muted"}>
					{t(`sshPage.terminal.${status}`)}
					{status === "closed" && reason && `：${reason}`}
				</span>
				{status === "closed" && (
					<Button
						size="sm"
						icon="refresh"
						className="ml-auto"
						onClick={onReconnect}
					>
						{t("sshPage.terminal.reconnect")}
					</Button>
				)}
			</div>
			<div
				ref={boxRef}
				data-testid="ssh-terminal"
				className="h-[28rem] overflow-hidden rounded-md bg-inset p-1"
			/>
		</div>
	);
}
