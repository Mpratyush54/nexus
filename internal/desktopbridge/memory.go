package desktopbridge

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

type Memory struct {
	mu      sync.Mutex
	devices map[string]Device
	jobs    map[string]Job
}

func NewMemory() *Memory { return &Memory{devices: map[string]Device{}, jobs: map[string]Job{}} }
func clone[T any](value T) T {
	b, _ := json.Marshal(value)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}
func copyJob(j Job) Job { owner := j.Owner; j = clone(j); j.Owner = owner; return j }
func copyDevice(d Device) Device {
	owner, key := d.Owner, d.KeyHash
	d = clone(d)
	d.Owner, d.KeyHash = owner, key
	return d
}
func key(owner, id string) string { return owner + "\x00" + id }
func (m *Memory) Register(_ context.Context, d Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, j := range m.jobs {
		if j.Owner == d.Owner && j.Expires.Before(d.Seen.Add(-7*24*time.Hour)) {
			delete(m.jobs, k)
		}
	}
	k := key(d.Owner, d.ID)
	if old, ok := m.devices[k]; ok && old.KeyHash != d.KeyHash {
		return ErrConflict
	}
	m.devices[k] = copyDevice(d)
	return nil
}
func (m *Memory) Devices(_ context.Context, owner string) ([]Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Device{}
	for _, d := range m.devices {
		if d.Owner == owner {
			out = append(out, copyDevice(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (m *Memory) Device(_ context.Context, owner, id string) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[key(owner, id)]
	if !ok {
		return d, ErrNotFound
	}
	return copyDevice(d), nil
}
func (m *Memory) Create(_ context.Context, j Job) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(j.Owner, j.ID)
	if old, ok := m.jobs[k]; ok {
		if !SameRequest(old.Request, j.Request) {
			return Job{}, ErrConflict
		}
		return copyJob(old), nil
	}
	count := 0
	for _, old := range m.jobs {
		if old.Owner == j.Owner && old.DeviceID == j.DeviceID && !Terminal(old.State) && old.Expires.After(j.Created) {
			count++
		}
	}
	if count >= 1 {
		return Job{}, ErrConflict
	}
	m.jobs[k] = copyJob(j)
	return copyJob(j), nil
}
func (m *Memory) Claim(_ context.Context, owner, device string, now time.Time) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var next *Job
	for k, j := range m.jobs {
		if j.Owner != owner || j.DeviceID != device {
			continue
		}
		if !Terminal(j.State) && !j.Expires.After(now) {
			j.State = "expired"
			j.Revision++
			j.Updated = now
			m.jobs[k] = j
		}
		if j.State == "queued" && (next == nil || j.Created.Before(next.Created)) {
			c := j
			next = &c
		}
	}
	if next == nil {
		return nil, nil
	}
	next.State = "awaiting_approval"
	next.Status = "Awaiting approval in Nexus Desktop"
	next.Revision++
	next.Updated = now
	// The claim hands the image bytes to the worker once and drops them.
	c := copyJob(*next)
	stored := copyJob(*next)
	stored.Attachments = WithoutAttachmentData(stored.Attachments)
	m.jobs[key(owner, next.ID)] = stored
	return &c, nil
}
func expire(j Job, now time.Time) Job {
	if !Terminal(j.State) && !j.Expires.After(now) {
		j.State = "expired"
		j.Status = "Desktop request expired; it was not retried"
		j.Revision++
		j.Updated = now
	}
	return j
}
func (m *Memory) Get(_ context.Context, owner, id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(owner, id)
	j, ok := m.jobs[k]
	if !ok {
		return j, ErrNotFound
	}
	j = expire(j, time.Now().UTC())
	m.jobs[k] = j
	return copyJob(j), nil
}
func (m *Memory) Update(_ context.Context, owner, device, id string, u Update, now time.Time) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(owner, id)
	j, ok := m.jobs[k]
	if !ok || j.DeviceID != device {
		return Job{}, ErrNotFound
	}
	out, e := Apply(j, u, now)
	if e == nil {
		m.jobs[k] = copyJob(out)
	}
	return copyJob(out), e
}
func (m *Memory) Cancel(_ context.Context, owner, id string, now time.Time) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(owner, id)
	j, ok := m.jobs[k]
	if !ok {
		return j, ErrNotFound
	}
	j.Attachments = WithoutAttachmentData(j.Attachments)
	if !Terminal(j.State) {
		j.State = "cancelled"
		j.Revision++
		j.Updated = now
		m.jobs[k] = j
	}
	return copyJob(j), nil
}
