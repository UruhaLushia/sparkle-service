package core

import (
	"strings"
	"sync"
	"sync/atomic"
)

type coreLogEventRule struct {
	parse func(string) (CoreEvent, bool)
}

var coreLogEventRules = []coreLogEventRule{
	{
		parse: parseTailscaleAuthCoreLogEvent,
	},
	{
		parse: parseTailscaleAuthDoneCoreLogEvent,
	},
}

type boundedOutputBuffer struct {
	mutex sync.Mutex
	buf   []byte
	limit int
}

func newBoundedOutputBuffer(limit int) *boundedOutputBuffer {
	if limit <= 0 {
		limit = startupBufferLimit
	}
	return &boundedOutputBuffer{limit: limit}
}

func (b *boundedOutputBuffer) Write(p []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *boundedOutputBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return string(b.buf)
}

type startupLogWatcher struct {
	mutex      sync.Mutex
	lineBuffer string
	fatal      chan error
	reported   bool
}

type coreLogEventWatcher struct {
	manager    *CoreManager
	mutex      sync.Mutex
	lineBuffer string
	active     atomic.Bool
}

func newStartupLogWatcher() *startupLogWatcher {
	return &startupLogWatcher{fatal: make(chan error, 1)}
}

func (w *startupLogWatcher) Write(p []byte) (int, error) {
	text := strings.ReplaceAll(string(p), "\r\n", "\n")

	w.mutex.Lock()
	defer w.mutex.Unlock()

	combined := w.lineBuffer + text
	lines := strings.Split(combined, "\n")
	if strings.HasSuffix(combined, "\n") {
		w.lineBuffer = ""
	} else {
		w.lineBuffer = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
		if len(w.lineBuffer) > startupLineLimit {
			w.lineBuffer = w.lineBuffer[len(w.lineBuffer)-startupLineLimit:]
		}
	}

	for _, line := range lines {
		w.reportFatal(startupFatalLineError(line))
	}
	if w.lineBuffer != "" {
		w.reportFatal(startupFatalLineError(w.lineBuffer))
	}

	return len(p), nil
}

func (w *startupLogWatcher) Fatal() <-chan error {
	return w.fatal
}

func (w *startupLogWatcher) reportFatal(err error) {
	if err == nil || w.reported {
		return
	}
	w.reported = true
	w.fatal <- err
}

func newCoreLogEventWatcher(manager *CoreManager) *coreLogEventWatcher {
	watcher := &coreLogEventWatcher{manager: manager}
	watcher.active.Store(true)
	return watcher
}

func (w *coreLogEventWatcher) Write(p []byte) (int, error) {
	if !w.active.Load() {
		return len(p), nil
	}

	text := strings.ReplaceAll(string(p), "\r\n", "\n")

	w.mutex.Lock()
	defer w.mutex.Unlock()
	if !w.active.Load() {
		return len(p), nil
	}

	combined := w.lineBuffer + text
	lines := strings.Split(combined, "\n")
	if strings.HasSuffix(combined, "\n") {
		w.lineBuffer = ""
	} else {
		w.lineBuffer = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
		if len(w.lineBuffer) > startupLineLimit {
			w.lineBuffer = w.lineBuffer[len(w.lineBuffer)-startupLineLimit:]
		}
	}

	for _, line := range lines {
		w.manager.publishCoreLogEvent(line)
	}

	return len(p), nil
}

func (w *coreLogEventWatcher) Stop() {
	if !w.active.Swap(false) {
		return
	}

	w.mutex.Lock()
	line := w.lineBuffer
	w.lineBuffer = ""
	w.mutex.Unlock()

	w.manager.publishCoreLogEvent(line)
}

func (cm *CoreManager) publishCoreLogEvent(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	for _, rule := range coreLogEventRules {
		event, ok := rule.parse(line)
		if !ok {
			continue
		}
		cm.publishCoreEvent(event)
	}
}

func parseTailscaleAuthCoreLogEvent(line string) (CoreEvent, bool) {
	const prefix = "[Tailscale]("
	const marker = ") To start this tsnet server, restart with TS_AUTHKEY set, or go to: "

	_, rest, ok := strings.Cut(line, prefix)
	if !ok {
		return CoreEvent{}, false
	}

	markerIndex := strings.Index(rest, marker)
	if markerIndex <= 0 {
		return CoreEvent{}, false
	}

	name := rest[:markerIndex]
	url := strings.TrimSpace(rest[markerIndex+len(marker):])
	if end := strings.IndexAny(url, " \t\"'<>"); end >= 0 {
		url = url[:end]
	}
	if name == "" || (!strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://")) {
		return CoreEvent{}, false
	}

	return CoreEvent{
		Type:    CoreEventLog,
		Message: "tailscale_auth",
		Data: map[string]string{
			"name": name,
			"url":  url,
		},
	}, true
}

func parseTailscaleAuthDoneCoreLogEvent(line string) (CoreEvent, bool) {
	const prefix = "[Tailscale]("
	const marker = ") AuthLoop: state is Starting; done"

	_, rest, ok := strings.Cut(line, prefix)
	if !ok {
		return CoreEvent{}, false
	}

	markerIndex := strings.Index(rest, marker)
	if markerIndex <= 0 {
		return CoreEvent{}, false
	}

	name := rest[:markerIndex]
	if name == "" {
		return CoreEvent{}, false
	}

	return CoreEvent{
		Type:    CoreEventLog,
		Message: "tailscale_auth_done",
		Data: map[string]string{
			"name": name,
		},
	}, true
}
