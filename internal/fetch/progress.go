package fetch

import (
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"time"
)

func newHash() hash.Hash { return sha256.New() }

// progress draws a single console line, rewritten at most ten times a second.
type progress struct {
	out   io.Writer
	total int64
	done  int64
	start time.Time
	last  time.Time
}

func newProgress(out io.Writer, total int64) *progress {
	now := time.Now()
	return &progress{out: out, total: total, start: now, last: now}
}

func (p *progress) add(n int) {
	p.done += int64(n)
	if now := time.Now(); now.Sub(p.last) >= 100*time.Millisecond {
		p.last = now
		p.draw(now)
	}
}

func (p *progress) finish() {
	p.draw(time.Now())
	fmt.Fprintln(p.out)
}

func (p *progress) draw(now time.Time) {
	const mb = 1 << 20
	speed := 0.0
	if secs := now.Sub(p.start).Seconds(); secs > 0 {
		speed = float64(p.done) / mb / secs
	}
	if p.total > 0 {
		fmt.Fprintf(p.out, "\r%3d%%  %.1f / %.1f MB  %.1f MB/s   ", p.done*100/p.total, float64(p.done)/mb, float64(p.total)/mb, speed)
		return
	}
	fmt.Fprintf(p.out, "\r%.1f MB  %.1f MB/s   ", float64(p.done)/mb, speed)
}
