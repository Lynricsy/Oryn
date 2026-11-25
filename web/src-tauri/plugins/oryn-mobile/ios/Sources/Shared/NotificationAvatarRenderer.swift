// iOS 通知发送者 128×128 圆形头像绘制（与 Android NotificationAvatarRenderer 保持同一套视觉语言）：
// - 前端预栅格化 PNG（全局助手自定义照片或 Nerd Font 线程图标）优先解码裁圆；
// - 普通线程：按线程标题卡图标（Emoji 或打包的 Symbols Nerd Font）与线程色绘制，无图标时绘制 Oryn 品牌标；
// - 全局助手（主线动态与助手对话线程）：优先自定义图片（含主 App ↔ NSE 共享缓存），其次自定义字形，否则四芒星 sparkle 标记；
// - 系统通知：深墨底白色 Oryn 品牌标。
import CoreText
import CryptoKit
import Foundation
import UIKit
enum NotificationAvatarRenderer {
  private struct Palette {
    let background: UIColor
    let foreground: UIColor
  }

  private static let threadPalettes: [String: Palette] = [
    "red": Palette(background: rgb(0xFCE8E6), foreground: rgb(0xB3261E)),
    "orange": Palette(background: rgb(0xFEF0E1), foreground: rgb(0xB45309)),
    "yellow": Palette(background: rgb(0xFEF7DC), foreground: rgb(0x9A6B00)),
    "green": Palette(background: rgb(0xE6F4EA), foreground: rgb(0x18794E)),
    "teal": Palette(background: rgb(0xE3F5F5), foreground: rgb(0x0F766E)),
    "blue": Palette(background: rgb(0xE8EFFC), foreground: rgb(0x2B5CE6)),
    "purple": Palette(background: rgb(0xF2E8FC), foreground: rgb(0x7C3AED)),
    "pink": Palette(background: rgb(0xFCE7F3), foreground: rgb(0xBE185D))
  ]

  private static let neutralPalette = Palette(background: rgb(0xEEF0F4), foreground: rgb(0x1F2430))
  private static let assistantPalette = Palette(background: rgb(0xE8EFFC), foreground: rgb(0x2B5CE6))
  private static let systemPalette = Palette(background: rgb(0x1F2430), foreground: .white)
  private static let avatarPx: CGFloat = 128
  private static let assistantHashAccount = "assistant_avatar_hash"
  private static let assistantImageAccount = "assistant_avatar_img"
  private static let maxCachedAvatarBytes = 64 * 1024

  /// 打包在主 App 与 NotificationService.appex 内的 Symbols Nerd Font Mono（CGDataProvider 按需 mmap，适配 NSE 内存限制）。
  private static let nerdCGFont: CGFont? = {
    guard let url = Bundle.main.url(forResource: "SymbolsNerdFontMono-Regular", withExtension: "ttf"),
      let provider = CGDataProvider(url: url as CFURL)
    else { return nil }
    return CGFont(provider)
  }()

  /// 前台收到全局助手头像信息时更新共享 Keychain 缓存（128×128 圆形 PNG），供 NSE 后台推送按 hash 零网络复用。
  static func cacheAssistantAvatar(_ avatar: NotifyAvatarArgs?) {
    guard let avatar = avatar, avatar.kind == "assistant" else { return }
    let hasIcon = avatar.icon.map { !$0.isEmpty } ?? false
    let hasImage = avatar.image.map { !$0.isEmpty } ?? false
    let hash = avatar.hash.flatMap { $0.isEmpty ? nil : $0 }
    if !hasIcon && !hasImage && hash == nil {
      clearAssistantAvatarCache()
      return
    }
    guard let hash = hash, let raw = avatar.image, !raw.isEmpty else { return }
    if let existing = PushKeyStore.readShared(assistantHashAccount),
      String(data: existing, encoding: .utf8) == hash,
      PushKeyStore.readShared(assistantImageAccount) != nil
    {
      return
    }
    guard let circle = decodeCircularImage(raw), let encoded = encodeCachedAvatar(circle) else { return }
    if PushKeyStore.writeShared(assistantImageAccount, encoded) {
      PushKeyStore.writeShared(assistantHashAccount, Data(hash.utf8))
    }
  }

  static func clearAssistantAvatarCache() {
    PushKeyStore.deleteShared(assistantHashAccount)
    PushKeyStore.deleteShared(assistantImageAccount)
  }

  /// 由主 App 在拉取或保存全局助手身份后直接同步原始头像（计算与服务端一致的 8 位 SHA-256 摘要并裁圆存入共享 Keychain）。
  static func syncAssistantAvatar(icon: String?, image: String?) {
    guard let raw = image, !raw.isEmpty else {
      clearAssistantAvatarCache()
      return
    }
    let hash = avatarDigest(kind: "image", value: raw)
    cacheAssistantAvatar(
      NotifyAvatarArgs(kind: "assistant", icon: icon, color: nil, image: raw, hash: hash))
  }

  /// 与服务端 `internal/notify/avatar.go` 的 `avatarDigest` 保持字节级一致：`sha256(kind + "\0" + val)[:8]`。
  static func avatarDigest(kind: String, value: String) -> String {
    var data = Data()
    data.append(contentsOf: kind.utf8)
    data.append(0)
    data.append(contentsOf: value.utf8)
    let digest = SHA256.hash(data: data)
    return digest.prefix(4).map { String(format: "%02x", $0) }.joined()
  }

  static func renderPNG(_ avatar: NotifyAvatarArgs) -> Data? {
    if let base64 = avatar.image, !base64.isEmpty, let image = decodeCircularImage(base64) {
      return image.pngData()
    }
    switch avatar.kind {
    case "assistant":
      if let icon = avatar.icon, !icon.isEmpty {
        if let img = renderIconImage(icon, palette: assistantPalette) {
          return img.pngData()
        }
      }
      if let hash = avatar.hash, !hash.isEmpty, let cached = loadCachedAssistantAvatar(hash: hash) {
        return cached
      }
      return renderSparkleImage(palette: assistantPalette).pngData()
    case "thread":
      let palette = avatar.color.flatMap { threadPalettes[$0] } ?? neutralPalette
      if let icon = avatar.icon, !icon.isEmpty {
        if let img = renderIconImage(icon, palette: palette) {
          return img.pngData()
        }
      }
      return renderBrandMarkImage(palette: palette).pngData()
    default:
      return renderBrandMarkImage(palette: systemPalette).pngData()
    }
  }
  static func isNerdGlyph(_ icon: String) -> Bool {
    guard let scalar = icon.unicodeScalars.first else { return false }
    let value = scalar.value
    return (value >= 0xE000 && value <= 0xF8FF) || (value >= 0xF0000 && value <= 0xFFFFD)
  }

  private static func rgb(_ hex: UInt32) -> UIColor {
    UIColor(
      red: CGFloat((hex >> 16) & 0xFF) / 255.0,
      green: CGFloat((hex >> 8) & 0xFF) / 255.0,
      blue: CGFloat(hex & 0xFF) / 255.0,
      alpha: 1.0
    )
  }

  private static func renderer() -> UIGraphicsImageRenderer {
    let format = UIGraphicsImageRendererFormat()
    format.scale = 1.0
    format.opaque = false
    return UIGraphicsImageRenderer(size: CGSize(width: avatarPx, height: avatarPx), format: format)
  }

  private static func decodeCircularImage(_ raw: String) -> UIImage? {
    let clean: String
    if let range = raw.range(of: "base64,") {
      clean = String(raw[range.upperBound...])
    } else {
      clean = raw
    }
    guard let data = Data(base64Encoded: clean, options: .ignoreUnknownCharacters),
      let source = UIImage(data: data)
    else { return nil }
    let side = min(source.size.width, source.size.height)
    guard side > 0 else { return nil }
    return renderer().image { _ in
      let rect = CGRect(x: 0, y: 0, width: avatarPx, height: avatarPx)
      UIBezierPath(ovalIn: rect).addClip()
      let scale = avatarPx / side
      let drawWidth = source.size.width * scale
      let drawHeight = source.size.height * scale
      source.draw(in: CGRect(
        x: (avatarPx - drawWidth) / 2,
        y: (avatarPx - drawHeight) / 2,
        width: drawWidth,
        height: drawHeight
      ))
    }
  }

  private static func loadCachedAssistantAvatar(hash: String) -> Data? {
    guard let savedHash = PushKeyStore.readShared(assistantHashAccount),
      String(data: savedHash, encoding: .utf8) == hash
    else { return nil }
    return PushKeyStore.readShared(assistantImageAccount)
  }

  private static func encodeCachedAvatar(_ image: UIImage) -> Data? {
    if let png = image.pngData(), png.count <= maxCachedAvatarBytes {
      return png
    }
    if let jpg = image.jpegData(compressionQuality: 0.85), jpg.count <= maxCachedAvatarBytes {
      return jpg
    }
    return nil
  }

  private static func renderIconImage(_ icon: String, palette: Palette) -> UIImage? {
    if isNerdGlyph(icon) {
      return renderNerdGlyphImage(icon, palette: palette)
    }
    return renderGlyphImage(icon, palette: palette)
  }

  /// 使用打包的 Symbols Nerd Font 绘制 PUA 字形并按真实墨迹边界居中；缺字体或缺字形时返回 nil 以安全回落，绝不画豆腐块。
  private static func renderNerdGlyphImage(_ glyph: String, palette: Palette) -> UIImage? {
    guard let cgFont = nerdCGFont else { return nil }
    let ctFont = CTFontCreateWithGraphicsFont(cgFont, avatarPx * 0.56, nil, nil)
    var chars = Array(glyph.utf16)
    guard !chars.isEmpty else { return nil }
    var glyphs = [CGGlyph](repeating: 0, count: chars.count)
    guard CTFontGetGlyphsForCharacters(ctFont, &chars, &glyphs, chars.count),
      glyphs.contains(where: { $0 != 0 })
    else { return nil }

    let attrs: [NSAttributedString.Key: Any] = [
      NSAttributedString.Key(kCTFontAttributeName as String): ctFont,
      NSAttributedString.Key(kCTForegroundColorAttributeName as String): palette.foreground.cgColor
    ]
    let line = CTLineCreateWithAttributedString(NSAttributedString(string: glyph, attributes: attrs))
    var drew = false
    let img = renderer().image { ctx in
      let rect = CGRect(x: 0, y: 0, width: avatarPx, height: avatarPx)
      palette.background.setFill()
      UIBezierPath(ovalIn: rect).fill()

      let cg = ctx.cgContext
      cg.saveGState()
      cg.textMatrix = .identity
      cg.translateBy(x: 0, y: avatarPx)
      cg.scaleBy(x: 1, y: -1)
      let bounds = CTLineGetImageBounds(line, cg)
      if !bounds.isEmpty && bounds.width > 0 && bounds.height > 0 {
        let x = (avatarPx - bounds.width) / 2 - bounds.origin.x
        let y = (avatarPx - bounds.height) / 2 - bounds.origin.y
        cg.textPosition = CGPoint(x: x, y: y)
        CTLineDraw(line, cg)
        drew = true
      }
      cg.restoreGState()
    }
    return drew ? img : nil
  }

  private static func renderGlyphImage(_ glyph: String, palette: Palette) -> UIImage {
    renderer().image { _ in
      let rect = CGRect(x: 0, y: 0, width: avatarPx, height: avatarPx)
      palette.background.setFill()
      UIBezierPath(ovalIn: rect).fill()
      let attrs: [NSAttributedString.Key: Any] = [
        .font: UIFont.systemFont(ofSize: avatarPx * 0.54, weight: .semibold),
        .foregroundColor: palette.foreground
      ]
      let text = NSAttributedString(string: glyph, attributes: attrs)
      let size = text.size()
      text.draw(at: CGPoint(x: (avatarPx - size.width) / 2, y: (avatarPx - size.height) / 2))
    }
  }
  private static func renderSparkleImage(palette: Palette) -> UIImage {
    renderer().image { _ in
      let rect = CGRect(x: 0, y: 0, width: avatarPx, height: avatarPx)
      palette.background.setFill()
      UIBezierPath(ovalIn: rect).fill()
      palette.foreground.setFill()
      fourPointStar(centerX: avatarPx * 0.46, centerY: avatarPx * 0.54, radius: avatarPx * 0.27).fill()
      fourPointStar(centerX: avatarPx * 0.72, centerY: avatarPx * 0.28, radius: avatarPx * 0.115).fill()
    }
  }

  private static func fourPointStar(centerX: CGFloat, centerY: CGFloat, radius: CGFloat) -> UIBezierPath {
    let inset = radius * 0.24
    let path = UIBezierPath()
    path.move(to: CGPoint(x: centerX, y: centerY - radius))
    path.addQuadCurve(
      to: CGPoint(x: centerX + radius, y: centerY),
      controlPoint: CGPoint(x: centerX + inset, y: centerY - inset)
    )
    path.addQuadCurve(
      to: CGPoint(x: centerX, y: centerY + radius),
      controlPoint: CGPoint(x: centerX + inset, y: centerY + inset)
    )
    path.addQuadCurve(
      to: CGPoint(x: centerX - radius, y: centerY),
      controlPoint: CGPoint(x: centerX - inset, y: centerY + inset)
    )
    path.addQuadCurve(
      to: CGPoint(x: centerX, y: centerY - radius),
      controlPoint: CGPoint(x: centerX - inset, y: centerY - inset)
    )
    path.close()
    return path
  }

  private static func renderBrandMarkImage(palette: Palette) -> UIImage {
    renderer().image { context in
      let rect = CGRect(x: 0, y: 0, width: avatarPx, height: avatarPx)
      palette.background.setFill()
      UIBezierPath(ovalIn: rect).fill()

      let cgContext = context.cgContext
      let iconSize = avatarPx * 0.58
      let scale = iconSize / 32.0
      let offset = (avatarPx - iconSize) / 2.0
      cgContext.saveGState()
      cgContext.translateBy(x: offset, y: offset)
      cgContext.scaleBy(x: scale, y: scale)
      palette.foreground.setFill()

      // 左半个 orb：圆心 (16, 16) 半径 12，弧与竖弦 x = 20.1 围合
      let startAngle = CGFloat(70.02 * .pi / 180.0)
      let endAngle = CGFloat(289.98 * .pi / 180.0)
      let orb = UIBezierPath(
        arcCenter: CGPoint(x: 16, y: 16),
        radius: 12,
        startAngle: startAngle,
        endAngle: endAngle,
        clockwise: true
      )
      orb.close()
      orb.fill()

      // 右侧竖光标条：x=[23.4, 27.0], y=[4.0, 28.0], 圆角 1.1
      UIBezierPath(roundedRect: CGRect(x: 23.4, y: 4.0, width: 3.6, height: 24.0), cornerRadius: 1.1).fill()
      cgContext.restoreGState()
    }
  }
}
