// CI（ios-tabbar-check.sh / ios-e2e-check.sh）用的逐区域像素比较。
// 用法：ios-region-diff <a.png> <b.png> <x> <y> <w> <h> [bx]（像素，左上为原点）
// 比较 a 的区域 (x, y, w, h) 与 b 的区域 (bx, y, w, h)；bx 缺省 = x（同位置）。给 bx 用于比较横向平移过的内容（下沉卡片）。
// 输出「任一颜色通道相差超过 16」的像素占比（0–1，六位小数）；参数或图片有误时退出码 2。
import CoreGraphics
import Foundation
import ImageIO

func fail(_ message: String) -> Never {
  FileHandle.standardError.write((message + "\n").data(using: .utf8)!)
  exit(2)
}

/// 把图片画进 RGBA8 位图（两张图走同一条色彩转换，比较才有意义）。
func load(_ path: String) -> (pixels: [UInt8], width: Int, height: Int) {
  guard let source = CGImageSourceCreateWithURL(URL(fileURLWithPath: path) as CFURL, nil),
    let image = CGImageSourceCreateImageAtIndex(source, 0, nil)
  else { fail("读不到图片 \(path)") }
  let (w, h) = (image.width, image.height)
  var pixels = [UInt8](repeating: 0, count: w * h * 4)
  pixels.withUnsafeMutableBytes { raw in
    guard
      let ctx = CGContext(
        data: raw.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
        space: CGColorSpaceCreateDeviceRGB(),
        bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
    else { fail("建不了位图上下文") }
    ctx.draw(image, in: CGRect(x: 0, y: 0, width: w, height: h))
  }
  return (pixels, w, h)
}

let args = CommandLine.arguments
guard args.count == 7 || args.count == 8, let x = Int(args[3]), let y = Int(args[4]),
  let w = Int(args[5]), let h = Int(args[6])
else { fail("用法：ios-region-diff <a.png> <b.png> <x> <y> <w> <h> [bx]") }
guard let bx = args.count == 8 ? Int(args[7]) : x else { fail("bx 不是整数：\(args[7])") }
let (a, b) = (load(args[1]), load(args[2]))
guard a.width == b.width, a.height == b.height else {
  fail("两张图尺寸不同：\(a.width)×\(a.height) 与 \(b.width)×\(b.height)")
}
guard w > 0, h > 0, x >= 0, y >= 0, bx >= 0, x + w <= a.width, bx + w <= a.width, y + h <= a.height
else {
  fail("区域 (\(x) / \(bx), \(y), \(w), \(h)) 越出 \(a.width)×\(a.height)")
}
var differing = 0
for row in y..<(y + h) {
  for col in 0..<w {
    let i = (row * a.width + x + col) * 4
    let j = (row * b.width + bx + col) * 4
    if abs(Int(a.pixels[i]) - Int(b.pixels[j])) > 16
      || abs(Int(a.pixels[i + 1]) - Int(b.pixels[j + 1])) > 16
      || abs(Int(a.pixels[i + 2]) - Int(b.pixels[j + 2])) > 16
    {
      differing += 1
    }
  }
}
print(String(format: "%.6f", Double(differing) / Double(w * h)))
