// 本文件实现配置的版本化、发布与回滚。
//
// 这是控制面的最小实现：它只负责"哪些配置生效"，不参与请求处理，
// 也不直接操作数据面内存——数据面通过 Watch 订阅快照，自己完成原子替换。
package admin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gag/internal/config"
)

// ErrVersionNotFound 表示指定的历史版本不存在。
var ErrVersionNotFound = errors.New("admin: version not found")

// Version 是一次配置发布记录。
type Version struct {
	ID        int64
	Version   string
	Hash      string
	Snapshot  *config.Snapshot
	CreatedAt time.Time
	Author    string
	Comment   string
}

// Store 是内存版配置仓库。
//
// 生产环境应替换为带持久化的实现（etcd/MySQL），接口保持不变。
type Store struct {
	mu       sync.RWMutex
	current  *Version
	history  []*Version
	nextID   int64
	watchers map[int64]chan *config.Snapshot
	nextSub  int64
	maxKeep  int
}

// NewStore 创建配置仓库。
func NewStore() *Store {
	return &Store{
		watchers: make(map[int64]chan *config.Snapshot, 4),
		maxKeep:  200,
	}
}

// Publish 校验并发布一份新配置。
func (s *Store) Publish(cfg *config.Config, author, comment string) (*Version, error) {
	snap, err := config.NewSnapshot(cfg)
	if err != nil {
		return nil, err
	}
	return s.PublishSnapshot(snap, author, comment)
}

// PublishSnapshot 发布已校验的快照。
func (s *Store) PublishSnapshot(snap *config.Snapshot, author, comment string) (*Version, error) {
	if snap == nil {
		return nil, errors.New("admin: snapshot 为 nil")
	}
	s.mu.Lock()
	s.nextID++
	v := &Version{
		ID:        s.nextID,
		Version:   snap.Version(),
		Hash:      snap.Hash(),
		Snapshot:  snap,
		CreatedAt: time.Now().UTC(),
		Author:    author,
		Comment:   comment,
	}
	s.current = v
	s.history = append(s.history, v)
	if len(s.history) > s.maxKeep {
		s.history = s.history[len(s.history)-s.maxKeep:]
	}
	watchers := make([]chan *config.Snapshot, 0, len(s.watchers))
	for _, ch := range s.watchers {
		watchers = append(watchers, ch)
	}
	s.mu.Unlock()

	for _, ch := range watchers {
		pushLatest(ch, snap)
	}
	return v, nil
}

// pushLatest 非阻塞投递，队列满时丢弃旧值只保留最新配置。
func pushLatest(ch chan *config.Snapshot, snap *config.Snapshot) {
	select {
	case ch <- snap:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- snap:
	default:
	}
}

// Current 返回当前生效版本。
func (s *Store) Current() *Version {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// History 返回历史版本（按发布时间升序）。
func (s *Store) History() []*Version {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Version, len(s.history))
	copy(out, s.history)
	return out
}

// Rollback 把配置回滚到指定版本，并生成一条新的发布记录（保留审计链路）。
func (s *Store) Rollback(id int64, author string) (*Version, error) {
	s.mu.RLock()
	var target *Version
	for _, v := range s.history {
		if v.ID == id {
			target = v
			break
		}
	}
	s.mu.RUnlock()
	if target == nil {
		return nil, fmt.Errorf("%w: #%d", ErrVersionNotFound, id)
	}
	snap, err := config.NewSnapshot(target.Snapshot.Config())
	if err != nil {
		return nil, err
	}
	return s.PublishSnapshot(snap, author, fmt.Sprintf("rollback to #%d (%s)", target.ID, target.Version))
}

// Watch 订阅配置变更；返回的 channel 会先收到当前快照。
func (s *Store) Watch(ctx context.Context) <-chan *config.Snapshot {
	ch := make(chan *config.Snapshot, 1)
	s.mu.Lock()
	s.nextSub++
	id := s.nextSub
	s.watchers[id] = ch
	current := s.current
	s.mu.Unlock()

	if current != nil {
		ch <- current.Snapshot
	}

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.watchers, id)
		s.mu.Unlock()
		// 故意不 close(ch)：发布方可能在无锁状态下投递，
		// 关闭会与发送竞争并触发 panic。消费方通过 ctx 感知结束。
	}()
	return ch
}
