/**
 * 3D 模型预览（glb / gltf / stl / obj / ply）：three.js 渲染 + OrbitControls 旋转缩放。
 * - 加载后按包围球自动取景；环境光 + 跟随相机的平行光；只含几何的格式套默认材质（有顶点色时启用 vertexColors）。
 * - 按需渲染：只在控制器变化、尺寸变化、切换线框时重绘，没有常驻渲染循环。
 * - CSP 下 `connect-src 'self'` 不允许 fetch data:/blob:，因此 glTF 的内嵌缓冲区在本地解码，纹理改用 <img>
 *   方式的 TextureLoader（`img-src` 允许 data:/blob:）；不支持外部引用的缓冲区，外部纹理以白色占位并提示。
 * - 卸载或换文件时释放几何体、材质、纹理、渲染器与 WebGL 上下文。
 */
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	AmbientLight,
	Box3,
	type BufferGeometry,
	DirectionalLight,
	DoubleSide,
	type Material,
	Mesh,
	MeshStandardMaterial,
	type Object3D,
	PerspectiveCamera,
	Points,
	PointsMaterial,
	Scene,
	SkinnedMesh,
	Sphere,
	Texture,
	TextureLoader,
	Vector3,
	WebGLRenderer,
} from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import { GLTFLoader } from "three/examples/jsm/loaders/GLTFLoader.js";
import { OBJLoader } from "three/examples/jsm/loaders/OBJLoader.js";
import { PLYLoader } from "three/examples/jsm/loaders/PLYLoader.js";
import { STLLoader } from "three/examples/jsm/loaders/STLLoader.js";
import { ErrorText, IconButton, Spinner } from "../../ui/primitives";
import type { BlobPreviewProps } from "./types";

/** 只含几何的格式所用的中性灰。 */
const NEUTRAL_COLOR = 0xb4b8c0;

/** glTF 引用了外部（非 data:）缓冲区：预览只拿到单个文件，无法解析。 */
class ExternalBufferError extends Error {}
/** 运行环境创建不了 WebGL 上下文。 */
class WebGLUnavailableError extends Error {}

interface ModelStats {
	vertices: number;
	triangles: number;
	/** glTF 里有外部纹理被替换成了占位 */
	missingTextures: boolean;
}

type State =
	| { status: "loading" }
	| { status: "error"; error: unknown }
	| { status: "ready"; stats: ModelStats };

interface ViewHandle {
	reset(): void;
	setWireframe(on: boolean): void;
}

export default function ModelPreview({
	blob,
	name,
}: BlobPreviewProps): ReactNode {
	const { t, i18n } = useTranslation();
	const hostRef = useRef<HTMLDivElement>(null);
	const viewRef = useRef<ViewHandle | null>(null);
	const [state, setState] = useState<State>({ status: "loading" });
	const [wireframe, setWireframe] = useState(false);

	useEffect(() => {
		const host = hostRef.current;
		if (!host) return;
		setState({ status: "loading" });
		setWireframe(false);

		let renderer: WebGLRenderer;
		try {
			renderer = new WebGLRenderer({ antialias: true, alpha: true });
		} catch (e) {
			setState({
				status: "error",
				error: new WebGLUnavailableError(String(e)),
			});
			return;
		}
		let disposed = false;
		renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
		const canvas = renderer.domElement;
		canvas.className = "absolute inset-0 block h-full w-full outline-none";
		host.appendChild(canvas);

		const scene = new Scene();
		const camera = new PerspectiveCamera(45, 1, 0.01, 1000);
		scene.add(camera);
		scene.add(new AmbientLight(0xffffff, 1));
		// 平行光挂在相机上、指向相机前方，旋转时受光面始终朝向观察者
		const sun = new DirectionalLight(0xffffff, 2.5);
		sun.position.set(0.6, 1, 0.4);
		sun.target.position.set(0, 0, -1);
		camera.add(sun, sun.target);

		const controls = new OrbitControls(camera, canvas);
		const render = () => renderer.render(scene, camera);
		controls.addEventListener("change", render);

		let model: Object3D | null = null;

		const resize = () => {
			const w = host.clientWidth;
			const h = host.clientHeight;
			if (w === 0 || h === 0) return;
			renderer.setSize(w, h, false);
			camera.aspect = w / h;
			camera.updateProjectionMatrix();
			render();
		};
		const ro = new ResizeObserver(resize);
		ro.observe(host);

		viewRef.current = {
			reset: () => controls.reset(),
			setWireframe: (on) => {
				if (!model) return;
				model.traverse((o) => {
					for (const m of materialsOf(o)) {
						if ("wireframe" in m) m.wireframe = on;
					}
				});
				render();
			},
		};

		loadModel(blob, name).then(
			({ object, missingTextures }) => {
				if (disposed) {
					disposeObject(object);
					return;
				}
				model = object;
				scene.add(object);
				resize();
				frame(object, camera, controls);
				render();
				setState({
					status: "ready",
					stats: { ...countGeometry(object), missingTextures },
				});
			},
			(error: unknown) => {
				if (!disposed) setState({ status: "error", error });
			},
		);

		return () => {
			disposed = true;
			viewRef.current = null;
			ro.disconnect();
			controls.removeEventListener("change", render);
			controls.dispose();
			disposeObject(scene);
			renderer.dispose();
			renderer.forceContextLoss();
			canvas.remove();
		};
	}, [blob, name]);

	useEffect(() => {
		viewRef.current?.setWireframe(wireframe);
	}, [wireframe]);

	const nf = new Intl.NumberFormat(i18n.language);

	return (
		<div className="relative h-full min-h-0 overflow-hidden bg-inset">
			<div ref={hostRef} className="absolute inset-0" />
			{state.status === "loading" && (
				<div className="absolute inset-0 flex items-center justify-center text-faint">
					<Spinner />
				</div>
			)}
			{state.status === "error" && (
				<div className="absolute inset-0 flex flex-col gap-1.5 overflow-auto p-3">
					{state.error instanceof WebGLUnavailableError ? (
						<ErrorText
							error={new Error(t("files.preview.model.webglUnavailable"))}
						/>
					) : state.error instanceof ExternalBufferError ? (
						<ErrorText
							error={new Error(t("files.preview.model.externalBuffer"))}
						/>
					) : (
						<>
							<p className="text-muted text-sm">
								{t("files.preview.model.failed")}
							</p>
							<ErrorText error={state.error} />
						</>
					)}
				</div>
			)}
			{state.status === "ready" && (
				<>
					<div className="absolute top-2 right-2 flex items-center gap-0.5 rounded-md border border-border bg-panel/80 p-0.5 shadow-xs backdrop-blur">
						<IconButton
							icon="frameCorners"
							box="sm"
							size={15}
							label={t("files.preview.model.resetView")}
							onClick={() => viewRef.current?.reset()}
						/>
						<IconButton
							icon="box"
							box="sm"
							size={15}
							active={wireframe}
							label={t("files.preview.model.wireframe")}
							onClick={() => setWireframe((w) => !w)}
						/>
					</div>
					<div className="pointer-events-none absolute bottom-2 left-3 flex flex-col gap-0.5 text-2xs text-faint tabular-nums">
						{state.stats.missingTextures && (
							<span className="text-warn">
								{t("files.preview.model.missingTextures")}
							</span>
						)}
						<span>
							{t("files.preview.model.stats", {
								vertices: nf.format(state.stats.vertices),
								triangles: nf.format(state.stats.triangles),
							})}
						</span>
					</div>
				</>
			)}
		</div>
	);
}

// ─────────────────────────── 加载 ───────────────────────────

async function loadModel(
	blob: Blob,
	name: string,
): Promise<{ object: Object3D; missingTextures: boolean }> {
	const ext = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
	switch (ext) {
		case "glb":
		case "gltf":
			return parseGltf(await blob.arrayBuffer());
		case "stl":
			return {
				object: meshOrPoints(
					new STLLoader().parse(await blob.arrayBuffer()),
					true,
				),
				missingTextures: false,
			};
		case "ply": {
			// PLY 有面时加载器会建索引；没有索引即为点云
			const geometry = new PLYLoader().parse(await blob.arrayBuffer());
			return {
				object: meshOrPoints(geometry, geometry.index !== null),
				missingTextures: false,
			};
		}
		case "obj": {
			// 没有配套 MTL：网格统一换成默认材质，线段与点保持加载器给的材质
			const group = new OBJLoader().parse(await blob.text());
			group.traverse((o) => {
				if (!(o instanceof Mesh)) return;
				for (const m of materialsOf(o)) m.dispose();
				o.material = defaultMaterial(o.geometry);
			});
			return { object: group, missingTextures: false };
		}
		default:
			throw new Error(`Unsupported model format: .${ext}`);
	}
}

function defaultMaterial(geometry: BufferGeometry): MeshStandardMaterial {
	const vertexColors = geometry.hasAttribute("color");
	if (!geometry.hasAttribute("normal")) geometry.computeVertexNormals();
	return new MeshStandardMaterial({
		color: vertexColors ? 0xffffff : NEUTRAL_COLOR,
		vertexColors,
		metalness: 0.05,
		roughness: 0.65,
		// STL / OBJ 常有朝向不一致的面，双面渲染避免出现破洞
		side: DoubleSide,
	});
}

function meshOrPoints(geometry: BufferGeometry, surface: boolean): Object3D {
	if (surface) return new Mesh(geometry, defaultMaterial(geometry));
	const vertexColors = geometry.hasAttribute("color");
	geometry.computeBoundingSphere();
	const radius = geometry.boundingSphere?.radius ?? 1;
	return new Points(
		geometry,
		new PointsMaterial({
			color: vertexColors ? 0xffffff : NEUTRAL_COLOR,
			vertexColors,
			// 点大小按模型尺度取，避免大坐标的点云看不见、小坐标的糊成一片
			size: radius / 300 || 0.01,
		}),
	);
}

/**
 * glTF / GLB：通过插件钩子拿到 parser，替换两处会走 fetch 的加载路径——
 * 缓冲区（data: URI 本地解码，外部引用直接报错）与纹理（换成基于 <img> 的 TextureLoader）。
 */
async function parseGltf(
	buf: ArrayBuffer,
): Promise<{ object: Object3D; missingTextures: boolean }> {
	const loader = new GLTFLoader();
	let missingTextures = false;
	loader.register((parser) => {
		parser.textureLoader = new TextureLoader(parser.options.manager);
		const json = parser.json as {
			buffers?: { uri?: unknown }[];
			images?: { uri?: unknown }[];
		};
		const original = parser.loadBuffer.bind(parser);
		parser.loadBuffer = (index) => {
			const uri = json.buffers?.[index]?.uri;
			if (typeof uri !== "string") return original(index);
			if (!uri.startsWith("data:")) {
				return Promise.reject(new ExternalBufferError(uri));
			}
			return Promise.resolve(decodeDataUri(uri));
		};
		for (const image of json.images ?? []) {
			if (typeof image.uri === "string" && !image.uri.startsWith("data:")) {
				image.uri = whitePixel();
				missingTextures = true;
			}
		}
		return { name: "ORYN_sandboxed_resources" };
	});
	const gltf = await loader.parseAsync(buf, "");
	return { object: gltf.scene, missingTextures };
}

/** `data:[<类型>][;base64],<数据>` → 字节；非 base64 的按百分号编码逐字节还原。 */
function decodeDataUri(uri: string): ArrayBuffer {
	const comma = uri.indexOf(",");
	if (comma < 0) throw new Error("Malformed data URI");
	const header = uri.slice(5, comma);
	const body = uri.slice(comma + 1);
	if (/;base64$/i.test(header)) {
		const bin = atob(body);
		const out = new Uint8Array(bin.length);
		for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
		return out.buffer;
	}
	const bytes: number[] = [];
	const enc = new TextEncoder();
	for (let i = 0; i < body.length; i++) {
		const ch = body[i] ?? "";
		if (ch === "%" && /^[0-9a-f]{2}$/i.test(body.slice(i + 1, i + 3))) {
			bytes.push(Number.parseInt(body.slice(i + 1, i + 3), 16));
			i += 2;
		} else {
			bytes.push(...enc.encode(ch));
		}
	}
	return new Uint8Array(bytes).buffer;
}

let whitePixelUrl: string | undefined;

/** 1×1 白色 PNG 的 data URL，替代无法加载的外部纹理（与基础色相乘后不改变颜色）。 */
function whitePixel(): string {
	if (!whitePixelUrl) {
		const c = document.createElement("canvas");
		c.width = 1;
		c.height = 1;
		const ctx = c.getContext("2d");
		if (ctx) {
			ctx.fillStyle = "#fff";
			ctx.fillRect(0, 0, 1, 1);
		}
		whitePixelUrl = c.toDataURL("image/png");
	}
	return whitePixelUrl;
}

// ─────────────────────────── 取景、统计与释放 ───────────────────────────

/** 让包围球完整落在视野内（取竖直与水平视角中较小者），并把当前视角存为「重置视角」的目标。 */
function frame(
	object: Object3D,
	camera: PerspectiveCamera,
	controls: OrbitControls,
): void {
	const box = new Box3().setFromObject(object);
	if (box.isEmpty()) return;
	const sphere = box.getBoundingSphere(new Sphere());
	const radius = sphere.radius > 0 ? sphere.radius : 1;
	const vFov = (camera.fov * Math.PI) / 180;
	const hFov = 2 * Math.atan(Math.tan(vFov / 2) * camera.aspect);
	const dist = (radius / Math.sin(Math.min(vFov, hFov) / 2)) * 1.05;
	camera.position
		.copy(sphere.center)
		.add(new Vector3(1, 0.7, 1).normalize().multiplyScalar(dist));
	camera.near = dist / 100;
	camera.far = dist * 100;
	camera.updateProjectionMatrix();
	controls.target.copy(sphere.center);
	controls.maxDistance = dist * 20;
	controls.update();
	controls.saveState();
}

function countGeometry(root: Object3D): {
	vertices: number;
	triangles: number;
} {
	let vertices = 0;
	let triangles = 0;
	root.traverse((o) => {
		if (!(o instanceof Mesh || o instanceof Points)) return;
		const g: BufferGeometry = o.geometry;
		const count = g.getAttribute("position")?.count ?? 0;
		vertices += count;
		if (o instanceof Mesh)
			triangles += Math.floor((g.index?.count ?? count) / 3);
	});
	return { vertices, triangles };
}

function materialsOf(o: Object3D): Material[] {
	if (!("material" in o)) return [];
	const m = o.material as Material | Material[] | undefined;
	return Array.isArray(m) ? m : m ? [m] : [];
}

function disposeObject(root: Object3D): void {
	root.traverse((o) => {
		if (
			"geometry" in o &&
			(o.geometry as BufferGeometry | undefined)?.isBufferGeometry
		) {
			(o.geometry as BufferGeometry).dispose();
		}
		if (o instanceof SkinnedMesh) o.skeleton.dispose();
		for (const m of materialsOf(o)) {
			for (const value of Object.values(m)) {
				if (value instanceof Texture) value.dispose();
			}
			m.dispose();
		}
	});
}
