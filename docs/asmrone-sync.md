# ASMR.one 目录同步与播放

Otoha 可从 ASMR.one 导入作品目录，按需同步音轨，并通过自己的服务播放远程音频。
目录同步按作品编号合并记录，将标题、社团、声优、标签、发售日、封面链接等已映射字段写入 SQLite，并尝试更新全文索引。

## 在网页同步和播放

打开 **设置与存储 → 媒体库管理 → ASMR.one 同步**。

- **开始全站同步**或**继续全站同步**按作品 ID 升序同步目录，每页最多 100 部。
- **同步最新 200 部**按收录时间倒序同步最多两页，并覆盖同一数据目录的同步断点。
  之后再执行全站同步时，排序参数与该断点不匹配，会从第一页开始。

同步完成后，可在音声库的**全部音源**下拉菜单选择 **ASMR.one**。
打开作品详情并点击**同步音轨**，即可为这部作品获取音轨树和可配对的字幕链接。
开启 `asmrone_enabled` 时，打开没有任何音轨的作品详情也会尝试自动获取。
关闭该选项会停用自动获取及 DLsite 抓取时的 ASMR.one 元数据补全，手动同步仍可使用。

目录同步保存封面链接，音轨同步保存媒体链接和附件信息。
播放时，Otoha 按需代理远程音频和字幕，音频请求可转发 HTTP Range 以支持拖动播放进度。

## 使用命令行批量同步

服务运行时可在网页启动任务。
使用以下命令操作同一数据目录前，先停止 Otoha 服务，以释放全文索引锁。
Windows PowerShell 中将 `./otoha` 换成 `.\otoha.exe`。

```sh
# 先试同步一页目录
./otoha sync-asmrone --data ./data --max-pages 1

# 以相同筛选和排序继续同步剩余目录
./otoha sync-asmrone --data ./data

# 从第一页重新遍历并补齐音轨和字幕链接
./otoha sync-asmrone --data ./data --reset-checkpoint --include-tracks
```

默认按作品 ID 升序同步，每页 100 部，同步到最后一页或空结果为止。
`--subtitle-only` 只同步源站标记有字幕的作品。
`--include-tracks` 会为每部作品额外查询音轨，保存路径、时长、播放和下载地址等信息。
同步期间只记录这些媒体地址，播放时再向源站请求相应内容。

默认 API 镜像依次为 `api.asmr-200.com`、`api.asmr.one`、`api.asmr-100.com`、`api.asmr-300.com`。
默认请求间隔为 0.5 秒，可用 `--interval 1` 调整，也可用 `--proxy socks5://127.0.0.1:1080` 临时指定代理。
网页任务与播放使用服务端配置的 `scraper_proxy`，可通过 `OTOHA_PROXY` 环境变量覆盖。

## 续跑和核验

- 断点保存在数据目录的 `cache/asmrone/sync_checkpoint.json`，其中 `next_page` 记录下次请求的页码。
  使用相同的排序字段、排序方向、页大小和字幕筛选参数，才会自动沿用该断点。
- `--reset-checkpoint` 会忽略旧断点并从第一页开始，已入库的作品会合并更新。
  先同步目录、之后才加 `--include-tracks` 时，用该参数补齐已经经过的页。
- 一页内的曲目请求失败或中途中断后，页码仍可能推进。
  可用 `--start-page N --max-pages 1 --include-tracks` 重试指定页，该次运行也会写入断点。
- 源站的分页结果会随收录和下架变化。
  从头同步会更新已有作品，已下架的记录仍会留在本地库。
- 核验时查看失败数并检查所需作品的音轨。
  曲目接口返回 404 时不计为失败，索引写入和断点保存错误也不计入统计，因此 `finished: true` 只表示遍历到了当时的末页或空页。
  作品已入库但搜索不到时，可用 `./otoha reindex --data ./data` 从 SQLite 重建索引。

## HTTP API

`POST /api/asmrone/sync` 启动后台同步任务，支持 `page_size`、`start_page`、`max_pages`、`order`、`sort`、`subtitle_only`、`include_tracks` 和 `reset_checkpoint`。
例如，发送以下 JSON 可从当前匹配的断点同步一页，并保存该页的音轨信息。

```json
{
  "page_size": 100,
  "order": "id",
  "sort": "asc",
  "max_pages": 1,
  "include_tracks": true
}
```

响应中的 `started` 标记是否启动了新任务。
用 `GET /api/asmrone/status` 查看任务和断点状态，用 `POST /api/works/{workno}/sync-tracks` 同步单部已入库作品的音轨。
服务启用认证时，这些请求需要通过认证。
