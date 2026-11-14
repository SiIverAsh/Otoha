# Otoha（音葉）

面向 DLsite 音声作品的开源自托管流媒体服务器。  
Open-source, zero-CGO, single-binary self-hosted streaming server for user-owned DLsite voice works.

---

## 核心特性

- **真正单文件静态编译（无 CGO / 无外部依赖）**：基于 Go 标准库 + `modernc.org/sqlite` + `blevesearch/bleve/v2`，**无需安装 Node.js、无需 FFmpeg**，单二进制内嵌 Web 前端与 SQL 迁移脚本，支持一键交叉编译至 `linux/arm64`（树莓派 / 低配 NAS 常驻内存 `< 150MB`）。
- **原生支持 6 位与 8 位作品号**：精准识别 `RJ` / `BJ` / `VJ` 的 6 位与 8 位作品号（如 `RJ123456`、`RJ01234567`）以及 `RG\d{5}` 社团号、`SRI\d{10}` 系列号，彻底解决旧版 kikoeru 8 位号导入失败与静默截断问题。
- **完全离线可用 + 三级容错元数据抓取**：
  - 元数据优先走 `product.json`，动态销量/评分用 `info/ajax` 补齐，异常回落至作品页 HTML 解析；对翻译作品自动回溯原作品号与 `ja_jp` 补齐声优列表。
  - 所有抓取结果落盘缓存，重扫媒体库零重复请求；区分永久 `404` 与网络限流退避重试，支持随时中断与断点续传；同时提供手工编辑元数据与 JSON 备份导入/导出。
  - 支持带用户名密码认证的 `HTTP` / `HTTPS` / `SOCKS5` 代理。
- **保留原始多版本目录树 + 原生无损直出流媒体**：
  - 完整保留 `SE有り` / `SE無し` 等多层子目录结构，纯 Go 解析 `.wav` / `.flac` / `.mp3` / `.m4a` / `.ogg` / `.opus` 时长；标准库 `http.ServeContent` 原生支持 `206 Partial Content` Range 拖动。
  - 自动匹配同目录同主文件名 `.lrc` / `.vtt` / `.srt` / `.ass` 字幕，自动嗅探 `Shift-JIS` / `GBK` / `UTF-8 BOM` 并转码输出同步时间轴。
- **会话聚合收听历史与严格隐私保护**：
  - 30 秒定时心跳 + 暂停/拖动/切轨/切后台即时上报 + `navigator.sendBeacon` 兜底；30 分钟内自动聚合会话，通过 `queue_ctx` 精确还原多版本播放队列，基于实听时长 `listened` 自动推进 `listening -> listened`。
  - 年龄分级一等公民：默认视图可完全隐藏非全年龄（R15/R18）作品，收听历史同步严格过滤不泄漏标题；支持全局关闭历史、临时无痕收听、按时间范围清除，并在清空后强制执行 SQLite `VACUUM` + WAL 截断擦除磁盘残留字节。
- **零门槛只读 WebDAV (`/dav/`)** 与 **kikoeru 无损迁移工具 (`import-kikoeru`)**。

---

## 合规与安全声明（必读）

1. **仅管理自有文件**：Otoha 仅用于索引与流式播放**用户本人合法持有**的本地音频文件。本项目**不提供、不索引、不分发任何作品内容**，亦**不实现** DLsite 账号登录或已购作品批量下载器功能。
2. **公网暴露安全警告**：Otoha 本身不内置内网穿透。若通过公网 IP、反向代理（Caddy / Nginx）、Cloudflare Tunnel 或 frp 对外暴露服务，**请务必开启多用户登录认证（`auth_enabled: true`）并强制全程启用 HTTPS**（或优先使用 Tailscale / WireGuard 组网访问），严防私人媒体库在公网裸奔被搜索引擎或扫描器抓取。

---

## 快速开始

### 1. 编译（零 CGO）

Windows PowerShell：

```powershell
$env:CGO_ENABLED = "0"
go build -o otoha.exe .
```

交叉编译至树莓派 / ARM64 NAS：

```powershell
$env:CGO_ENABLED = "0"; $env:GOOS = "linux"; $env:GOARCH = "arm64"; go build -o otoha-linux-arm64 .
```

### 2. 扫描本地音声目录并启动服务

```powershell
# 离线扫描本地目录（不请求外网）
.\otoha.exe scan --data .\data --root "D:\VOICES" --name Voices

# 扫描并抓取 DLsite 公开元数据（支持带账密的 HTTP / SOCKS5 代理，3 秒礼貌限速，支持 Ctrl+C 断点续传）
.\otoha.exe scan --data .\data --root "D:\VOICES" --scrape --proxy "socks5://user:pass@127.0.0.1:1080"

# 启动流媒体与 Web 服务（默认监听 http://127.0.0.1:8090，只读 WebDAV 挂载于 /dav/）
.\otoha.exe serve --addr 127.0.0.1:8090 --data .\data
```

### 3. 全文索引重建与多用户管理

```powershell
# 从 SQLite 真相源全量重建 Bleve CJK 索引
.\otoha.exe reindex --data .\data

# 创建管理员账号（用于开启认证及 WebDAV Basic Auth）
.\otoha.exe user add --data .\data --username admin --password "your-strong-password" --admin
```

### 4. 从旧版 kikoeru 迁移数据库

源数据库全程以 `mode=ro` + `PRAGMA query_only=ON` 只读打开，绝不修改旧库；自动使用磁盘路径复核并标记被 kikoeru 截断的 8 位 RJ 号冲突，同时探测兼容 `azuse` 分支的 `t_history` 历史表：

```powershell
# 先执行 Dry-Run 输出完整冲突与重抓报告（不写入新库）
.\otoha.exe import-kikoeru --db "C:\kikoeru\sqlite\db.sqlite3" --covers "C:\kikoeru\covers" --root-map "Voices=D:\VOICES" --dry-run --report .\kikoeru-dryrun.json

# 确认无误后正式事务导入
.\otoha.exe import-kikoeru --db "C:\kikoeru\sqlite\db.sqlite3" --covers "C:\kikoeru\covers" --root-map "Voices=D:\VOICES" --data .\data --report .\kikoeru-import.json
```

### 5. 系统服务部署

- **Windows 服务**：`.\otoha.exe service install --addr 127.0.0.1:8090 --data "C:\Otoha\data"`
- **Linux systemd**：参考 [`deploy/otoha.service`](deploy/otoha.service) 或执行 `./otoha service systemd` 生成单元配置。

---

## 开源许可

本项目基于 [GNU General Public License v3.0 (GPL-3.0)](LICENSE) 开源发布。
