// 网页给的 `#rrggbb` 颜色解析（底栏强调色与垫底色、页头调色板与图标颜色共用）。
import SwiftUI
import UIKit

/// `#rrggbb` → (r, g, b) ∈ [0, 1]；空或格式不对为 nil。
private func hexRGB(_ hex: String?) -> (Double, Double, Double)? {
  guard let hex = hex, hex.count == 7, hex.hasPrefix("#"), let n = UInt32(hex.dropFirst(), radix: 16)
  else {
    return nil
  }
  return (Double((n >> 16) & 255) / 255, Double((n >> 8) & 255) / 255, Double(n & 255) / 255)
}

/// `#rrggbb` → Color；空或格式不对为 nil。
func hexColor(_ hex: String?) -> Color? {
  hexRGB(hex).map { Color(red: $0.0, green: $0.1, blue: $0.2) }
}

/// `#rrggbb` → UIColor（sRGB，与网页 `getComputedStyle` 换算出的值同一色彩空间）；空或格式不对为 nil。
func hexUIColor(_ hex: String?) -> UIColor? {
  hexRGB(hex).map { UIColor(red: $0.0, green: $0.1, blue: $0.2, alpha: 1) }
}
