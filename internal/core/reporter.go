package core

// Reporter receives progress events from the processing phases. Every UI
// (TUI, CLI, web, GUI) implements this interface to observe long-running work.
type Reporter interface {
	SetPhase(name string, done, total int)
	SetCurrent(s string)
	AddRun(tags, links int)
	Advance(skipped bool)
	Logf(format string, a ...any)
	Fail(format string, a ...any)
}

// NoopReporter ignores all progress events.
type NoopReporter struct{}

func (NoopReporter) SetPhase(string, int, int) {}
func (NoopReporter) SetCurrent(string)         {}
func (NoopReporter) AddRun(int, int)           {}
func (NoopReporter) Advance(bool)              {}
func (NoopReporter) Logf(string, ...any)       {}
func (NoopReporter) Fail(string, ...any)       {}
