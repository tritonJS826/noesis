// Package consoleshared implements the live terminal progress display shared
// by the CLI and TUI frontends. It is not a standalone UI: it only renders
// progress events from internal/core.
package consoleshared

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"noesis/internal/core"
)

const (
	cReset = "\x1b[0m"
	cBold  = "\x1b[1m"
	cDim   = "\x1b[2m"
	cCyan  = "\x1b[36m"
	cGreen = "\x1b[32m"
	cYel   = "\x1b[33m"
	cRed   = "\x1b[31m"
	eol    = "\x1b[K"
)

// Progress renders phase progress on an ANSI terminal. It implements
// core.Reporter.
type Progress struct {
	db  *sql.DB
	tty bool

	mu      sync.Mutex
	phase   string
	current string
	done    int
	total   int
	ran     int // processed in this phase run (for ETA)
	started time.Time
	errs    int
	logs    []string

	runTags   int           // new tags in this phase run
	runLinks  int           // new links in this phase run
	curStart  time.Time     // when the current operation started
	lastDur   time.Duration // duration of the previous operation
	lastTags  int           // tags produced by previous pair
	lastLinks int           // 1 if the previous pair produced a link

	quit chan struct{}
	wg   sync.WaitGroup
}

// New creates a progress reporter. It detects whether stdout is a terminal.
func New(db *sql.DB) *Progress {
	st, _ := os.Stdout.Stat()
	return &Progress{db: db, tty: st != nil && st.Mode()&os.ModeCharDevice != 0, started: time.Now()}
}

func (p *Progress) SetPhase(name string, done, total int) {
	p.mu.Lock()
	p.phase, p.done, p.total, p.ran, p.started, p.current = name, done, total, 0, time.Now(), ""
	p.runTags, p.runLinks, p.lastDur, p.lastTags, p.lastLinks = 0, 0, 0, 0, 0
	p.curStart = time.Now()
	p.mu.Unlock()
}

func (p *Progress) SetCurrent(s string) {
	p.mu.Lock()
	p.current = s
	p.curStart = time.Now()
	p.mu.Unlock()
}

// AddRun records the result of a processed pair: how many new tags and links it produced.
func (p *Progress) AddRun(tags, links int) {
	p.mu.Lock()
	p.runTags += tags
	p.runLinks += links
	p.lastTags, p.lastLinks = tags, links
	p.lastDur = time.Since(p.curStart)
	p.mu.Unlock()
}

func (p *Progress) Advance(skipped bool) {
	p.mu.Lock()
	p.done++
	if !skipped {
		p.ran++
	}
	p.mu.Unlock()
}

func (p *Progress) Logf(format string, a ...any) {
	p.mu.Lock()
	p.logs = append(p.logs, time.Now().Format("15:04:05 ")+fmt.Sprintf(format, a...))
	if len(p.logs) > 8 {
		p.logs = p.logs[len(p.logs)-8:]
	}
	p.mu.Unlock()
}

func (p *Progress) Fail(format string, a ...any) {
	p.mu.Lock()
	p.errs++
	p.mu.Unlock()
	p.Logf(cRed+"error: "+cReset+format, a...)
}

func bar(done, total, w int) string {
	if total <= 0 {
		return strings.Repeat("░", w)
	}
	f := done * w / total
	if f > w {
		f = w
	}
	return cGreen + strings.Repeat("█", f) + cDim + strings.Repeat("░", w-f) + cReset
}

func (p *Progress) render() string {
	s := core.LoadStats(p.db)
	p.mu.Lock()
	defer p.mu.Unlock()

	var b strings.Builder
	w := func(format string, a ...any) { b.WriteString(fmt.Sprintf(format, a...) + eol + "\n") }

	n := s.Notes
	pairsTotal := n * (n - 1) / 2
	phase := p.phase
	if phase == "" {
		phase = "—"
	}
	line := strings.Repeat("─", 64)

	w(" %s%snoesis%s  phase: %s%s%s   %s(Ctrl+C — stop, progress is saved)%s", cBold, cCyan, cReset, cYel, phase, cReset, cDim, cReset)
	w(" %s%s%s", cDim, line, cReset)
	w(" Originals in db    : %s%d%s", cBold, s.Notes, cReset)
	w(" Notes with tags    : %s%d%s / %d", cBold, s.Tagged, cReset, s.Notes)
	w(" Tags               : %s%d%s unique  (%d assignments)", cBold, s.UniqTags, cReset, s.TagAssign)
	w(" Links              : %s%d%s pairs (bidirectional)", cBold, s.Links, cReset)
	w(" Pairs analyzed     : %s%d%s / %d", cBold, s.PairsDone, cReset, pairsTotal)
	w(" Written to vault   : %s%d%s / %d", cBold, s.Written, cReset, s.Notes)
	w(" %s%s%s", cDim, line, cReset)

	pct := 0
	if p.total > 0 {
		pct = p.done * 100 / p.total
	}
	elapsed := time.Since(p.started)
	eta, speed, avg := "—", "—", "—"
	if p.ran > 0 && elapsed > 0 {
		per := elapsed / time.Duration(p.ran)
		avg = per.Round(10 * time.Millisecond).String()
		speed = fmt.Sprintf("%.1f", float64(p.ran)/elapsed.Minutes())
		if p.total > p.done {
			eta = (per * time.Duration(p.total-p.done)).Round(time.Second).String()
		} else {
			eta = "0s"
		}
	}
	w(" %s %s%3d%%%s  %s%d%s / %d", bar(p.done, p.total, 36), cBold, pct, cReset, cBold, p.done, cReset, p.total)
	w(" Elapsed %s  ·  remaining ~%s  ·  %s items/min  ·  avg %s/item",
		elapsed.Round(time.Second), eta, speed, avg)
	w(" This run: %s+%d%s processed, %s+%d%s tags, %s+%d%s links, %d left",
		cGreen, p.ran, cReset, cGreen, p.runTags, cReset, cGreen, p.runLinks, cReset, p.total-p.done)
	cur := p.current
	if r := []rune(cur); len(r) > 70 {
		cur = string(r[:70]) + "…"
	}
	w(" Now: %s", cur)
	if p.current != "" {
		last := "—"
		if p.lastDur > 0 {
			last = fmt.Sprintf("%s (+%d tags, +%d links)", p.lastDur.Round(10*time.Millisecond), p.lastTags, p.lastLinks)
		}
		w(" Current operation running %s  ·  previous: %s", time.Since(p.curStart).Round(100*time.Millisecond), last)
	} else {
		w("")
	}
	errc := cGreen
	if p.errs > 0 {
		errc = cRed
	}
	w(" Errors: %s%d%s", errc, p.errs, cReset)
	w(" %sLog:%s", cDim, cReset)
	for i := 0; i < 8; i++ {
		if i < len(p.logs) {
			w("  %s", p.logs[i])
		} else {
			w("")
		}
	}
	return b.String()
}

// Start begins periodic rendering on a terminal. It is a no-op otherwise.
func (p *Progress) Start() {
	p.quit = make(chan struct{})
	if !p.tty {
		return
	}
	fmt.Print("\x1b[?1049h\x1b[?25l")
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				fmt.Print("\x1b[H" + p.render() + "\x1b[J")
			case <-p.quit:
				return
			}
		}
	}()
}

// Stop stops rendering and prints the final progress snapshot.
func (p *Progress) Stop() {
	if p.quit != nil {
		close(p.quit)
		p.wg.Wait()
	}
	if p.tty {
		fmt.Print("\x1b[?25h\x1b[?1049l")
	}
	fmt.Print(strings.ReplaceAll(p.render(), eol, ""))
}
