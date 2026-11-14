package com.lynricsy.oryn

import android.app.Application

/** 进程入口：Activity、推送服务等任何组件拉起进程时都先装好本 flavor 的推送通道。 */
class OrynApp : Application() {
  override fun onCreate() {
    super.onCreate()
    Flavor.install(this)
  }
}
