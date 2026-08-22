# android 安卓端文档

> 阅读对象：需要在安卓端构建/调试、或为移动端适配壳代码的开发者。
> 安卓端与桌面端共用同一个 **Tauri 2 + Rust 壳**（`desktop/`），前端 100% 复用 `web/` 构建产物——**没有独立 UI**。
> 状态：✅ 已完成壳适配，真机 debug 包可构建安装；🚧 规划中（release 签名上架等）。

---

## 1. 定位：安卓端是什么

安卓端 = 桌面端同一个 `desktop/` Tauri 工程，目标平台从 Windows 换成 Android。**前端（`web/`）与业务逻辑完全复用**，只在 Rust 壳层面为安卓做了条件编译适配。

- **同一套代码**：`desktop/src-tauri` 一个工程，`tauri build` 出 Windows 包，`tauri android build` 出安卓 APK。
- **前端复用**：dev 加载 `:3001`，打包嵌入 `web/dist`（与桌面端完全一致）。
- **安卓无托盘**：桌面端的系统托盘/最小化到托盘/多窗口管理是桌面专属，安卓端已用 `#[cfg(desktop)]` 隔离，不会编译。

一句话：**同一份 web + 同一个 Rust 壳，多一个安卓打包目标。**

## 2. 平台差异（壳层）

| 能力 | 桌面端（Windows） | 安卓端 |
|---|---|---|
| 系统托盘 | ✅ `tray.rs` | ❌ 无托盘，整个模块 `#[cfg(desktop)]` 不编译 |
| 关闭=最小化到托盘 | ✅ | ❌ 安卓 App 生命周期由系统管理 |
| `app_exit` | 置 `QUITTING` → `app.exit(0)` | 直接 `app.exit(0)`（无托盘隐藏逻辑） |
| 本地 shell（`local_shell_execute`） | ✅ cmd/sh | ⚠️ 走 `sh -c`，但受安卓沙盒/SELinux 限制（见 §5） |
| 系统通知 | ✅ tauri-plugin-notification | ✅ 插件同样注册 |
| 外部链接/文件下载打开 | ✅ 系统默认浏览器 | ✅ 改用 **tauri-plugin-opener**（系统 Intent 开浏览器，避免 webview 内导航替换页面） |
| 返回键/边缘滑动 | 无（桌面） | ✅ 覆盖层栈拦截：配置弹窗/菜单/会话抽屉打开时返回键关闭该层，不退出应用 |
| 明文 HTTP 访问 | 无限制 | debug 构建允许（`usesCleartextTraffic=true`）；release 需 HTTPS |

## 3. 构建环境（本机已配好）

以下环境在开发机 `D:\AndroidStudioSdk` 就绪：

| 组件 | 位置/版本 | 说明 |
|---|---|---|
| **Android SDK** | `D:\AndroidStudioSdk` | `ANDROID_HOME` / `ANDROID_SDK_ROOT` 已设为用户级环境变量 |
| **NDK** | `D:\AndroidStudioSdk\ndk\30.0.15729638` | `ANDROID_NDK_HOME` 已设 |
| **platform android-36** | SDK 内 | `compileSdk=36` 需要（注意：还装有 android-37） |
| **build-tools 35 / 36** | SDK 内 | gradle 自动补齐 35 |
| **cmdline-tools** | `D:\AndroidStudioSdk\cmdline-tools\latest` | Tauri CLI 环境检查必需（含 sdkmanager） |
| **JDK 17** | `C:\Program Files\Java\jdk-17` | `JAVA_HOME` 已配；Gradle 8.14.3 兼容 |
| **Rust 安卓目标** | rustup | `aarch64-linux-android` / `armv7-linux-androideabi` / `x86_64-linux-android` / `i686-linux-android` |
| **Gradle 8.14.3** | `~/.gradle/wrapper/dists` | 官方源超时，已用腾讯镜像手动下载到位 |
| **Windows Developer Mode** | 已开启 | 需要创建符号链接（`.so` → jniLibs） |

**注意**：这些环境变量/Developer Mode 是用户级配置，换新机需重新配置（见 §7 迁移清单）。

### 3.1 Rust 链接器配置（关键）

`desktop/src-tauri/.cargo/config.toml` 指定了 4 个安卓目标的 NDK clang 作为链接器：

```toml
[target.aarch64-linux-android]
linker = "D:/AndroidStudioSdk/ndk/30.0.15729638/toolchains/llvm/prebuilt/windows-x86_64/bin/aarch64-linux-android21-clang.cmd"
[target.armv7-linux-androideabi]
linker = ".../armv7a-linux-androideabi21-clang.cmd"
[target.i686-linux-android]
linker = ".../i686-linux-android21-clang.cmd"
[target.x86_64-linux-android]
linker = ".../x86_64-linux-android21-clang.cmd"
```

> ⚠️ 该文件在 `src-tauri` 下，cargo 需从 `src-tauri` 目录运行才读到（`--manifest-path` 从桌面目录跑可能找不到，见 §8 排障）。

## 4. 构建命令

```powershell
# 一次性设置（每次新终端）：
$env:ANDROID_HOME="D:\AndroidStudioSdk"
$env:ANDROID_SDK_ROOT="D:\AndroidStudioSdk"
$env:ANDROID_NDK_HOME="D:\AndroidStudioSdk\ndk\30.0.15729638"

cd D:\Agent\desktop

# 初始化安卓工程（首次/重新生成 gen/android）
npx tauri android init

# 构建 debug 版（真机测试用，Android Debug 签名，可直接安装）
npx tauri android build --target aarch64 --debug

# 构建 release 版（上架用，需先配签名 keystore）
npx tauri android build --target aarch64
```

### 产物位置

| 构建 | APK | AAB |
|---|---|---|
| debug | `src-tauri/gen/android/app/build/outputs/apk/universal/debug/app-universal-debug.apk` | `.../bundle/universalDebug/app-universal-debug.aab` |
| release | `.../apk/universal/release/app-universal-release-unsigned.apk` | `.../bundle/universalRelease/app-universal-release.aab` |

> **debug APK 用 Android 调试签名**（`CN=Android Debug`），可直接 `adb install`。
> **release APK 是 unsigned**，需配置签名 keystore 才能安装/上架（见 §6）。

### 一键构建 + 安装到真机（免手动复制 APK）

`scripts/android-install.ps1` 封装"构建 + adb 安装"，无需手动把 APK 拷到手机：

```powershell
.\scripts\android-install.ps1                # 构建 aarch64 debug + 安装到已连真机
.\scripts\android-install.ps1 -SkipBuild     # 跳过构建，复用上次 debug 包重装
.\scripts\android-install.ps1 -CleanInstall  # 卸载旧包再装（清数据）
```

前置：真机开 USB 调试并连接（`adb devices` 可见），手机需开启"通过 USB 安装应用"并在安装时点允许。
应用包名：`com.nebula.agent`（若手机上有旧包名残留，需先卸载旧版）。

> **Chrome DevTools 远程调试 UI**：debug 版 webview 调试默认开启，电脑 Chrome 打开 `chrome://inspect/#devices`，点设备对应 app 的 inspect 即可实时改 DOM / 看样式与 console——手机 UI 适配调试的首选工具。

## 5. 移动端交互适配（手机 UI/操作）

安卓端前端复用 `web/` 的响应式布局，并针对手机操作做了专项适配：

### 5.1 外部链接 / 图片 / 文件下载 → 系统默认浏览器

- **根因**：桌面端 `open_external` 命令走 `xdg-open`，但安卓无此程序 → 前端回退 `window.open` 在 webview 内导航，整个界面被目标网页替换。
- **修复**：改用官方 **`tauri-plugin-opener`**（`Cargo.toml` + `lib.rs` 注册 + `capabilities/default.json` 加 `opener:default`）。前端 `web/src/lib/external.ts` 的 `openExternal`、`rich.ts` 的 `downloadUrl` 在 Tauri 环境统一走 opener `openUrl`——**安卓用系统 Intent 打开默认浏览器**，不再劫持页面。
- 覆盖：AI 气泡内 Markdown 链接、渲染图片/视频下载、文件卡片下载/预览。

### 5.2 返回键 / 边缘滑动（不退回桌面）

- **背景**：安卓物理返回键默认——有导航历史回退历史页，否则退出应用；但应用内覆盖层（配置弹窗/菜单面板/会话列表抽屉）不是独立历史页，返回键会直接退桌面。
- **方案**：`web/src/lib/backstack.ts` 维护"可关闭覆盖层栈"。覆盖层打开时 `registerBackHandler(close)` + `pushState` 制造虚拟历史；关闭时 `unregisterBackHandler()` + `history.back()`。安卓返回键触发 `popstate` → 关闭栈顶覆盖层。栈空时才允许退出。
- 接入点：`ChatPage`（会话抽屉）、`ConfigButtonArea`（配置弹窗）、`MenuButton`（菜单面板）。

### 5.3 统一工具条（上传 + 配置按钮）

- 文件上传按钮与配置按钮放进**同一父组件**（`ConfigButtonArea` 接受 `leading` 上传按钮），同尺寸（44px 触控）、同视觉（圆角 icon），超出屏幕**横向滑动**选择——避免按钮变大后部分被挤出屏幕外。

### 5.4 品牌（Nebula 星云）浅色图标

- 图标源 `web/public/nebula-icon.svg` 为**浅色系**：浅紫蓝渐变背景 + 星空蓝渐变核心。
- `NebulaLogo.tsx` 内联 SVG 组件用 `useId()` 生成唯一 gradient/filter id——**避免多个实例同页渲染时 ID 冲突导致背景消失**（同 id 会解析到首个实例，`showBg=false` 实例缺渐变会让后续实例背景失效）。
- **可见性**：`NebulaLogo` 默认**仅移动端（安卓）显示**（内部用 `useIsMobile()` 判断，网页端/桌面端内部 UI 返回 null）。品牌图标只用于安卓端界面 + 安装包/桌面快捷方式/网站 favicon（`public/nebula-icon.svg` 与 tauri 生成图标），不在网页/桌面端界面内重复展示。
- 桌面/安卓 launcher 图标由该源生成（`npx tauri icon`）。注意：`tauri icon` 生成的安卓 launcher 大尺寸会变透明，需手动用方形背景 PNG 覆盖 `gen/android/app/src/main/res/mipmap-*/ic_launcher*.png`。

## 6. 安卓端的 local shell（本地工具）

`local_shell_execute` 在安卓上**可以编译并运行**（走 `sh -c`，安卓有 `/system/bin/sh`），但有平台限制：

| 项 | 桌面端 | 安卓端 |
|---|---|---|
| Shell 能力 | cmd/sh 完整系统命令 | App 沙盒进程内的 `/system/bin/sh`，常规命令（echo/ls/cat/管道）可用 |
| 文件系统 | 全盘 | 只能访问 **App 私有目录** + 公共存储需权限；不能操作别的 App 文件 |
| 系统级命令 | 自由 | 大部分受 SELinux 限制（如 `pm`/`am`/`reboot` 需 root） |
| root | 不需要 | 系统级操作需要 root（绝大多数设备无） |

**结论**：面向"App 沙盒内文件/脚本操作"够用；系统级管理需换 Android 原生方案（Service/WorkManager），属原生开发范畴。

## 7. Release 签名（✅ 已配置）

release 版已配置签名 keystore（本机 `desktop/nebula-release.keystore`，**已 gitignore**，换机需重新生成）：

1. 生成 keystore：`keytool -genkey -v -keystore nebula-release.keystore -alias nebula -keyalg RSA -keysize 2048 -validity 10000 ...`
2. 在 `gen/android/app/build.gradle.kts` 的 `signingConfigs` 创建 `release`（storeFile 指向 `../../../../nebula-release.keystore`），并在 `release` buildType 引用 `signingConfig = signingConfigs.getByName("release")`；同时 release 设 `usesCleartextTraffic=true`（当前后端为 http，允许明文，后续上 HTTPS 可改回 false）
3. `npx tauri android build --target aarch64` → 产出**已签名** `app-universal-release.apk`，可直接安装/分发

> ⚠️ `gen/android` 是生成的、gitignore；签名配置只在本机 gen/android 生效。新机需重新 `tauri android init` + 重新配置签名 + 重新生成 keystore。
> debug 包用 Android 调试签名，无需此配置。

## 8. 环境迁移清单（换新机时）

1. 装 Android Studio → SDK/NDK；装 JDK 17
2. `ANDROID_HOME`/`ANDROID_SDK_ROOT`/`ANDROID_NDK_HOME`/`JAVA_HOME` 设为用户级环境变量
3. `rustup target add aarch64-linux-android armv7-linux-androideabi x86_64-linux-android i686-linux-android`
4. 补装 SDK 组件：`platforms;android-36`、`build-tools;35`、cmdline-tools
5. 启用 **Windows Developer Mode**（符号链接权限）
6. 确认 `src-tauri/.cargo/config.toml` 的 NDK 路径与本机一致
7. Gradle 发行包若下载超时，用国内镜像手动下载放 `~/.gradle/wrapper/dists`

## 9. 排障速查

| 现象 | 根因 | 解决 |
|---|---|---|
| `failed to ensure Android environment` | 缺 cmdline-tools | SDK 需装 `cmdline-tools/latest`（含 sdkmanager） |
| `Creation symbolic link is not allowed` | 未开 Developer Mode | 开启 Windows 开发者模式 |
| `linker cc not found` | 缺 `.cargo/config.toml` 或从错误目录运行 | 确认 `src-tauri/.cargo/config.toml` 存在；从 `src-tauri` 目录跑 cargo |
| `Trailing char < > ... D:\AndroidStudioSdk ` | cmd 里 `set VAR=path &&` 的 `&&` 前空格被吸进变量 | 用 `set VAR=path&&`（`&&` 前不加空格） |
| `.so does not include required runtime symbols` | 缺 `tauri::mobile_entry_point` | `lib.rs` 的 `run()` 加 `#[cfg_attr(mobile, tauri::mobile_entry_point)]` |
| Gradle 发行包下载超时 | 官方源网络慢 | 腾讯镜像 `mirrors.cloud.tencent.com/gradle` 手动下载放 wrapper dists |