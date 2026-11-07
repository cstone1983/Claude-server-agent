package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const (
	jobBufferBytes = 4 << 20
	jobRetention   = 24 * time.Hour
	maxRunningJobs = 20
)

type job struct {
	ID       string
	Summary  string
	Started  time.Time
	mu       sync.Mutex
	buf      []byte
	dropped  int64 // bytes discarded from the front of buf
	ended    time.Time
	exitCode int
	status   string // running, exited, cancelled, failed
	cancel   context.CancelFunc
}

func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.buf = append(j.buf, p...)
	if over := len(j.buf) - jobBufferBytes; over > 0 {
		j.buf = append([]byte{}, j.buf[over:]...)
		j.dropped += int64(over)
	}
	return len(p), nil
}

type JobView struct {
	JobID      string `json:"job_id"`
	Summary    string `json:"summary"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at,omitempty"`
	Output     string `json:"output"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Note       string `json:"note,omitempty"`
}

func (j *job) view(offset int64, max int) JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := JobView{JobID: j.ID, Summary: j.Summary, Status: j.status, StartedAt: j.Started.UTC().Format(time.RFC3339)}
	if !j.ended.IsZero() {
		v.EndedAt = j.ended.UTC().Format(time.RFC3339)
		ec := j.exitCode
		v.ExitCode = &ec
	}
	total := j.dropped + int64(len(j.buf))
	if offset < j.dropped {
		if offset > 0 {
			v.Note = fmt.Sprintf("output before offset %d was discarded (buffer keeps the last %d bytes)", j.dropped, jobBufferBytes)
		}
		offset = j.dropped
	}
	if offset > total {
		offset = total
	}
	chunk := j.buf[offset-j.dropped:]
	if len(chunk) > max {
		chunk = chunk[:max]
	}
	v.Offset = offset
	v.NextOffset = offset + int64(len(chunk))
	v.Output = cleanOutput(string(chunk))
	if v.NextOffset < total {
		v.Note = "more output available: call job_status again with offset=next_offset"
	}
	return v
}

type jobManager struct {
	mu   sync.Mutex
	jobs map[string]*job
}

func newJobManager() *jobManager { return &jobManager{jobs: map[string]*job{}} }

func (m *jobManager) start(s procSpec, summary string) (*job, error) {
	m.mu.Lock()
	running := 0
	for id, j := range m.jobs {
		j.mu.Lock()
		if j.status == "running" {
			running++
		} else if time.Since(j.ended) > jobRetention {
			delete(m.jobs, id)
		}
		j.mu.Unlock()
	}
	m.mu.Unlock()
	if running >= maxRunningJobs {
		return nil, fmt.Errorf("too many running jobs (%d)", running)
	}
	b := make([]byte, 6)
	rand.Read(b)
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	j := &job{ID: "job_" + hex.EncodeToString(b), Summary: summary, Started: time.Now(), status: "running", cancel: cancel}
	cmd, err := buildCmd(ctx, s)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = j, j
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.mu.Unlock()
	go func() {
		err := cmd.Wait()
		j.mu.Lock()
		defer j.mu.Unlock()
		j.ended = time.Now()
		j.exitCode = -1
		if cmd.ProcessState != nil {
			j.exitCode = cmd.ProcessState.ExitCode()
		}
		switch {
		case j.status == "cancelled":
		case ctx.Err() == context.DeadlineExceeded:
			j.status = "timed_out"
		case err != nil && cmd.ProcessState == nil:
			j.status = "failed"
		default:
			j.status = "exited"
		}
		cancel()
	}()
	return j, nil
}

func (m *jobManager) get(id string) *job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func (m *jobManager) list() []JobView {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []JobView
	for _, j := range m.jobs {
		v := j.view(1<<62, 0)
		v.Output, v.Note = "", ""
		out = append(out, v)
	}
	return out
}

func (m *jobManager) cancel(id string) error {
	j := m.get(id)
	if j == nil {
		return fmt.Errorf("no such job %s", id)
	}
	j.mu.Lock()
	if j.status == "running" {
		j.status = "cancelled"
	}
	j.mu.Unlock()
	j.cancel()
	return nil
}
