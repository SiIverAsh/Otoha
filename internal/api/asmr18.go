package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/SiIverAsh/otoha/internal/scrape"
)

func (tm *TaskManager) TriggerASMR18Sync(opts scrape.ASMR18Options) (bool, error) {
	if err := opts.Validate(); err != nil {
		return false, err
	}
	tm.mu.Lock()
	if tm.state.Running {
		tm.mu.Unlock()
		return false, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	tm.cancelFn = cancel
	tm.state = TaskSnapshot{Running: true, Phase: "syncing_asmr18", Message: "正在读取 ASMR18 作品和播放源…", StartedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	tm.mu.Unlock()
	go func() {
		defer cancel()
		p, err := scrape.SyncASMR18(ctx, tm.store, tm.search, tm.cfg, opts, func(p scrape.ASMR18Progress) {
			tm.publish(func(s *TaskSnapshot) {
				s.CurrentWorkno = p.Workno
				s.ASMR18Preview = p.Preview
				s.WorksScanned = p.Processed
				s.TracksScanned = p.Tracks
				s.Message = fmt.Sprintf("ASMR18：已处理 %d · 新增 %d · 合并 %d · 跳过 %d · 音轨 %d · 失败 %d · %.2f 部/s", p.Processed, p.Inserted, p.Merged, p.Skipped, p.Tracks, p.Failures, p.WorksPerSec)
				if p.Message != "" {
					s.Message += " · " + p.Message
				}
			})
		})
		tm.mu.Lock()
		tm.cancelFn = nil
		tm.mu.Unlock()
		tm.publish(func(s *TaskSnapshot) {
			s.Running = false
			s.Phase = "done"
			s.Message = fmt.Sprintf("ASMR18 完成：新增 %d · 合并 %d · 跳过 %d · 音轨 %d · 失败 %d", p.Inserted, p.Merged, p.Skipped, p.Tracks, p.Failures)
			if opts.DryRun {
				s.Message = "ASMR18 预览完成（未写入）：已解析 " + fmt.Sprint(p.Processed) + " 部作品，" + fmt.Sprint(p.Tracks) + " 条音轨"
			}
			if errors.Is(err, context.Canceled) {
				s.Phase = "cancelled"
				s.Message = "ASMR18 采集已取消，已完成的作品已保存"
			} else if err != nil {
				s.Phase = "error"
				s.Message += " · " + err.Error()
			}
		})
	}()
	return true, nil
}
