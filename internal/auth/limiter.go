package auth

import (
	"sync"
	"time"
)

// loginLimiter 是登录失败限速器：同一登录名连续失败达到阈值后临时锁定。
// 内存实现，单实例语义；多副本部署时应改为共享存储（§16.2 如实说明）。
type loginLimiter struct {
	mu           sync.Mutex
	state        map[string]*attemptState
	maxFails     int
	lockDuration time.Duration
	calls        int
}

type attemptState struct {
	fails       int
	lockedUntil time.Time
	lastFail    time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		state:        map[string]*attemptState{},
		maxFails:     5,
		lockDuration: 5 * time.Minute,
	}
}

// lockedRemaining 返回该登录名是否处于锁定状态及剩余时长。
func (l *loginLimiter) lockedRemaining(loginName string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.state[loginName]
	if !ok {
		return false, 0
	}
	// 仅在曾设置过锁定时间时判断过期并清零计数；
	// lockedUntil 为零值表示从未锁定，不能重置失败计数。
	if !st.lockedUntil.IsZero() {
		if time.Now().Before(st.lockedUntil) {
			return true, time.Until(st.lockedUntil)
		}
		st.lockedUntil = time.Time{}
		st.fails = 0
	}
	return false, 0
}

// fail 记录一次失败；达到阈值时启动锁定。
func (l *loginLimiter) fail(loginName string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.state[loginName]
	if !ok {
		st = &attemptState{}
		l.state[loginName] = st
	}
	if time.Now().Before(st.lockedUntil) {
		return // 锁定期间的失败不延长锁定
	}
	st.lastFail = time.Now()
	st.fails++
	if st.fails >= l.maxFails {
		st.lockedUntil = time.Now().Add(l.lockDuration)
		st.fails = 0
	}
	l.calls++
	if l.calls%512 == 0 {
		l.prune(24 * time.Hour)
	}
}

// reset 登录成功后清零计数。
func (l *loginLimiter) reset(loginName string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.state, loginName)
}

// prune 清理长时间无活动（既未失败也未锁定）的记录，防止 map 无限增长。
func (l *loginLimiter) prune(olderThan time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, st := range l.state {
		expired := st.lockedUntil.IsZero() || time.Now().After(st.lockedUntil)
		idle := time.Since(st.lastFail) > olderThan
		if expired && idle {
			delete(l.state, k)
		}
	}
}
