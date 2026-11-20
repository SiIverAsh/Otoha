# ASMR18 导入与播放

Otoha 可以从 ASMR18（`asmr18.fans`）作品页提取元数据、HLS 播放地址和章节，并按 RJ 编号合并到现有作品库。
导入只保存目录信息和播放地址，播放时由 Otoha 按需向上游请求 HLS 清单和分片。

## 在网页中导入

1. 打开「设置与存储 → 媒体库管理 → ASMR18 作品与播放源」，填入作品页地址，每行一个。
2. 点击「预览解析」核对识别到的作品和播放源，再点击「导入作品」写入作品库。
3. 如需按分类导入，展开「按分类采集」，选择分类并点击「采集最新一页（最多 24 部）」。
4. 任务运行中或出错时，点击顶部的「查看后台任务」查看状态。也可以在任务运行中取消。

指定地址导入会刷新已有的 ASMR18 来源。
按分类采集会跳过已完成的来源，勾选「更新已导入的播放地址」可以重新抓取。
在「采集参数」里可以调整同时处理的作品数、全局请求间隔，或勾选「读取时长（增加请求数）」。

采集和播放使用服务端抓取代理。
可在「设置与存储 → 偏好设置 → 抓取与扫描」保存代理，或在启动服务时设置 `OTOHA_PROXY`。
导入后，在「发现作品」将「全部音源」切换为「ASMR18」。
「男性向」和「女性向」分类的新作品标记为 R18，使用默认全年龄视图时，可切换到「仅 R18」查看。

## 使用命令行导入

以下示例使用 [README](../README.md) 中安装或构建的程序，Windows 上将 `./otoha` 换为 `.\otoha.exe`。
同一数据目录正在运行服务时，在网页中导入即可。
命令行写入时先停止服务，释放 Bleve 搜索索引锁。

```sh
# 预览指定作品
./otoha sync-asmr18 --data ./data/asmr18-trial --dry-run https://asmr18.fans/allages/rj01752865/

# 导入同一个作品
./otoha sync-asmr18 --data ./data/asmr18-trial https://asmr18.fans/allages/rj01752865/

# 从全年龄分类第二页开始，最多读取两页、处理 30 部新抓取的作品
./otoha sync-asmr18 --data ./data/asmr18-trial --category allages --start-page 2 --max-pages 2 --max-works 30

# 启动独立试用库
./otoha serve --data ./data/asmr18-trial --addr 127.0.0.1:8091
```

需要为单次采集指定代理时，使用 `--proxy http://127.0.0.1:7897`。
该参数仅覆盖本次命令的代理，后续播放使用服务启动时的代理配置。

| 参数 | 作用 |
| --- | --- |
| `--category` | 未指定作品地址时，选择 `allages`（全年龄）、`boys`（男性向）或 `girls`（女性向）。默认 `allages`。 |
| `--start-page`、`--max-pages` | 默认从第 1 页开始，读取 1 页。将 `--max-pages` 设为 0 可遍历到分类末尾。 |
| `--max-works` | 默认最多提交 24 部作品抓取，跳过的作品和重复 RJ 不占额度。设为 0 可取消作品数限制。 |
| `--workers`、`--interval` | 默认同时处理 4 部作品，采集请求共享 0.5 秒的发起间隔。并发数可设为 1–16。 |
| `--dry-run` | 重新解析指定作品并输出结果，适合核查已导入作品，作品库不写入。 |
| `--refresh` | 重抓已导入的作品，更新 ASMR18 播放地址、音轨名称和章节。 |
| `--probe-duration` | 额外请求 HLS 清单校验格式，并从完整的媒体清单读取时长。也会重试缺少时长的 ASMR18 来源。 |

采集默认只从作品页提取播放地址，播放或启用清单探测时才能检验播放 CDN 是否可达。
未预读到的时长也可以由浏览器在播放时上报。
分类采集没有页码断点，恢复时可指定 `--start-page` 或重新遍历，已经完成的来源默认会被跳过。
遇到上游 HTTP 403、429、503 或校验页面时，本次采集会停止，已完成的导入会保留。

如需逐个遍历三个分类，可在 POSIX shell 中运行以下命令。
某个分类返回错误时，循环会停止。

```sh
for category in allages boys girls; do
  ./otoha sync-asmr18 --data ./data/asmr18-trial --category "$category" \
    --max-pages 0 --max-works 0 || break
done
```

## 合并与播放规则

- 同一 RJ 编号跨分类和音源共用一个作品条目。首次导入读取页面提供的标题、制作人员、社团名称、系列、标签和封面。页面上传时间和社团 slug 不会填入 DLsite 发售日与社团编号。
- 合并时保留现有作品元数据和其他来源的音轨。刷新时，仍能匹配原文件槽位的音轨保留 ID、已知时长和收听进度。
- 同时关联 ASMR.one 和 ASMR18 的作品可以在两个音源筛选中找到。同一作品有本地音轨时，详情优先显示本地音轨。
- 播放清单与分片经 `/api/stream/{trackId}` 代理。子清单、密钥和分片须位于该音源的同源目录内，分片支持转发 Range 请求。
- 浏览器可使用原生 HLS，或在支持 MediaSource 的浏览器中使用程序内嵌的 hls.js。上游页面结构或播放地址可能变化，作品页解析成功后仍需确认实际播放。

## 补全 DLsite 元数据

`enrich-metadata` 补充已入库作品缺失的发售日、社团编号、系列、价格、文件大小、评分与销量、制作人员、标签、封面、版本和翻译信息。
通常从 DLsite 获取，并按配置使用 ASMR.one 补齐可得字段。
已有字段和用户数据会保留，播放地址和音轨不会被更新。

```sh
# 用默认并发和限速补全最多 10 部核心信息缺失的作品
./otoha enrich-metadata --data ./data/asmr18-trial --max-works 10

# 检查全部已入库 ASMR18 作品，包括已有发售日的作品
./otoha enrich-metadata --data ./data/asmr18-trial --all

# 仅补全缺失的发售日，可在服务运行期间执行
./otoha enrich-metadata --data ./data/asmr18-trial --date-only
```

常规补全和 `--all` 会更新搜索索引，运行前应停止同一数据目录的服务。
`--date-only` 只更新 SQLite 中缺失的发售日，无须打开搜索索引，且不能和 `--all` 同用。
可在命令末尾指定 RJ 编号。
`--workers` 默认 4，可设为 1–256。
`--rps` 默认 2，是所有并发任务、元数据来源及封面请求共享的每秒请求上限。
任务默认先备份 SQLite，再将逐项结果写入数据目录下 `cache/metadata/` 中带时间戳的 JSONL 报告。
可用 `--report` 指定新的报告路径，或用 `--backup=false` 跳过本次备份。
每部作品独立保存，中断后已完成的更新仍可使用。

## 按章节拆分曲目

只有一条 ASMR18 播放源且包含多个可用章节的作品，导入时可按章节列为独立曲目。
对已经入库的单播放源作品，可运行 `split-asmr18` 补处理。

```sh
# 仅使用已保存的 ASMR18 章节，无须联网
./otoha split-asmr18 --data ./data/asmr18-trial --chapters-only

# 核对指定作品的曲目与起止时间
./otoha split-asmr18 --data ./data/asmr18-trial --dry-run RJ01211108

# 保存核对通过的分轨
./otoha split-asmr18 --data ./data/asmr18-trial RJ01211108
```

缺少可用章节时，程序尝试读取 DLsite 公开曲目表，必要时读取 HLS 清单核对总时长。
只有曲目表无歧义、各曲目有时长，且总长与播放源相符时，才会保存分轨。
可一次传入多个 RJ 编号，或用 `--max-works` 限制检查数量，已有分轨的作品会跳过。
每条分轨对应原播放源的一个时间段，进度和续播以该曲目为单位。
目录显示的格式是 HLS，章节标题中原有的 MP3 或 WAV 字样会保留。

## HTTP 接口

管理员可以调用 `POST /api/asmr18/sync` 提交后台任务，例如预览一个指定作品。
接口接受任务时返回 HTTP 202，可通过 `GET /api/tasks` 查看状态。

```json
{
  "urls": ["https://asmr18.fans/allages/rj01752865/"],
  "dry_run": true,
  "workers": 4,
  "interval_sec": 0.5,
  "probe_duration": false
}
```

将 `dry_run` 设为 `false` 即可导入，添加 `"refresh": true` 可更新已有播放源。
