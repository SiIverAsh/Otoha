# ASMR.one 全站数据同步

项目现有入口为 `cmd/sync_asmrone.go`，调用 `internal/scrape/sync_asmrone.go` 的
`SyncAsmrOneCatalog`，通过 `internal/asmrone/client.go` 请求公开 API。

## 全站作品目录

```sh
./otoha sync-asmrone --data ./data --page-size 100 --order id --sort asc
```

Windows 将 `./otoha` 换成 `.\otoha.exe`。默认不限制页数，也不筛选有字幕作品，
分页请求 `/api/works`，直到最后一页或空结果。API 客户端默认按
`api.asmr-200.com`、`api.asmr.one`、`api.asmr-100.com`、`api.asmr-300.com` 尝试镜像，
请求间隔默认 0.5 秒；可用 `--interval 1` 调整，或用 `--proxy socks5://127.0.0.1:1080` 指定代理。

作品标题、社团、声优、标签、日期、评分、销量、封面链接、版本等 API 返回且程序映射的字段
按页写入 `data/otoha.db`，同时更新 `data/index.bleve` 搜索索引。
这不是所有 API 端点或原始响应的完整备份，也不下载封面、音频或字幕文件。

## 同时保存曲目树及远程链接

```sh
./otoha sync-asmrone --data ./data --page-size 100 --order id --sort asc --include-tracks
```

此模式额外逐作品请求 `/api/tracks/{id}`，保存曲目路径、大小、时长、远程播放/下载链接、
配对字幕链接和附件信息。它会增加请求量，不会批量下载媒体文件。
当前服务端也存在使用这些远程链接代理流式播放的实现。

## 断点、补齐与核验

- 断点保存在 `data/cache/asmrone/sync_checkpoint.json`，记录 `next_page`、统计和 `finished`。
  用相同数据目录、排序、页大小和字幕筛选参数重新运行，会从记录的下一页继续。
- `--max-pages 10` 可限制本次同步页数；`--subtitle-only` 会缩小同步范围，全站同步不要添加。
- 若先同步目录、后增加 `--include-tracks`，必须加 `--reset-checkpoint` 从头遍历才能补齐之前的作品。
  该参数重置游标，不清空数据库；已有作品会合并更新并计入 duplicates。
- 当前曲目循环被中断后仍可能推进页游标，曲目请求失败也不会阻止该页完成。
  要重试该页，可用 `--start-page N --include-tracks`；需要全量补齐时用
  `--reset-checkpoint --include-tracks`。以完整跑完且失败数为零作为核验目标，
  但曲目 404 不计失败，因此不能据此保证每个作品都有曲目。
- 按页同步不是固定快照。站点删除、补录或排序变化可能影响分页；从头重跑可以合并刷新已有作品，
  但不会自动删除站点已下架的作品。
- 搜索索引写入错误和断点保存错误在当前实现中被忽略。`finished: true` 不能单独证明所有数据完整；
  搜索异常时可执行 `./otoha reindex --data ./data` 从 SQLite 重建索引。

补齐示例：

```sh
./otoha sync-asmrone --data ./data --order id --sort asc --include-tracks --reset-checkpoint
```
