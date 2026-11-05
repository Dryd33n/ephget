package fetch

import (
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/Ephget/ephget/internal/ui"
)

func newHash() hash.Hash { return sha256.New() }

// barWidth is the progress bar's size. It leaves room for the numbers beside it in
// the 80 columns a new console usually has.
const barWidth = 28

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
	done := float64(p.done) / mb
	if p.total <= 0 {
		fmt.Fprintf(p.out, "\r%.1f MB  %.1f MB/s   ", done, speed)
		return
	}
	percent := p.done * 100 / p.total
	total := float64(p.total) / mb
	if ui.Interactive() {
		bar := ui.Out.Bar(float64(p.done)/float64(p.total), barWidth)
		fmt.Fprintf(p.out, "\r%s %3d%%  %.1f / %.1f MB  %s   ", bar, percent, done, total, ui.Out.Dim(fmt.Sprintf("%.1f MB/s", speed)))
		return
	}
	fmt.Fprintf(p.out, "\r%3d%%  %.1f / %.1f MB  %.1f MB/s   ", percent, done, total, speed)
}
