// Windows 下发布构建隐藏控制台窗口（打包成 GUI 应用，不弹黑框）。
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    agent_desktop_lib::run()
}
