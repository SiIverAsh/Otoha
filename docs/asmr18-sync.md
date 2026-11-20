# ASMR18 采集与播放

ASMR18（`asmr18.fans`）通过公开作品页提供元数据和 HLS 播放清单。Otoha 解析 HTML 与打包脚本中的字符串，不执行远端 JavaScript，也不下载完整音频。

## 网页入口

重新构建并启动 Otoha 后，展开 **设置与存储 → 媒体库管理 → ASMR18 作品与播放源**，填写作品页地址，每行一个。
可设置同时采集的作品数和所有请求共用的间隔，默认 4 个任务、0.5 秒全局间隔。
可先点击「预览解析」再执行导入，也可通过「按分类采集」导入最新一页（最多 24 部）。
进度与错误显示在任务状态栏，支持取消。

访问页面和播放 CDN 都使用 Otoha 的 `scraper_proxy` 设置或 `OTOHA_PROXY` 环境变量。
在 **设置与存储 → 偏好设置 → 抓取与扫描** 中填写并保存抓取代理。

## 命令行

服务器停止时，可以用命令行导入同一个数据目录。服务器运行时，请使用网页入口，避免争用 Bleve 索引。

```sh
# 只解析作品页，不写入作品库（需要验证播放清单时加 --probe-duration）
go run . sync-asmr18 --data ./data/asmr18-trial \
  --proxy http://127.0.0.1:7897 --dry-run \
  https://asmr18.fans/allages/rj01752865/

# 小范围试导入一个作品
go run . sync-asmr18 --data ./data/asmr18-trial \
  --proxy http://127.0.0.1:7897 \
  https://asmr18.fans/allages/rj01752865/

# 启动该独立试用库
OTOHA_PROXY=http://127.0.0.1:7897 go run . serve \
  --data ./data/asmr18-trial --addr 127.0.0.1:8091
```

`--proxy` 仅覆盖本次命令，不持久化代理配置。批量采集支持 `--category allages|boys|girls`、`--start-page`、`--max-pages`、`--max-works`、`--workers` 和 `--interval`。默认仅采集第一页、最多请求 24 部、4 个并发作品任务、所有请求共用 0.5 秒间隔（约每秒最多 2 次请求）。增加 worker 不会分别获得一份请求额度。

默认跳过已完整导入的 ASMR18 来源；`--max-works` 限制实际提交抓取的去重作品数，已完成来源的跳过不占这个额度。两个上限设为 0 时才不限量。尚未保存页码断点，但重新遍历会跳过已完成来源，失败作品可再次尝试。

- `--refresh`：重新采集已有来源，更新播放地址、章节和音轨名称；已有作品元数据、音轨 ID、已知时长和收听进度继续保留。
- `--probe-duration`：额外读取 HLS 清单获取时长、验证播放源。默认只请求作品页；时长在首次播放后由浏览器补齐。已有来源时长缺失时，此选项会重新采集以尝试补齐。
- `--dry-run`：始终解析指定作品，包括已导入来源；没有 `--probe-duration` 时只核对页面解析，不验证 CDN 可达性。

网页里“导入填写的作品”会刷新明确指定的 URL；分类批量采集默认跳过已有来源，可勾选重新采集已有来源。遇到 HTTP 429、403、503 或验证码时停止本次采集，不继续排队请求；已完成的写入保留。

HTTP 入口为管理员接口 `POST /api/asmr18/sync`：

```json
{
  "urls": ["https://asmr18.fans/allages/rj01752865/"],
  "dry_run": false,
  "workers": 4,
  "interval_sec": 0.5,
  "refresh": true,
  "probe_duration": false
}
```

## 补全已入库作品的元数据

`enrich-metadata` 从 DLsite 及配置的 ASMR.one 元数据接口补充已有 ASMR18 作品的可得字段，包括发售日、社团编号、系列、价格、文件大小、评分与销量、制作人员、标签、封面、版本和翻译信息。仅填补缺失内容，保留已有作品信息和用户数据；不读取音轨接口、HLS 清单或音频，不更新播放地址与音轨。

先停止使用同一数据目录的 Otoha 服务，以释放搜索索引锁。
如果索引持续被占用，命令会在约一秒后提示停止服务并退出。

```sh
go build -o otoha .
./otoha enrich-metadata --data ./data/asmr18-trial \
  --all --workers 128 --rps 100
```

`--all` 检查全部已导入的 ASMR18 作品，包括已有发售日但缺少其他字段的作品。不带该选项时，只选择核心信息缺失的作品。`--rps 100` 是所有 worker 和元数据来源合计每秒最多 100 次 HTTP 请求，重试、重定向和封面下载也占用同一额度；实际速度还受响应时间影响，不代表每秒完成 100 部作品。并发可设为 1–256，默认 4；默认请求上限为每秒 2 次。

命令默认先保存一致的 SQLite 备份，并把逐项结果写入 `data/cache/metadata/` 下带时间戳的 JSONL 报告。每部作品独立保存，Ctrl+C 后已完成的更新保留。来源不存在或没有提供的字段不会编造。处理完毕后可用原命令重新启动服务。

可在末尾指定 RJ 编号，或用 `--max-works 10` 限制处理数量；需要代理时添加 `--proxy http://127.0.0.1:7897`，仅作用于本次命令。

## 把整段音频列为独立曲目

只有一个 ASMR18 播放源的作品，如果已有可用的章节起点，导入时会按章节列为独立曲目。
对于已入库的作品，用 `split-asmr18` 补处理。
缺少章节起点时，命令按 RJ 编号读取 DLsite 上的公开曲目表。
有逐轨时长且总长与 ASMR18 播放源相符时，才累计时长生成分轨起点。
必要时会读取 HLS 清单核对总长。
时长缺失、曲目表有歧义或版本时长不符的作品保留原播放源。

```sh
# 将已保存的 ASMR18 章节展开为曲目，无须联网
go run . split-asmr18 --data ./data/asmr18-trial --chapters-only

# 核对指定作品的曲目和起止时间，暂不保存
go run . split-asmr18 --data ./data/asmr18-trial --dry-run RJ01211108

# 将核对通过的分轨写入曲目列表
go run . split-asmr18 --data ./data/asmr18-trial RJ01211108
```

可以一次传入多个 RJ 编号，也可以用 `--max-works` 限制批量检查的作品数。
DLsite 请求使用抓取间隔和代理设置，已有分轨的作品会跳过。
每条曲目对应原播放源的一个时间段，进度和续播以该曲目为单位。
音轨目录根据实际播放源标注 HLS。
章节标题中原有的 MP3 或 WAV 字样会保留，发售文件的格式可参考 DLsite 发售页的说明。

## 字段与去重规则

- `workno` 是规范化为大写的 RJ 编号，也是作品表主键。同一 RJ 跨分类、跨来源重复采集均复用已有作品。
- 新作品读取标题、社团名、声优、编剧、画师、音乐、系列名称、标签、封面。已有作品保留现有元数据和用户信息，仅新增或更新 ASMR18 播放源。
- 上传时间不填作发行日期，站点社团 slug 不填作 DLsite RG 编号。没有提供的销量、价格、评分、字幕等字段不推算。
- 全年龄分类映射全年龄；`boys`、`girls` 分类按 R18 隔离。新作品遵循既有默认分级过滤。
- 每个播放器保存一条 HLS 播放源。可验证的章节起点和 DLsite 曲目时长用于生成独立曲目。
- ASMR18 使用独立 storage；使用 `--refresh` 重采集时，以稳定文件槽位更新播放 URL，保留音轨 ID 和收听进度，不删除其他来源的音轨。本地音轨仍按既有规则优先显示。
- 播放地址与来源页分别保存。默认由浏览器加载播放源后报告时长；启用 `--probe-duration` 时，采集阶段也会尝试累加清单的 `EXTINF`。关闭探测不会清空已有时长。

## 播放实现

HLS 清单及分片经过 Otoha `/api/stream/{trackId}` 代理，沿用用户认证、配置代理和分片 Range 请求。清单里的子清单、密钥和分片地址改写为 Otoha 地址，并限制在该作品的 CDN 目录内，避免开放任意 URL 代理。

Safari 等支持原生 HLS 的浏览器直接播放；其他支持 MediaSource 的浏览器使用随应用自托管的 hls.js 1.6.13（Apache-2.0，许可证见 `web/src/vendor/hls-LICENSE`）。无需访问外部脚本 CDN。

站点结构、CDN 域名、不同文件版本的组织方式可能变化；页面中没有受支持的播放地址时会报告错误，不生成空音轨。默认模式只提取页面链接，CDN 的实际可达性在播放或启用时长探测时确认。实际试用应先用少量作品检查后再扩大采集。

## 全站运行示例

```sh
for category in allages boys girls; do
  go run . sync-asmr18 --data ./data/asmr18-trial \
    --proxy http://127.0.0.1:7897 --category "$category" \
    --max-pages 0 --max-works 0 --workers 4 --interval 0.5 || break
done
```

需要全面刷新已导入来源时加 `--refresh`。发生上游限制或错误时，示例会停止后续分类；重新运行会快速跳过此前成功导入的来源。
