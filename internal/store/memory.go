package store

import (
	"errors"
	"sort"
	"sync"

	"vertical-intel-video-agent/internal/domain"
)

var ErrNotFound = errors.New("not found")

type MemoryStore struct {
	mu       sync.RWMutex
	projects map[string]domain.VideoProject
	batches  map[string]domain.BatchJob
}

// NewMemoryStore 创建线程安全的内存存储，供本地 MVP 和测试使用。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		projects: map[string]domain.VideoProject{},
		batches:  map[string]domain.BatchJob{},
	}
}

// SaveProject 保存或覆盖单个视频项目。
func (s *MemoryStore) SaveProject(p domain.VideoProject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects[p.ID] = p
}

// Project 按 ID 返回单个视频项目。
func (s *MemoryStore) Project(id string) (domain.VideoProject, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	return p, ok
}

// Projects 返回按创建时间倒序排列的项目列表。
func (s *MemoryStore) Projects() []domain.VideoProject {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.VideoProject, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// SaveBatch 保存或覆盖一次批量任务。
func (s *MemoryStore) SaveBatch(job domain.BatchJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches[job.ID] = job
}

// Batch 按 ID 返回一次批量任务。
func (s *MemoryStore) Batch(id string) (domain.BatchJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.batches[id]
	return job, ok
}

// Batches 返回按更新时间倒序排列的批量任务列表。
func (s *MemoryStore) Batches() []domain.BatchJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.BatchJob, 0, len(s.batches))
	for _, job := range s.batches {
		out = append(out, job)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}
