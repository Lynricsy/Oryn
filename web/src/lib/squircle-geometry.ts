/**
 * 超椭圆角形的几何常量：与 CSS Borders 4「corner-shape: superellipse(K)」同一条曲线。
 *
 * 规范（§3.9.4.2 Computing a contoured path）：每个角是单位超椭圆 |x|^n + |y|^n = 1（n = 2^K）按该角的
 * border-radius 缩放后的四分之一段。本站 K = 1.6，n ≈ 3.03。
 *
 * 降级（见 squircle-fallback.ts）只用这里的 ROUND_SCALE：原生圆角取 ROUND_SCALE × 半径，这个圆刚好把超椭圆整个包住、
 * 在 45° 对角线上相切，两条曲线的最大法向偏差 1.42% 半径（单测里验证）。
 */

/** 本站选定的 superellipse() 参数 K。 */
export const SUPERELLIPSE_K = 1.6;
/** 超椭圆指数 n = 2^K。 */
export const EXPONENT = 2 ** SUPERELLIPSE_K;

/**
 * 包络圆的半径系数：半径为 k·r 的圆角恰好包住半径 r 的超椭圆角，两者在 45° 对角线上相切。
 * 对角线上超椭圆离两条边各 r·(1 − 2^(−1/n))，圆离两条边各 k·r·(1 − √½)，令二者相等得 k。
 */
export const ROUND_SCALE = (1 - 2 ** (-1 / EXPONENT)) / (1 - Math.SQRT1_2);
