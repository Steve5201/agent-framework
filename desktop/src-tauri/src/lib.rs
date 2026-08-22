// ---------------------------------------------------------------------------
// 星云 Nebula 桌面端入口（Tauri 2）
//
// 职责：加载 web 构建产物（dev 走 :3000，生产走 frontendDist），
//       注册安全存储/通知插件，装配托盘与窗口行为。
//
// 前端复用 web/ 工程（desktop 不重复造 UI）；登录态安全存储见
// web/src/lib/storage.ts 的 Tauri 后端。
// ---------------------------------------------------------------------------

mod commands;
#[cfg(desktop)]
mod tray;

#[cfg(desktop)]
use std::sync::atomic::{AtomicBool, Ordering};

#[cfg(desktop)]
use tauri::{Manager, PhysicalSize, WindowEvent};
#[cfg(desktop)]
use tauri_plugin_notification::NotificationExt;

/// 是否处于"正在退出"状态（用户从托盘菜单点了退出 / 前端点了退出应用）。
/// 置位后窗口关闭事件不再拦截，进程真正退出。仅桌面端使用。
#[cfg(desktop)]
pub static QUITTING: AtomicBool = AtomicBool::new(false);

/// 是否已提示过"最小化到托盘"（只提示一次，避免每次关窗都打扰）。仅桌面端使用。
#[cfg(desktop)]
static HIDDEN_ONCE: AtomicBool = AtomicBool::new(false);

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    #[cfg(desktop)]
    let mut builder = tauri::Builder::default()
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_opener::init());
    #[cfg(not(desktop))]
    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_opener::init());

    // ---- 桌面端专属装配：托盘 + 窗口尺寸适配 + 关闭最小化到托盘 ----
    #[cfg(desktop)]
    {
        builder = builder
            .setup(|app| {
                tray::setup(app.handle())?;
                // 窗口初始尺寸适配当前屏幕：若 tauri.conf.json 默认尺寸（1200x800）
                // 超出主屏工作区（高 DPI 缩放 / 小屏），clamp 到工作区的 90% 再居中，
                // 避免窗口初始太大超出屏幕看不见标题栏/按钮。
                if let Some(w) = app.get_webview_window("main") {
                    if let Ok(Some(m)) = w.current_monitor() {
                        let wa = m.size(); // 物理像素
                        let max_w = (wa.width as f64 * 0.9) as u32;
                        let max_h = (wa.height as f64 * 0.9) as u32;
                        let cur = w.outer_size().unwrap_or_default();
                        let nw = cur.width.min(max_w).max(900);
                        let nh = cur.height.min(max_h).max(640);
                        if nw != cur.width || nh != cur.height {
                            let _ = w.set_size(PhysicalSize::new(nw, nh));
                        }
                        let _ = w.center();
                    }
                }
                Ok(())
            })
            .on_window_event(|window, event| {
                // 关闭主窗口 = 最小化到托盘（后台常驻），只有"退出"（托盘菜单 /
                // 前端退出应用按钮）才真正结束进程。首次隐藏时发系统通知提示退出途径。
                if let WindowEvent::CloseRequested { api, .. } = event {
                    if !QUITTING.load(Ordering::SeqCst) {
                        window.hide().ok();
                        api.prevent_close();
                        if !HIDDEN_ONCE.swap(true, Ordering::SeqCst) {
                            let _ = window
                                .app_handle()
                                .notification()
                                .builder()
                                .title("星云 Nebula 仍在运行")
                                .body("已最小化到系统托盘：右键托盘图标可退出，或在侧栏点击电源按钮")
                                .show();
                        }
                    }
                }
            });
    }

    builder
        .invoke_handler(tauri::generate_handler![
            commands::app_info,
            commands::app_exit,
            commands::tokens_get,
            commands::tokens_set,
            commands::tokens_clear,
            // 记住密码：系统凭据库加密存储（登录页勾选）
            commands::remember_credentials_set,
            commands::remember_credentials_get,
            commands::remember_credentials_clear,
            // 聊天消息里的外部链接：交给系统默认浏览器打开（防 webview 内导航替换界面）
            commands::open_external,
            // 阶段3·本地工具代理：桌面端执行本地 shell 命令
            commands::local_shell_execute
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
