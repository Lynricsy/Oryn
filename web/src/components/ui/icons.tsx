/** 内联 SVG 图标（24×24 描边风格），避免引入图标库。 */
import type { ReactNode, SVGProps } from "react";

export type IconName =
	| "plus"
	| "search"
	| "menu"
	| "more"
	| "send"
	| "stop"
	| "settings"
	| "panel"
	| "chevronDown"
	| "chevronRight"
	| "close"
	| "pin"
	| "archive"
	| "image"
	| "fork"
	| "tree"
	| "cpu"
	| "check"
	| "alert"
	| "clock"
	| "terminal"
	| "list"
	| "trash"
	| "copy"
	| "edit"
	| "play"
	| "pause"
	| "bolt"
	| "box"
	| "bug"
	| "folder"
	| "key"
	| "device"
	| "file"
	| "info"
	| "message"
	| "target"
	| "user"
	| "back"
	| "sparkle";

const PATHS: Record<IconName, ReactNode> = {
	plus: <path d="M12 5v14M5 12h14" />,
	search: (
		<>
			<circle cx="11" cy="11" r="7" />
			<path d="m20 20-3.5-3.5" />
		</>
	),
	menu: <path d="M4 6h16M4 12h16M4 18h16" />,
	more: (
		<>
			<circle cx="5" cy="12" r="1" />
			<circle cx="12" cy="12" r="1" />
			<circle cx="19" cy="12" r="1" />
		</>
	),
	send: <path d="M12 19V5M5 12l7-7 7 7" />,
	stop: <rect x="6" y="6" width="12" height="12" rx="2" />,
	settings: (
		<>
			<circle cx="12" cy="12" r="3" />
			<path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" />
		</>
	),
	panel: (
		<>
			<rect x="3" y="4" width="18" height="16" rx="2" />
			<path d="M15 4v16" />
		</>
	),
	chevronDown: <path d="m6 9 6 6 6-6" />,
	chevronRight: <path d="m9 6 6 6-6 6" />,
	close: <path d="M6 6l12 12M18 6 6 18" />,
	pin: (
		<path d="M12 17v5M9 10.8a2 2 0 0 1-1.1 1.8l-1.8.9A2 2 0 0 0 5 15.2V17h14v-1.8a2 2 0 0 0-1.1-1.8l-1.8-.9a2 2 0 0 1-1.1-1.8V7h1a2 2 0 0 0 0-4H8a2 2 0 0 0 0 4h1z" />
	),
	archive: (
		<>
			<rect x="3" y="4" width="18" height="4" rx="1" />
			<path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4" />
		</>
	),
	image: (
		<>
			<rect x="3" y="3" width="18" height="18" rx="2" />
			<circle cx="9" cy="9" r="2" />
			<path d="m21 15-3.1-3.1a2 2 0 0 0-2.8 0L6 21" />
		</>
	),
	fork: (
		<>
			<circle cx="6" cy="5" r="2" />
			<circle cx="18" cy="5" r="2" />
			<circle cx="12" cy="19" r="2" />
			<path d="M6 7v1a4 4 0 0 0 4 4h4a4 4 0 0 0 4-4V7M12 12v5" />
		</>
	),
	tree: (
		<>
			<circle cx="6" cy="6" r="2" />
			<circle cx="6" cy="18" r="2" />
			<circle cx="18" cy="12" r="2" />
			<path d="M6 8v8M8 6h4a4 4 0 0 1 4 4v0" />
		</>
	),
	cpu: (
		<>
			<rect x="6" y="6" width="12" height="12" rx="2" />
			<path d="M9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4" />
		</>
	),
	check: <path d="M20 6 9 17l-5-5" />,
	alert: (
		<>
			<circle cx="12" cy="12" r="9" />
			<path d="M12 8v4M12 16h.01" />
		</>
	),
	clock: (
		<>
			<circle cx="12" cy="12" r="9" />
			<path d="M12 7v5l3 2" />
		</>
	),
	terminal: <path d="m5 7 5 5-5 5M12 19h7" />,
	list: <path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01" />,
	trash: <path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v6M14 11v6" />,
	copy: (
		<>
			<rect x="8" y="8" width="13" height="13" rx="2" />
			<path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3" />
		</>
	),
	edit: <path d="M12 20h9M16.5 3.5a2.1 2.1 0 1 1 3 3L7 19l-4 1 1-4z" />,
	play: <path d="m7 4 13 8-13 8z" />,
	pause: <path d="M8 4v16M16 4v16" />,
	bolt: <path d="M13 2 4 14h7l-1 8 9-12h-7z" />,
	box: (
		<>
			<path d="M21 8 12 3 3 8v8l9 5 9-5z" />
			<path d="m3 8 9 5 9-5M12 13v8" />
		</>
	),
	bug: (
		<>
			<rect x="8" y="6" width="8" height="14" rx="4" />
			<path d="M12 20v-9M8 10H4M20 10h-4M8 15H4M20 15h-4M9 6l-1-3M15 6l1-3" />
		</>
	),
	folder: (
		<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
	),
	key: (
		<>
			<circle cx="8" cy="15" r="4" />
			<path d="m10.8 12.2 9.2-9.2M17 6l3 3M15 8l2 2" />
		</>
	),
	device: (
		<>
			<rect x="3" y="4" width="18" height="12" rx="2" />
			<path d="M8 20h8M12 16v4" />
		</>
	),
	file: (
		<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9zM14 3v6h6" />
	),
	info: (
		<>
			<circle cx="12" cy="12" r="9" />
			<path d="M12 16v-4M12 8h.01" />
		</>
	),
	message: (
		<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
	),
	target: (
		<>
			<circle cx="12" cy="12" r="9" />
			<circle cx="12" cy="12" r="5" />
			<circle cx="12" cy="12" r="1" />
		</>
	),
	user: (
		<>
			<circle cx="12" cy="8" r="4" />
			<path d="M4 21a8 8 0 0 1 16 0" />
		</>
	),
	back: <path d="M19 12H5M12 19l-7-7 7-7" />,
	sparkle: (
		<path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9zM19 16l.8 2.2L22 19l-2.2.8L19 22l-.8-2.2L16 19l2.2-.8z" />
	),
};

export interface IconProps extends SVGProps<SVGSVGElement> {
	name: IconName;
	size?: number;
}

export function Icon({ name, size = 16, ...rest }: IconProps): ReactNode {
	return (
		<svg
			width={size}
			height={size}
			viewBox="0 0 24 24"
			fill="none"
			stroke="currentColor"
			strokeWidth={1.75}
			strokeLinecap="round"
			strokeLinejoin="round"
			aria-hidden="true"
			{...rest}
		>
			{PATHS[name]}
		</svg>
	);
}
