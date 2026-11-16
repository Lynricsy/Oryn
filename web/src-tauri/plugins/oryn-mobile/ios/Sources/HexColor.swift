// 网页给的 `#rrggbb` 颜色解析（底栏强调色、页头调色板与图标颜色共用）。
import SwiftUI

/// `#rrggbb` → Color；空或格式不对为 nil。
func hexColor(_ hex: String?) -> Color? {
  guard let hex = hex, hex.count == 7, hex.hasPrefix("#"), let n = UInt32(hex.dropFirst(), radix: 16)
  else {
    return nil
  }
  return Color(
    red: Double((n >> 16) & 255) / 255, green: Double((n >> 8) & 255) / 255,
    blue: Double(n & 255) / 255)
}
