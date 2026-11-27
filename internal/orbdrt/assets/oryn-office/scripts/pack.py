"""把 unpack.py 解出的目录打回 Office 文件。

用法：python pack.py <目录> <输出文件>

条目名是相对目录的路径（不带目录本身作前缀），[Content_Types].xml 永远排第一，其余按 .oryn-order 记下的原顺序，
新增的文件按路径排在最后。打包前检查每个 .xml / .rels 都是合法的 XML，有坏的就不写输出文件。
输出先写到临时文件，成功后再替换目标。
"""

import os
import pathlib
import sys
import xml.etree.ElementTree as ET
import zipfile

ORDER_FILE = ".oryn-order"
CONTENT_TYPES = "[Content_Types].xml"


def main() -> int:
    if len(sys.argv) != 3:
        print("用法：python pack.py <目录> <输出文件>", file=sys.stderr)
        return 2
    root, out = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
    if not (root / CONTENT_TYPES).is_file():
        print(f"{root} 里没有 {CONTENT_TYPES}，不是解包出的 Office 目录", file=sys.stderr)
        return 2
    files = {p.relative_to(root).as_posix() for p in root.rglob("*") if p.is_file()}
    files.discard(ORDER_FILE)
    order_path = root / ORDER_FILE
    recorded = order_path.read_text(encoding="utf-8").splitlines() if order_path.is_file() else []
    names = [CONTENT_TYPES]
    names += [n for n in recorded if n in files and n != CONTENT_TYPES]
    names += sorted(files - set(names))

    bad = []
    for name in names:
        if name.endswith((".xml", ".rels")):
            try:
                ET.fromstring((root / name).read_bytes())
            except ET.ParseError as err:
                bad.append(f"- {name}: {err}")
    if bad:
        print("这些 XML 不合法，没有打包：", file=sys.stderr)
        print("\n".join(bad), file=sys.stderr)
        return 1

    tmp = out.with_name(f".{out.name}.oryn-pack")
    try:
        with zipfile.ZipFile(tmp, "w", compression=zipfile.ZIP_DEFLATED) as zf:
            for name in names:
                zf.write(root / name, arcname=name)
        os.replace(tmp, out)
    finally:
        tmp.unlink(missing_ok=True)
    print(f"已打包 {len(names)} 个条目到 {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
