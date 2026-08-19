// ---------------------------------------------------------------------------
// 阶段3·本地工具代理：local_shell_execute
//
// 前端收到 SSE 的 tool_call 事件（本地工具 local_shell）并弹确认后调用本
// 命令，在本机执行 shell 命令（30s 超时），返回 stdout+stderr+退出码，
// 前端再经 gateway 上行 API 回填给 agent-service 唤醒挂起会话。
//
// 安全边界：命令全文展示给用户确认后才执行；超时自动终止子进程。
// ---------------------------------------------------------------------------

/// 本地 shell 执行结果（前端直接回填给 agent-service）。
#[derive(Debug, Serialize)]
pub struct LocalExecResult {
    pub content: String,
    pub is_error: bool,
}

/// 本地 shell 命令默认超时（可用环境变量 LOCAL_EXEC_TIMEOUT_SECS 覆盖，单测用）。
fn local_exec_timeout() -> std::time::Duration {
    std::env::var("LOCAL_EXEC_TIMEOUT_SECS")
        .ok()
        .and_then(|v| v.parse().ok())
        .map(std::time::Duration::from_secs)
        .unwrap_or(std::time::Duration::from_secs(30))
}

/// 计算命令执行的超时截止时间。
/// None/0 = 采用默认超时（local_exec_timeout）；正值 = 强制该秒数；
/// -1 = 不限超时（返回 None，命令可持续运行直至完成）。
fn local_exec_deadline(timeout_secs: Option<i64>) -> Option<std::time::Instant> {
    match timeout_secs {
        Some(n) if n > 0 => Some(std::time::Instant::now() + std::time::Duration::from_secs(n as u64)),
        Some(-1) => None,
        _ => Some(std::time::Instant::now() + local_exec_timeout()),
    }
}

/// 读取管道到 EOF（子进程退出后自动结束），返回原始字节。
///
/// 历史教训：早期用 read_to_string 读取，Windows cmd 的中文输出（GBK 编码）
/// 不是合法 UTF-8，read_to_string 直接失败导致输出被整体丢弃——表现为
/// "退出码 1，无输出"。改为读字节后由 decode_output 统一解码。
fn read_pipe<R: std::io::Read>(mut r: R) -> Vec<u8> {
    let mut buf = Vec::new();
    let _ = r.read_to_end(&mut buf);
    buf
}

/// 解码子进程输出字节：优先 UTF-8；非 UTF-8 时在 Windows 上用 GBK（cp936，
/// 中文系统 cmd 默认编码）兜底，其它平台用 lossy 替换。
fn decode_output(bytes: &[u8]) -> String {
    match std::str::from_utf8(bytes) {
        Ok(s) => s.to_string(),
        Err(_) if cfg!(windows) => {
            let (cow, _, _) = encoding_rs::GBK.decode(bytes);
            cow.into_owned()
        }
        Err(_) => String::from_utf8_lossy(bytes).into_owned(),
    }
}

/// 临时 bat 文件路径（进程号 + 纳秒时间戳，避免并发冲突）。
fn temp_bat_path() -> std::path::PathBuf {
    let name = format!(
        "agent_shell_{}_{}.bat",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos())
            .unwrap_or(0)
    );
    std::env::temp_dir().join(name)
}

/// 在阻塞线程中执行命令：spawn → 双管道读取线程 + try_wait 轮询 + 超时 kill。
///
/// Windows 实现细节（历史 bug 修复）：
///   cmd /C <命令串> 直接把含双引号的命令串作为参数传过去时，参数引号被
///   转义成 \"，cmd.exe 无法解析 → `type "D:\a.txt"` 报"文件名、目录名或
///   卷标语法不正确"。规避方案：把命令原样写入临时 .bat 文件（内容不经过
///   任何参数转义），再 `cmd /C <bat>` 执行；bat 内容用 GBK 编码，中文路径
///   在中文系统上也能正确解析。执行后立即删除临时文件。
fn run_local_shell_blocking(command: &str, cwd: Option<String>, timeout_secs: Option<i64>) -> Result<LocalExecResult, String> {
    use std::io::Write;
    use std::process::{Command, Stdio};
    use std::thread;
    use std::time::Instant;

    let (program, args, cleanup): (&str, Vec<std::ffi::OsString>, Box<dyn Fn()>) =
        if cfg!(windows) {
            // bat 内容：@echo off 抑制回显；末尾 exit /b %ERRORLEVEL% 把退出码
            // 正确传播给 cmd 进程。bat 用 GBK 编码写入（ASCII 命令 = 字节不变）。
            let bat = temp_bat_path();
            let script = format!(
                "@echo off\r\n{command}\r\nexit /b %ERRORLEVEL%\r\n"
            );
            let (encoded, _, _) = encoding_rs::GBK.encode(script.as_str());
            let mut f = std::fs::File::create(&bat).map_err(|e| format!("创建临时脚本失败: {e}"))?;
            f.write_all(&encoded).map_err(|e| format!("写入临时脚本失败: {e}"))?;
            drop(f);
            let cleanup_bat = bat.clone();
            let c = move || {
                let _ = std::fs::remove_file(&cleanup_bat);
            };
            ("cmd", vec!["/C".into(), bat.as_os_str().into()], Box::new(c))
        } else {
            // Unix：sh -c 无引号问题，无需临时文件。
            ("sh", vec!["-c".into(), command.into()], Box::new(|| {}))
        };

    let mut cmd = Command::new(program);
    cmd.args(&args).stdin(Stdio::null());
    if let Some(dir) = cwd {
        if !dir.trim().is_empty() {
            cmd.current_dir(&dir);
        }
    }

    let mut child = cmd
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| {
            cleanup();
            format!("启动命令失败: {e}")
        })?;
    let stdout = child.stdout.take().expect("stdout 管道可取");
    let stderr = child.stderr.take().expect("stderr 管道可取");

    // 读取线程：子进程退出 / 管道关闭后 read_to_end 返回。
    let out_th = thread::spawn(move || read_pipe(stdout));
    let err_th = thread::spawn(move || read_pipe(stderr));

    let deadline = local_exec_deadline(timeout_secs);
    let status = loop {
        match child.try_wait() {
            Ok(Some(st)) => break st,
            Ok(None) => {
                // deadline 为 None（不限超时）时永不超时终止。
                if let Some(deadline) = deadline {
                    if Instant::now() >= deadline {
                        let _ = child.kill();
                        cleanup();
                        return Err("命令执行超时，已终止".to_string());
                    }
                }
                thread::sleep(std::time::Duration::from_millis(50));
            }
            Err(e) => {
                cleanup();
                return Err(format!("等待命令失败: {e}"));
            }
        }
    };

    let stdout = out_th.join().map_err(|_| "读取输出线程异常".to_string())?;
    let stderr = err_th.join().map_err(|_| "读取错误线程异常".to_string())?;
    cleanup();

    let stdout = decode_output(&stdout);
    let stderr = decode_output(&stderr);

    let mut content = String::new();
    if !stdout.trim().is_empty() {
        content.push_str(&stdout);
    }
    if !stderr.trim().is_empty() {
        if !content.is_empty() {
            content.push('\n');
        }
        content.push_str(&stderr);
    }
    if !status.success() {
        if !content.is_empty() {
            content.push('\n');
        }
        content.push_str(&format!("（退出码: {}）", status.code().unwrap_or(-1)));
    }
    if content.trim().is_empty() {
        content = "（命令执行完成，无输出）".to_string();
    }

    Ok(LocalExecResult {
        content,
        is_error: !status.success(),
    })
}

/// 本地 shell 执行命令（invoke('local_shell_execute')）。
/// spawn_blocking 保证不阻塞 Tauri 异步运行时/主线程。
/// timeout_secs：None/0 = 采用默认（30s，可用 LOCAL_EXEC_TIMEOUT_SECS 覆盖）；
/// 正值 = 强制该秒数超时；-1 = 不限超时（自由模式，命令可持续运行直至完成）。
#[tauri::command]
pub async fn local_shell_execute(
    command: String,
    cwd: Option<String>,
    timeout_secs: Option<i64>,
) -> Result<LocalExecResult, String> {
    tauri::async_runtime::spawn_blocking(move || run_local_shell_blocking(&command, cwd, timeout_secs))
        .await
        .map_err(|e| format!("本地执行任务异常: {e}"))?
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn local_shell_success_returns_output() {
        let res = run_local_shell_blocking("echo hello-world", None, None).unwrap();
        assert!(!res.is_error, "预期成功, got {}", res.content);
        assert!(res.content.contains("hello-world"), "got {}", res.content);
    }

    #[test]
    fn local_shell_nonzero_exit_is_error() {
        let res = run_local_shell_blocking("exit 3", None, None).unwrap();
        assert!(res.is_error, "非零退出码应标记失败, got {}", res.content);
        assert!(res.content.contains("退出码: 3"), "got {}", res.content);
    }

    #[test]
    fn local_shell_no_output_placeholder() {
        let cmd = if cfg!(windows) { "rem noop" } else { ":" };
        let res = run_local_shell_blocking(cmd, None, None).unwrap();
        assert!(!res.is_error);
        assert!(res.content.contains("无输出"), "got {}", res.content);
    }

    #[test]
    fn local_shell_timeout_kills_process() {
        std::env::set_var("LOCAL_EXEC_TIMEOUT_SECS", "1");
        let cmd = if cfg!(windows) {
            "ping -n 30 127.0.0.1 > nul"
        } else {
            "sleep 30"
        };
        let err = run_local_shell_blocking(cmd, None, None).expect_err("超时应返回错误");
        assert!(err.contains("超时"), "got {err}");
        std::env::remove_var("LOCAL_EXEC_TIMEOUT_SECS");
    }

    #[test]
    fn local_shell_no_timeout_runs_to_completion() {
        // 自由模式（timeout_secs = -1）：即使默认超时极短也不中断命令。
        std::env::set_var("LOCAL_EXEC_TIMEOUT_SECS", "1");
        let cmd = if cfg!(windows) {
            "ping -n 3 127.0.0.1 > nul"
        } else {
            "sleep 2"
        };
        let res = run_local_shell_blocking(cmd, None, Some(-1)).expect("不限超时应执行完成");
        assert!(!res.is_error, "命令应成功完成, got {}", res.content);
        std::env::remove_var("LOCAL_EXEC_TIMEOUT_SECS");
    }

    #[test]
    fn local_shell_positive_timeout_overrides_default() {
        // 显式正值超时（timeout_secs = 2）优先于默认超时（1s）：命令超过 2s 才成功，
        // 默认 1s 已可杀——此处验证正值被采用（命令不因 1s 默认被杀）。
        std::env::set_var("LOCAL_EXEC_TIMEOUT_SECS", "1");
        let cmd = if cfg!(windows) {
            "ping -n 3 127.0.0.1 > nul"
        } else {
            "sleep 2"
        };
        let res = run_local_shell_blocking(cmd, None, Some(3)).expect("显式超时应生效");
        assert!(!res.is_error, "命令应在显式超时内完成, got {}", res.content);
        std::env::remove_var("LOCAL_EXEC_TIMEOUT_SECS");
    }

    #[test]
    fn local_shell_windows_type_reads_file_with_space() {
        // 回归保护（历史 bug）：cmd /C <命令串> 直传时含双引号的路径会被转义
        // 破坏（报"文件名、目录名或卷标语法不正确"）。现改用临时 .bat 执行，
        // 路径含空格也必须能读。测试文件放在 crate 的 target/ 下。
        if !cfg!(windows) {
            return;
        }
        let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("target/local shell test");
        std::fs::create_dir_all(&dir).expect("创建测试目录失败");
        let file = dir.join("read me.txt");
        std::fs::write(&file, "skill-channel-ok").expect("写入测试文件失败");
        let res = run_local_shell_blocking(
            &format!("type \"{}\"", file.display()),
            Some(dir.to_string_lossy().into_owned()),
            None,
        )
        .expect("type 执行应成功");
        let _ = std::fs::remove_file(&file);
        assert!(!res.is_error, "type 应成功, got {}", res.content);
        assert!(res.content.contains("skill-channel-ok"), "got {}", res.content);
    }

    #[test]
    fn local_shell_windows_gbk_output_decodes() {
        // 回归保护（历史 bug）：cmd 的中文输出（GBK）曾被 read_to_string 静默
        // 丢弃 → "退出码 1，无输出"。现在字节级读取 + GBK 兜底解码，中文必须可读。
        if !cfg!(windows) {
            return;
        }
        let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("target/local_shell_test");
        std::fs::create_dir_all(&dir).expect("创建测试目录失败");
        let file = dir.join("gbk.txt");
        // "你" 的 GBK 编码字节（非合法 UTF-8，用于触发 GBK 兜底解码）。
        std::fs::write(&file, [0xC4, 0xE3]).expect("写入 GBK 测试文件失败");
        let res = run_local_shell_blocking(
            &format!("type \"{}\"", file.display()),
            Some(dir.to_string_lossy().into_owned()),
            None,
        )
        .expect("type 执行应成功");
        let _ = std::fs::remove_file(&file);
        assert!(!res.is_error, "type 应成功, got {}", res.content);
        assert!(res.content.contains('你'), "GBK 中文应被解码, got {}", res.content);
    }
}

use std::fs;
use std::sync::atomic::Ordering;

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager};

/// 应用基础信息：版本 / 平台。前端可据此做环境差异展示。
#[derive(Serialize)]
pub struct AppInfo {
    version: String,
    platform: String,
}

/// 登录态 token 对（access + refresh）。
#[derive(Debug, Serialize, Deserialize)]
pub struct TokenPair {
    pub access_token: String,
    pub refresh_token: String,
}

/// 会话文件：%APPDATA%/com.agentframework.desktop/session.json
/// （Linux/macOS 对应各自的 app_config_dir）。
const SESSION_FILE: &str = "session.json";

fn session_path(app: &AppHandle) -> Result<std::path::PathBuf, String> {
    let dir = app
        .path()
        .app_config_dir()
        .map_err(|e| format!("无法定位应用配置目录: {e}"))?;
    Ok(dir.join(SESSION_FILE))
}

#[tauri::command]
pub fn app_info(app: AppHandle) -> AppInfo {
    AppInfo {
        version: app.package_info().version.to_string(),
        platform: std::env::consts::OS.to_string(),
    }
}

/// 退出整个桌面应用（前端"退出应用"按钮调用，配合托盘"退出"）。
/// 置位 QUITTING 后 exit：窗口关闭拦截逻辑（lib.rs）看到置位不再隐藏，进程真正退出。
#[tauri::command]
pub fn app_exit(app: AppHandle) {
    crate::QUITTING.store(true, Ordering::SeqCst);
    app.exit(0);
}

#[tauri::command]
pub fn tokens_get(app: AppHandle) -> Result<Option<TokenPair>, String> {
    let path = session_path(&app)?;
    if !path.exists() {
        return Ok(None);
    }
    let raw = fs::read_to_string(&path).map_err(|e| format!("读取会话文件失败: {e}"))?;
    let tokens: TokenPair =
        serde_json::from_str(&raw).map_err(|e| format!("解析会话文件失败: {e}"))?;
    Ok(Some(tokens))
}

#[tauri::command]
pub fn tokens_set(app: AppHandle, tokens: TokenPair) -> Result<(), String> {
    let path = session_path(&app)?;
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir).map_err(|e| format!("创建配置目录失败: {e}"))?;
    }
    // 先写临时文件再 rename：避免中途崩溃/断电留下半截损坏文件
    let tmp = path.with_extension("json.tmp");
    let raw = serde_json::to_string_pretty(&tokens).map_err(|e| format!("序列化失败: {e}"))?;
    fs::write(&tmp, raw).map_err(|e| format!("写入会话文件失败: {e}"))?;
    fs::rename(&tmp, &path).map_err(|e| format!("落盘会话文件失败: {e}"))?;
    Ok(())
}

#[tauri::command]
pub fn tokens_clear(app: AppHandle) -> Result<(), String> {
    let path = session_path(&app)?;
    if path.exists() {
        fs::remove_file(&path).map_err(|e| format!("删除会话文件失败: {e}"))?;
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// 记住密码（remember_credentials_*）
//
// 背景：登录页"记住密码"勾选后，把用户名+密码存入**系统凭据库**（Windows
// 凭据管理器 / macOS Keychain / Linux Secret Service），由系统加密保护，
// 比明文落盘安全。浏览器环境无此能力，由前端回退 localStorage（见
// web/src/lib/remember.ts，风险已在文档注明）。
// ---------------------------------------------------------------------------

/// 凭据条目服务名（唯一命名空间，避免与其它应用冲突）。
const REMEMBER_SERVICE: &str = "com.agentframework.desktop.login";
/// 凭据条目用户名（单条 JSON 存 username+password，无需枚举条目）。
const REMEMBER_ACCOUNT: &str = "saved-credentials";

/// 保存记住的凭据（覆盖写入）。密码为空或用户名为空 → 拒绝。
#[tauri::command]
pub fn remember_credentials_set(username: String, password: String) -> Result<(), String> {
    if username.trim().is_empty() || password.is_empty() {
        return Err("凭据不能为空".to_string());
    }
    let blob = serde_json::json!({ "username": username, "password": password }).to_string();
    keyring::Entry::new(REMEMBER_SERVICE, REMEMBER_ACCOUNT)
        .and_then(|e| e.set_password(&blob))
        .map_err(|e| format!("保存凭据失败: {e}"))
}

/// 读取记住的凭据；未保存 / 读取失败（如凭据损坏）返回 None（不阻塞登录）。
#[tauri::command]
pub fn remember_credentials_get() -> Result<Option<serde_json::Value>, String> {
    let entry = keyring::Entry::new(REMEMBER_SERVICE, REMEMBER_ACCOUNT)
        .map_err(|e| format!("创建凭据条目失败: {e}"))?;
    let blob = match entry.get_password() {
        Ok(v) => v,
        Err(keyring::Error::NoEntry) => return Ok(None), // 从未保存过 → 正常空
        Err(e) => return Err(format!("读取凭据失败: {e}")),
    };
    serde_json::from_str::<serde_json::Value>(&blob)
        .map(Some)
        .map_err(|e| format!("凭据数据损坏: {e}"))
}

/// 清除记住的凭据（取消勾选 / 登出时调用）。未保存时幂等成功。
#[tauri::command]
pub fn remember_credentials_clear() -> Result<(), String> {
    let entry = keyring::Entry::new(REMEMBER_SERVICE, REMEMBER_ACCOUNT)
        .map_err(|e| format!("创建凭据条目失败: {e}"))?;
    match entry.delete_credential() {
        Ok(()) => Ok(()),
        Err(keyring::Error::NoEntry) => Ok(()), // 本来就没有 → 幂等
        Err(e) => Err(format!("清除凭据失败: {e}")),
    }
}

#[cfg(test)]
mod remember_credentials_tests {
    use super::*;

    #[test]
    fn set_rejects_empty_input() {
        assert!(remember_credentials_set(String::new(), "pw".to_string()).is_err());
        assert!(remember_credentials_set("alice".to_string(), String::new()).is_err());
    }

    // 注意：set/get/clear 走系统凭据库，涉及本机凭证，不放单元测试
    // （避免测试污染用户真实凭据）。CI 仅验证参数校验逻辑。
}

// ---------------------------------------------------------------------------
// 外部链接打开（open_external）
//
// 背景：桌面端 WebView2 点击 <a> 默认在当前 webview 内导航，整个界面会被
// 目标网页替换（应用"消失"）。前端（web/src/lib/external.ts）统一拦截
// Markdown 链接点击后调本命令，交给系统默认浏览器打开。
// ---------------------------------------------------------------------------

/// 允许交给系统浏览器打开的外部协议白名单（防命令注入：拒绝其它协议/壳命令）。
const EXTERNAL_SCHEMES: [&str; 4] = ["http://", "https://", "mailto:", "tel:"];

/// 校验外部链接：仅放行白名单协议，且长度受限。
fn validate_external_url(url: &str) -> Result<(), String> {
    if url.trim().is_empty() {
        return Err("链接为空".to_string());
    }
    if url.len() > 2048 {
        return Err("链接过长".to_string());
    }
    let lower = url.to_ascii_lowercase();
    if !EXTERNAL_SCHEMES.iter().any(|p| lower.starts_with(p)) {
        return Err("仅支持 http/https/mailto/tel 链接".to_string());
    }
    Ok(())
}

/// 用系统默认处理程序打开目标（URL 或本地文件路径）。
/// Windows: `cmd /C start "" "<target>"`；macOS: `open`；Linux: `xdg-open`。
/// start 的首个 "" 是窗口标题占位，target 由 std::process::Command 自动按需加引号，
/// 含空格/& 的目标不会被 cmd 拆词。
fn spawn_open(target: &str) -> Result<(), String> {
    use std::process::Command;

    let (program, args): (&str, Vec<&str>) = if cfg!(windows) {
        ("cmd", vec!["/C", "start", "", target])
    } else if cfg!(target_os = "macos") {
        ("open", vec![target])
    } else {
        ("xdg-open", vec![target])
    };
    Command::new(program)
        .args(&args)
        .spawn()
        .map_err(|e| format!("打开系统默认程序失败: {e}"))?;
    Ok(())
}

/// 打开外部链接（前端 invoke('open_external', { url })）。
/// 常规 http/https/mailto/tel 交给系统默认浏览器打开；data:image/* 数据 URL
/// （前端图表/SVG 下载按钮生成）解码写入系统临时文件后用默认程序打开——Windows
/// 没有注册 data: 协议的 ShellExecute 处理程序，直接 `start "data:..."` 会失败，
/// 必须先落盘成图片文件。
#[tauri::command]
pub fn open_external(url: String) -> Result<(), String> {
    if url.to_ascii_lowercase().starts_with("data:image/") {
        return open_data_image(&url);
    }
    validate_external_url(&url)?;
    spawn_open(&url)
}

/// data:image 数据 URL 解码后的大小上限（前端 PNG 截图一般 <1MB，防超长内存/落盘）。
const MAX_DATA_IMAGE_BYTES: usize = 8 * 1024 * 1024;

/// 解析 data:image/* 数据 URL，解码写入系统临时文件并用默认程序打开。
/// MIME 白名单只放行常见图片类型——防止把任意内容伪装成可执行/脚本扩展名落地后打开执行。
fn open_data_image(url: &str) -> Result<(), String> {
    let (meta, payload) = url.split_once(',').ok_or("data URL 缺少内容")?;
    if payload.len() > MAX_DATA_IMAGE_BYTES {
        return Err("图片数据过大".to_string());
    }
    // 头部形如 data:image/svg+xml;charset=utf-8;base64
    let head = meta.trim_start_matches("data:");
    let (mediatype, is_base64) = match head.find(';') {
        Some(idx) => (&head[..idx], head[idx..].contains(";base64")),
        None => (head, false),
    };
    let ext = match mediatype.to_ascii_lowercase().as_str() {
        "image/svg+xml" => "svg",
        "image/png" => "png",
        "image/jpeg" => "jpg",
        "image/webp" => "webp",
        "image/gif" => "gif",
        "image/bmp" => "bmp",
        other => return Err(format!("不支持的图片类型: {other}")),
    };
    let bytes = if is_base64 {
        decode_base64(payload).map_err(|e| format!("base64 解码失败: {e}"))?
    } else {
        percent_decode(payload)
    };
    if bytes.is_empty() {
        return Err("图片内容为空".to_string());
    }
    if bytes.len() > MAX_DATA_IMAGE_BYTES {
        return Err("图片数据过大".to_string());
    }
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    let path = std::env::temp_dir().join(format!(
        "agent_download_{}_{}.{ext}",
        std::process::id(),
        nanos
    ));
    fs::write(&path, &bytes).map_err(|e| format!("写入临时文件失败: {e}"))?;
    spawn_open(path.to_str().ok_or("临时文件路径非法")?)?;
    // 延迟清理：等默认程序读取完毕后删除；失败忽略（系统临时目录兜底回收）。
    let p = path.clone();
    std::thread::spawn(move || {
        std::thread::sleep(std::time::Duration::from_secs(120));
        let _ = fs::remove_file(&p);
    });
    Ok(())
}

/// 最小 base64 解码（RFC 4648 标准字母表，容忍缺失 padding）。
/// 仅为 data URL 图片负载设计，不覆盖 URL-safe 等变体。
fn decode_base64(input: &str) -> Result<Vec<u8>, String> {
    const TABLE: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut lut = [0xFFu8; 128];
    for (i, &c) in TABLE.iter().enumerate() {
        lut[c as usize] = i as u8;
    }
    let mut out = Vec::with_capacity(input.len() / 4 * 3);
    let mut acc: u32 = 0;
    let mut bits: u32 = 0;
    for &b in input.as_bytes() {
        if b == b'=' {
            break; // padding 之后无有效数据
        }
        if b >= 128 {
            return Err("非法 base64 字符".to_string());
        }
        let v = lut[b as usize];
        if v == 0xFF {
            return Err("非法 base64 字符".to_string());
        }
        acc = (acc << 6) | u32::from(v);
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    Ok(out)
}

/// 百分号解码（UTF-8 字节序列，兼容 JS encodeURIComponent 输出）。非 % 字符原样保留。
fn percent_decode(input: &str) -> Vec<u8> {
    let bytes = input.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let (Some(hi), Some(lo)) = (hex_val(bytes[i + 1]), hex_val(bytes[i + 2])) {
                out.push((hi << 4) | lo);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    out
}

/// 十六进制字符 → 数值（0-F）。
fn hex_val(b: u8) -> Option<u8> {
    match b {
        b'0'..=b'9' => Some(b - b'0'),
        b'a'..=b'f' => Some(b - b'a' + 10),
        b'A'..=b'F' => Some(b - b'A' + 10),
        _ => None,
    }
}

#[cfg(test)]
mod external_tests {
    use super::*;

    #[test]
    fn validate_url_accepts_allowed_schemes() {
        for url in [
            "https://www.example.edu.cn",
            "http://example.com",
            "mailto:someone@example.edu.cn",
            "tel:+861234567890",
        ] {
            assert!(validate_external_url(url).is_ok(), "应放行 {url}");
        }
    }

    #[test]
    fn validate_url_rejects_unsafe_inputs() {
        let too_long = "https://".repeat(400); // 8*400=3200 字节，超 2048 上限
        for url in [
            "",
            "ftp://example.com",     // 非白名单协议
            "file:///C:/Windows",    // 本地文件访问，禁止
            "javascript:alert(1)",   // XSS 载体
            "cmd://echo-hack",       // 潜在命令注入载体
            too_long.as_str(),       // 超长
        ] {
            assert!(validate_external_url(url).is_err(), "应拒绝 {url}");
        }
    }

    #[test]
    fn validate_url_case_insensitive_scheme() {
        assert!(validate_external_url("HTTPS://Example.COM").is_ok());
    }

    #[test]
    fn open_external_rejects_invalid_without_spawn() {
        // 非法 URL 应直接返回错误，不触发任何系统调用。
        let err = open_external("file:///C:/Windows".to_string()).expect_err("应拒绝非法链接");
        assert!(err.contains("仅支持"), "got {err}");
    }

    #[test]
    fn base64_decode_standard() {
        assert_eq!(decode_base64("aGVsbG8=").unwrap(), b"hello");
    }

    #[test]
    fn base64_decode_without_padding() {
        // 容忍缺失 padding（前端 ECharts getDataURL 输出自带 =，这里兜底）
        assert_eq!(decode_base64("aGVsbG8").unwrap(), b"hello");
    }

    #[test]
    fn base64_decode_rejects_invalid_char() {
        assert!(decode_base64("aGVsbG8!").is_err());
        assert!(decode_base64("???==").is_err());
    }

    #[test]
    fn base64_decode_png_magic_bytes() {
        // 真实 PNG 头：89 50 4E 47 0D 0A 1A 0A
        let png = "iVBORw0KGgo=";
        assert_eq!(
            decode_base64(png).unwrap(),
            vec![0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]
        );
    }

    #[test]
    fn percent_decode_utf8() {
        // JS encodeURIComponent('你好.svg') 的输出，应还原为原始 UTF-8 字节
        let decoded = percent_decode("%E4%BD%A0%E5%A5%BD.svg");
        assert_eq!(decoded, "你好.svg".as_bytes());
    }

    #[test]
    fn percent_decode_keeps_plain_chars() {
        assert_eq!(percent_decode("a b+c.svg"), b"a b+c.svg");
        // 非法 % 序列原样保留，不 panic
        assert_eq!(percent_decode("50%"), b"50%");
    }

    #[test]
    fn data_image_rejects_without_side_effects() {
        // 非白名单图片类型 / 空内容：在落盘与打开之前直接报错
        assert!(open_data_image("data:text/html,hello").is_err());
        assert!(open_data_image("data:image/tiff,AAAA").is_err());
        assert!(open_data_image("data:image/svg+xml,").is_err());
    }
}
