package httpserver

import (
	"context"
	"time"
)

// StartExamSweep 启动到期自动结算循环（§14.7）：单进程定时扫描已到期未提交
// 作答并结算；重启后继续扫描数据库，不依赖进程内计时器。
func (s *Server) StartExamSweep(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		sweep := func() {
			n, err := s.exam.SweepExpiredAttempts(ctx)
			if err != nil {
				s.log.Warn("到期结算扫描失败", "error", err)
				return
			}
			if n > 0 {
				s.log.Info("到期作答已自动结算", "count", n)
			}
		}
		// 启动即扫一次，恢复上次中断的到期作答。
		sweep()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}
