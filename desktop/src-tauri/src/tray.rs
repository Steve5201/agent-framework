// ---------------------------------------------------------------------------
// 系统托盘（P2-85）
//
// 行为约定：
//   - 托盘菜单"显示主窗口" / "退出"；
//   - 单击托盘图标显示主窗口；
//   - 关闭主窗口默认最小化到托盘（见 lib.rs 的 on_window_event）。
//
// 仅桌面端：安卓无系统托盘，整个模块在非 desktop 目标下不编译。
// ---------------------------------------------------------------------------

#![cfg(desktop)]

use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    AppHandle, Manager,
};

pub fn setup(app: &AppHandle) -> tauri::Result<()> {
    let show = MenuItem::with_id(app, "show", "显示主窗口", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "退出", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &quit])?;

    TrayIconBuilder::with_id("main-tray")
        // bundle.icon 已配置，generate_context! 会嵌入默认窗口图标；取不到即配置错误
        .icon(
            app.default_window_icon()
                .cloned()
                .expect("缺少默认窗口图标，请检查 tauri.conf.json 的 bundle.icon"),
        )
        .tooltip("星云 Nebula")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id().as_ref() {
            "show" => show_main_window(app),
            "quit" => {
                crate::QUITTING.store(true, std::sync::atomic::Ordering::SeqCst);
                app.exit(0);
            }
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            // 仅左键单击（弹起）显示并聚焦主窗口。
            // 注意不能匹配所有 Click：右键点击同样会触发 Click 事件，若一并
            // 唤起窗口，会抢走原生右键菜单的焦点，导致菜单"闪一下"就消失。
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                show_main_window(tray.app_handle());
            }
        })
        .build(app)?;

    Ok(())
}

fn show_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        window.show().ok();
        window.unminimize().ok();
        window.set_focus().ok();
    }
}
