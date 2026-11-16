// orb 状态珠（照 web/src/components/ui/OrbBead.tsx 的 16×16 视图盒画）：
// booting 旋转弧（1.2s 一圈）、ready 实心、working 实心 + 光晕呼吸（2.4s 一个来回）、freezing / frozen 霜环 + 六道刻线
// （freezing 明暗 1.6s 一个来回）、stopped 空心、deleted 虚线、failed 红底白叉、err 7pt 错误色圆点。
// 动画按时钟算相位（TimelineView），状态切换不需要重启动画；减弱动态效果时全部静止。
import SwiftUI

@available(iOS 26.0, *)
struct OrbBeadView: View {
  let state: String
  let palette: HeaderPalette
  @Environment(\.accessibilityReduceMotion) private var reduce

  private var animated: Bool { state == "booting" || state == "working" || state == "freezing" }

  var body: some View {
    Group {
      if animated && !reduce {
        TimelineView(.animation) { context in
          glyph(time: context.date.timeIntervalSinceReferenceDate)
        }
      } else {
        glyph(time: nil)
      }
    }
    .accessibilityHidden(true)
  }

  /// 0→1→0 的来回，`period` 秒一个来回；静止时取中间值。
  private func wave(_ time: Double?, _ period: Double) -> Double {
    guard let time = time else { return 0.5 }
    return 0.5 - 0.5 * cos(2 * .pi * time / period)
  }

  /// booting 弧的转角（度）；静止时停在起点。
  private func spin(_ time: Double?) -> Double {
    guard let time = time else { return -90 }
    return -90 + 360 * (time / 1.2).truncatingRemainder(dividingBy: 1)
  }

  private func glyph(time: Double?) -> some View {
    GeometryReader { geo in
      let u = min(geo.size.width, geo.size.height) / 16
      ZStack {
        switch state {
        case "booting":
          Circle().stroke(Color.primary.opacity(0.18), lineWidth: 1.75 * u)
            .frame(width: 12 * u, height: 12 * u)
          Circle().trim(from: 0, to: 0.25)
            .stroke(palette.brand, style: StrokeStyle(lineWidth: 1.75 * u, lineCap: .round))
            .frame(width: 12 * u, height: 12 * u)
            .rotationEffect(.degrees(spin(time)))
        case "ready", "working":
          let w = wave(time, 2.4)
          let breathing = state == "working"
          Circle().fill(palette.good)
            .frame(width: 14.72 * u, height: 14.72 * u)
            .scaleEffect(breathing ? 0.9 + 0.2 * w : 1)
            .opacity(breathing ? 0.2 + 0.3 * w : 0.3)
          Circle().fill(palette.good).frame(width: 10.24 * u, height: 10.24 * u)
        case "freezing", "frozen":
          ZStack {
            Circle().stroke(palette.frost, lineWidth: 1.5 * u).frame(width: 12 * u, height: 12 * u)
            ForEach(0..<6, id: \.self) { i in
              Capsule().fill(palette.frost)
                .frame(width: 1.2 * u, height: 1.8 * u)
                .offset(y: -2.5 * u)
                .rotationEffect(.degrees(Double(i) * 60))
            }
          }
          .opacity(state == "freezing" ? 0.5 + 0.5 * wave(time, 1.6) : 1)
        case "stopped":
          Circle().stroke(Color.primary.opacity(0.35), lineWidth: 1.75 * u)
            .frame(width: 12 * u, height: 12 * u)
        case "deleted":
          Circle()
            .stroke(
              Color.primary.opacity(0.35),
              style: StrokeStyle(lineWidth: 1.5 * u, dash: [2 * u, 2 * u])
            )
            .frame(width: 12 * u, height: 12 * u)
        case "err":
          Circle().fill(palette.err).frame(width: 7, height: 7)
        default:
          // failed（及未知档位）：红底白叉
          Circle().fill(palette.err).frame(width: 14 * u, height: 14 * u)
          Image(systemName: "xmark")
            .font(.system(size: 7 * u, weight: .bold))
            .foregroundStyle(Color.white)
        }
      }
      .frame(width: geo.size.width, height: geo.size.height)
    }
  }
}
