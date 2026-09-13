package host

import (
	"bytes"
	"errors"
	"strings"
	"sync"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

const providerEventLimit = 64 * 1024

type protocolCollector struct {
	mu              sync.Mutex
	provider        adapter.Provider
	parser          *adapter.StreamParser
	line            []byte
	oversized       bool
	critical        bool
	markerTail      []byte
	err             error
	terminal        *adapter.Event
	sessionID       string
	message         []byte
	droppedOversize int
	onSession       func(string, string) error
	sessionObserved bool
	sessionReported bool
}

func newProtocolCollector(provider string) (*protocolCollector, error) {
	p := adapter.Provider(provider)
	if p != adapter.ProviderClaude && p != adapter.ProviderAGY && p != adapter.ProviderGrok && p != adapter.ProviderCodex {
		return nil, errors.New("provider_output_protocol_invalid")
	}
	return &protocolCollector{provider: p, parser: adapter.NewStreamParser(p, providerEventLimit, int(^uint(0)>>1)), line: make([]byte, 0, 4096)}, nil
}

func (p *protocolCollector) Write(data []byte) (int, error) {
	written := len(data)
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		part := data
		complete := false
		if index >= 0 {
			part, data, complete = data[:index], data[index+1:], true
		}
		p.consumePart(part)
		if complete {
			p.finishLine()
		}
		if index < 0 {
			break
		}
	}
	return written, nil
}

func (p *protocolCollector) consumePart(part []byte) {
	for len(part) > 0 {
		length := len(part)
		if length > 4096 {
			length = 4096
		}
		chunk := part[:length]
		part = part[length:]
		probe := make([]byte, 0, len(p.markerTail)+len(chunk))
		probe = append(probe, p.markerTail...)
		probe = append(probe, chunk...)
		compact := make([]byte, 0, len(probe))
		for _, value := range probe {
			if value != ' ' && value != '\t' && value != '\r' && value != '\n' {
				compact = append(compact, value)
			}
		}
		if bytes.Contains(compact, []byte(`"type":"result"`)) ||
			bytes.Contains(compact, []byte(`"event":"result"`)) ||
			bytes.Contains(compact, []byte(`"type":"turn.completed"`)) ||
			bytes.Contains(compact, []byte(`"type":"turn.failed"`)) {
			p.critical = true
		}
		if len(probe) > 128 {
			probe = probe[len(probe)-128:]
		}
		p.markerTail = append(p.markerTail[:0], probe...)
		if p.oversized {
			continue
		}
		if len(p.line)+len(chunk) > providerEventLimit {
			remaining := providerEventLimit - len(p.line)
			if remaining > 0 {
				p.line = append(p.line, chunk[:remaining]...)
			}
			p.oversized = true
			continue
		}
		p.line = append(p.line, chunk...)
	}
}

func (p *protocolCollector) finishLine() {
	if p.err == nil {
		if p.oversized {
			if p.critical {
				p.err = errors.New("provider_critical_event_too_large")
			} else {
				p.droppedOversize++
			}
		} else if len(bytes.TrimSpace(p.line)) > 0 {
			line := bytes.TrimSuffix(p.line, []byte{'\r'})
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte{'{'}) {
				event, err := p.parser.Parse(string(line))
				if err != nil {
					p.err = err
				} else {
					p.accept(event)
				}
			}
		}
	}
	p.line = p.line[:0]
	p.markerTail = p.markerTail[:0]
	p.oversized = false
	p.critical = false
}

func (p *protocolCollector) accept(event adapter.Event) {
	if event.SessionID != "" && p.sessionID != "" && p.sessionID != event.SessionID {
		p.err = errors.New("provider_session_changed")
		return
	}
	if p.provider == adapter.ProviderCodex {
		switch event.Kind {
		case adapter.EventInit:
			if p.sessionObserved {
				p.err = errors.New("provider_session_duplicate")
				return
			}
		case adapter.EventMessage:
			if !p.sessionObserved {
				p.err = errors.New("provider_event_before_session")
				return
			}
			if p.terminal != nil {
				p.err = errors.New("provider_event_after_terminal")
				return
			}
		case adapter.EventResult, adapter.EventQuestion:
			if !p.sessionObserved {
				p.err = errors.New("provider_event_before_session")
				return
			}
		}
	}
	if event.SessionID != "" {
		p.sessionID = event.SessionID
		if p.isAuthoritativeSessionEvent(event) {
			p.sessionObserved = true
			p.reportSession()
		}
	}
	if event.Kind == adapter.EventMessage && p.provider == adapter.ProviderCodex && event.Text != "" {
		if len(event.Text) > providerEventLimit {
			p.err = errors.New("provider_critical_event_too_large")
			return
		}
		p.message = append(p.message[:0], event.Text...)
		return
	}
	if event.Kind == adapter.EventMessage && p.provider == adapter.ProviderGrok && event.Text != "" {
		if len(p.message) > providerEventLimit-len(event.Text) {
			p.err = errors.New("provider_critical_event_too_large")
			return
		}
		p.message = append(p.message, event.Text...)
		return
	}
	if event.Kind != adapter.EventResult && event.Kind != adapter.EventQuestion {
		return
	}
	if p.terminal != nil {
		p.err = errors.New("provider_multiple_terminal_events")
		return
	}
	copy := event
	p.terminal = &copy
}

func (p *protocolCollector) SetSessionObserver(observer func(string, string) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if observer == nil || p.onSession != nil {
		return errors.New("provider_session_observer_invalid")
	}
	p.onSession = observer
	p.reportSession()
	return p.err
}

func (p *protocolCollector) reportSession() {
	if p.err != nil || !p.sessionObserved || p.sessionReported || p.onSession == nil {
		return
	}
	if err := p.onSession(providerSessionKind(p.provider), p.sessionID); err != nil {
		p.err = err
		return
	}
	p.sessionReported = true
}

func (p *protocolCollector) isAuthoritativeSessionEvent(event adapter.Event) bool {
	if p.provider == adapter.ProviderGrok {
		return event.Kind == adapter.EventMessage
	}
	return event.Kind == adapter.EventInit
}

func (p *protocolCollector) Finish() (adapter.Event, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.line) > 0 || p.oversized {
		p.finishLine()
	}
	if p.err != nil {
		return adapter.Event{}, p.sessionID, p.err
	}
	if p.terminal == nil {
		return adapter.Event{}, p.sessionID, errors.New("provider_terminal_missing")
	}
	terminal := *p.terminal
	if terminal.Text == "" && (p.provider == adapter.ProviderGrok || p.provider == adapter.ProviderCodex) {
		terminal.Text = string(p.message)
	}
	if p.provider == adapter.ProviderCodex && terminal.Status == "completed" && strings.TrimSpace(terminal.Text) == "" {
		return adapter.Event{}, p.sessionID, errors.New("provider_result_missing")
	}
	if terminal.SessionID == "" {
		terminal.SessionID = p.sessionID
	}
	if terminal.SessionID == "" {
		return adapter.Event{}, "", errors.New("provider_session_missing")
	}
	return terminal, terminal.SessionID, nil
}

func providerSessionKind(provider adapter.Provider) string {
	if provider == adapter.ProviderAGY {
		return "conversation-id"
	}
	return "session-id"
}

func providerSucceeded(provider adapter.Provider, event adapter.Event) bool {
	switch provider {
	case adapter.ProviderClaude:
		return event.Status == "success"
	case adapter.ProviderAGY:
		return event.Status == "SUCCESS"
	case adapter.ProviderGrok:
		return event.Status == "success" || event.Status == "end_turn"
	case adapter.ProviderCodex:
		return event.Status == "completed"
	default:
		return false
	}
}
