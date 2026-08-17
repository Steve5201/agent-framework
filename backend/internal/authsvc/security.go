package authsvc

import (
	"sync"
	"time"
)

// loginThrottle 登录失败计数限流（P2-26）：
// 按用户名统计连续失败次数，达到阈值后锁定一段时间。
//
// 注意：这是单实例内存实现，适合当前单机部署；
// 若未来多副本部署，需替换为 Redis 等共享存储（在此处保留接口边界即可）。
type loginThrottle struct {
	mu         sync.Mutex
	entries    map[string]*throttleEntry
	maxFail    int
	lockWindow time.Duration
}

type throttleEntry struct {
	failures    int
	lockedUntil time.Time
}

func newLoginThrottle(maxFail int, lockWindow time.Duration) *loginThrottle {
	return &loginThrottle{
		entries:    make(map[string]*throttleEntry),
		maxFail:    maxFail,
		lockWindow: lockWindow,
	}
}

// allow 检查 key 当前是否放行。返回 (是否放行, 剩余锁定时间)。
// 注意：未锁定时不清除计数条目，保证失败计数能持续累加到阈值；
// 仅当"锁定期已过"时才删除条目（计数归零）。
func (t *loginThrottle) allow(key string, now time.Time) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	e := t.entries[key]
	if e == nil {
		return true, 0
	}
	if now.Before(e.lockedUntil) {
		return false, e.lockedUntil.Sub(now)
	}
	if !e.lockedUntil.IsZero() {
		// 锁定期已过：清零计数重新开始
		delete(t.entries, key)
	}
	return true, 0
}

// recordFailure 记录一次失败；达到阈值时进入锁定。
func (t *loginThrottle) recordFailure(key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()

	e := t.entries[key]
	if e == nil {
		e = &throttleEntry{}
		t.entries[key] = e
	}
	e.failures++
	if e.failures >= t.maxFail {
		e.lockedUntil = now.Add(t.lockWindow)
	}
}

// reset 登录成功时清零失败计数。
func (t *loginThrottle) reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}
