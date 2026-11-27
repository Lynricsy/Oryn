"""重算 Excel 工作簿的全部公式并检查错误值。

用法：python recalc.py <工作簿.xlsx|.xlsm> [--timeout 秒]

用 LibreOffice 无头打开工作簿、强制重算全部公式后原地另存（先写临时文件再替换原文件），这样文件里带上了公式的
计算结果，预览与不会自己重算的阅读器都能看到数字；再用 openpyxl 读出计算结果，列出所有错误值（#DIV/0!、#REF! 等）
所在的单元格。没有错误时退出码 0，有错误时退出码 1，运行失败时退出码 2。
"""

import argparse
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

import openpyxl

# 强制在打开 OOXML / ODF 表格时重算全部公式（0 = 总是重算）；写进一次性的用户配置目录，不影响用户自己的设置。
RECALC_XCU = """<?xml version="1.0" encoding="UTF-8"?>
<oor:items xmlns:oor="http://openoffice.org/2001/registry" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
<item oor:path="/org.openoffice.Office.Calc/Formula/Load"><prop oor:name="OOXMLRecalcMode" oor:op="fuse"><value>0</value></prop></item>
<item oor:path="/org.openoffice.Office.Calc/Formula/Load"><prop oor:name="ODFRecalcMode" oor:op="fuse"><value>0</value></prop></item>
</oor:items>
"""

# 另存用的 LibreOffice 导出过滤器（按扩展名）。
FILTERS = {".xlsx": "Calc MS Excel 2007 XML", ".xlsm": "Calc MS Excel 2007 VBA XML"}

ERRORS = {"#NULL!", "#DIV/0!", "#VALUE!", "#REF!", "#NAME?", "#NUM!", "#N/A", "#SPILL!", "#CALC!", "#GETTING_DATA"}


def recalc(path: pathlib.Path, timeout: int) -> None:
    """经 LibreOffice 重算并原地另存。"""
    ext = path.suffix.lower()
    with tempfile.TemporaryDirectory(prefix="oryn-recalc-") as work:
        profile = pathlib.Path(work, "profile")
        (profile / "user").mkdir(parents=True)
        (profile / "user" / "registrymodifications.xcu").write_text(RECALC_XCU, encoding="utf-8")
        out = pathlib.Path(work, "out")
        out.mkdir()
        cmd = [
            "soffice",
            f"-env:UserInstallation={profile.as_uri()}",
            "--headless",
            "--convert-to",
            f"{ext[1:]}:{FILTERS[ext]}",
            "--outdir",
            str(out),
            str(path),
        ]
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
        result = out / path.name
        if not result.is_file() or result.stat().st_size == 0:
            raise RuntimeError(f"LibreOffice 没能重算 {path}：\n{proc.stdout}{proc.stderr}")
        tmp = path.with_name(f".{path.name}.oryn-recalc")
        shutil.copyfile(result, tmp)
        os.replace(tmp, path)


def scan(path: pathlib.Path) -> tuple[int, list[str]]:
    """返回公式个数与错误值清单（每项是「工作表!单元格: 错误值（公式）」）。"""
    formulas = openpyxl.load_workbook(path, data_only=False)
    values = openpyxl.load_workbook(path, data_only=True)
    count, problems = 0, []
    for ws in formulas.worksheets:
        vs = values[ws.title]
        for row in ws.iter_rows():
            for cell in row:
                is_formula = cell.data_type == "f"
                count += is_formula
                value = vs[cell.coordinate].value
                if isinstance(value, str) and value in ERRORS:
                    where = f"{ws.title}!{cell.coordinate}: {value}"
                    problems.append(f"{where}（{cell.value}）" if is_formula else where)
    return count, problems


def main() -> int:
    ap = argparse.ArgumentParser(description="重算 Excel 工作簿的全部公式并检查错误值")
    ap.add_argument("workbook", type=pathlib.Path)
    ap.add_argument("--timeout", type=int, default=120, help="LibreOffice 重算的超时（秒），缺省 120")
    args = ap.parse_args()
    path = args.workbook.resolve()
    if path.suffix.lower() not in FILTERS:
        print(f"只支持 .xlsx 与 .xlsm：{path}", file=sys.stderr)
        return 2
    if not path.is_file():
        print(f"找不到文件：{path}", file=sys.stderr)
        return 2
    try:
        recalc(path, args.timeout)
        count, problems = scan(path)
    except (RuntimeError, subprocess.TimeoutExpired, OSError) as err:
        print(err, file=sys.stderr)
        return 2
    if problems:
        print(f"{path.name}：{count} 个公式，{len(problems)} 个错误值：")
        for p in problems:
            print(f"- {p}")
        return 1
    print(f"{path.name}：{count} 个公式已重算，没有错误值。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
