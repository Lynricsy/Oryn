package com.lynricsy.oryn

import android.os.Bundle
import android.webkit.WebView
import androidx.activity.enableEdgeToEdge
import com.lynricsy.oryn.mobile.OrynMobilePlugin

class MainActivity : TauriActivity() {
  override fun onCreate(savedInstanceState: Bundle?) {
    enableEdgeToEdge()
    super.onCreate(savedInstanceState)
    // 先前的 Activity 被系统销毁、进程留着时，Rust 要为这个 Activity 重建主窗口（插件判断要不要）
    OrynMobilePlugin.activityCreated(this)
  }

  /**
   * wry 每建一个主 WebView 都调这里（含 Activity 因字体大小等未声明的配置变更重建后的那个）。
   * Tauri 只在第一次给插件调 `load`，所以由这里把新的 Activity 与 WebView 交给插件重新绑定。
   */
  override fun onWebViewCreate(webView: WebView) {
    super.onWebViewCreate(webView)
    OrynMobilePlugin.webViewCreated(this, webView)
  }
}
