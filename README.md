# Otoha

Otoha（音葉）是面向 DLsite 音声作品的开源自托管媒体库。
在电脑或 NAS 上启动服务，即可用浏览器整理本地音频，也可同步 ASMR.one 目录和导入 ASMR18 播放源。

Open-source, self-hosted streaming server for DLsite audio works.

## 安装与启动

从 [GitHub Releases](https://github.com/SiIverAsh/Otoha/releases) 下载对应系统和架构的压缩包。
Windows、Linux 和 macOS 均有 `amd64` 和 `arm64` 构建，附件中的 `checksums.txt` 提供 SHA-256 校验值。
解压后即可运行，Web 界面内嵌在程序中，运行时无需安装 Node.js 或 FFmpeg。

在 Linux 或 macOS 中运行以下命令。

```sh
./otoha serve --data ./data
```

在 Windows 上使用 PowerShell。

```powershell
.\otoha.exe serve --data .\data
```

在同一台机器的浏览器打开 [http://127.0.0.1:8090](http://127.0.0.1:8090)。
启动时指定的 `--data` 目录存放 SQLite 数据库、全文索引、封面、缓存和配置。
以后启动、导入或维护同一媒体库时，沿用这个目录。

## 添加作品并开始播放

| 音源 | 入口 | 播放方式 |
| --- | --- | --- |
| 本地目录 | **设置与存储 → 媒体库管理 → 存储与扫描 → 添加本地目录**。填写服务端可读的绝对路径，然后点击「扫描本地文件」。 | 直接传输原始音频，保留作品内的版本和子目录。 |
| ASMR.one | **设置与存储 → 媒体库管理 → ASMR.one 同步**。可以同步最新 200 部或按页同步全站目录。 | 通过 Otoha 播放已取得的远程音轨。可按作品加载音轨，也可用命令批量同步音轨。 |
| ASMR18 | **设置与存储 → 媒体库管理 → ASMR18 作品与播放源**。粘贴作品页地址，预览后点「导入作品」，也可按分类采集。 | 通过 Otoha 播放 HLS 流，支持跳转已有章节。 |

本地目录中的作品文件夹需要带有 6 位或 8 位的 `RJ`、`BJ` 或 `VJ` 作品号，例如 `[RJ01234567] 作品名`。
如需封面、发售日或声优等公开信息，可选择「扫描并抓取 DLsite 元数据」。
同一作品有本地音轨时，详情优先显示本地音轨。

音声库支持按关键词搜索，并按音源、分级、社团、声优、标签、收听进度或字幕筛选。
默认只显示标为全年龄的作品。
刚扫描的本地作品如果还未分级，可以将筛选切换为「全年龄 + 未分级」。

播放器支持队列、倍速、睡眠定时和字幕。
本地字幕与音频放在同一目录，文件名只需扩展名不同，支持 `.lrc`、`.vtt`、`.srt` 和 `.ass`。
服务端保存收听历史和续听进度，无痕收听时暂停记录。
「我的收藏」和播放列表保存在当前浏览器，按账号区分。

- [ASMR.one 同步说明](https://github.com/SiIverAsh/Otoha/blob/main/docs/asmrone-sync.md)，包括按页续跑和批量同步音轨。
- [ASMR18 导入与播放说明](https://github.com/SiIverAsh/Otoha/blob/main/docs/asmr18-sync.md)，包括批量采集、补齐发售日和按章节分轨。
- [Otoha Mobile](https://github.com/SiIverAsh/Otoha_mobile) 是独立的 Android 应用，可直接连接 ASMR.one 和 ASMR18。要访问自己的 Otoha 服务器，在手机浏览器中打开部署地址。

请仅对有权访问的作品使用本地和远程播放功能，并遵守音源站点的使用条款。

## 部署与维护

Otoha 默认仅通过 HTTP 监听 `127.0.0.1:8090`，登录认证默认关闭。
如需开放远程访问，先在「媒体库管理 → 用户账号」创建管理员，再在「偏好设置 → 访问与认证」开启多用户登录认证。
通过 HTTPS 反向代理对外提供服务，并保留 Otoha 的本机监听地址。

| 配置 | 用途 |
| --- | --- |
| `serve --addr` 或 `OTOHA_ADDR` | 设置服务的监听地址。 |
| `serve --auth` 或 `OTOHA_AUTH=true` | 启动时开启认证。先创建可登录的账号。 |
| `scraper_proxy` 或 `OTOHA_PROXY` | 设置服务端抓取代理。网页入口为「偏好设置 → 抓取与扫描」。 |
| `webdav_enabled` | 控制默认开启的只读 `/dav/`，用于访问已配置的本地存储目录。启用登录认证后，WebDAV 客户端可用 HTTP Basic Auth 登录。 |

配置文件为 `<数据目录>/config.json`。
启动时环境变量覆盖文件中的对应配置，显式的命令行选项优先。
Linux 可使用发行包中的 `deploy/otoha.service`，并为服务账号配置数据目录写权限和媒体目录读权限。

服务运行时可在网页中扫描、同步或重建全文索引。
改用命令行执行这些操作前，先停止使用同一数据目录的服务，以释放 Bleve 索引锁。
以下命令在 Windows PowerShell 中将 `./otoha` 改为 `.\otoha.exe`，并调整媒体路径。

```sh
./otoha scan --data ./data --root "/path/to/voices" --name Voices
./otoha reindex --data ./data
```

需要扫描时联网抓取缺失的元数据，可加 `--scrape`。
网页的「备份与索引」支持导出和导入作品元数据 JSON。
完整迁移服务器时，先停服再备份整个数据目录，并保留媒体文件及本地存储路径。

### 从 kikoeru 迁移

源 SQLite 数据库以只读方式打开。
先生成迁移报告，检查作品号冲突和待补抓的条目。
`--root-map` 将旧库中的存储别名映射到 Otoha 所在机器的实际路径。

```sh
./otoha import-kikoeru --db ./kikoeru.sqlite3 --data ./data \
  --root-map "Voices=/path/to/voices" --dry-run --report ./kikoeru-dryrun.json
```

确认报告后，移除 `--dry-run` 并更换报告文件名，即可正式导入。
如有旧封面目录，可同时指定 `--covers /path/to/covers`。

## 源码构建与测试

使用 [go.mod](https://github.com/SiIverAsh/Otoha/blob/main/go.mod) 中声明的 Go 版本。
从源码目录执行 `CGO_ENABLED=0 go build -o otoha .`。
Windows PowerShell 先执行 `$env:CGO_ENABLED = "0"`，再执行 `go build -o otoha.exe .`。

页面由 Go 和 Templ 渲染，HTMX 用于局部更新。
修改 `web/views` 下的 `.templ` 文件后，运行 `go generate ./web/views` 提交对应的生成文件。
浏览器资源来自 `web/src`，JSON 和媒体接口位于 `/api/`。

```sh
go vet ./...
go test ./...
node --test web/test/*.test.mjs
```

浏览器模块测试使用 Node.js 24。
发布工作流在 Linux、Windows 和 macOS 上运行检查。
在 **Actions → Release → Run workflow** 中选择 `main` 和版本增量，通过检查和打包后会自动发布正式 Release。
工作流从最高的 `vX.Y.Z` 稳定版本递增，首次发布从 `0.0.0` 起算。
GitHub 会在发布时自动为本次构建的提交创建对应标签。

| 选项 | 从 `1.2.3` 递增后的版本 |
| --- | --- |
| `major`（大版本） | `2.0.0` |
| `minor`（小版本） | `1.3.0` |
| `patch`（补丁，默认） | `1.2.4` |

[发布工作流](https://github.com/SiIverAsh/Otoha/actions/workflows/release.yml)

## 许可证

[GNU General Public License v3.0](LICENSE)。
