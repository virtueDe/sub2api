package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

type openAISSEDataAccumulator struct {
	lines     []string
	eventType string
}

func (a *openAISSEDataAccumulator) AddLine(line string, fn func([]byte)) {
	if fn != nil {
		a.AddEventLine(line, func(_ string, data []byte) { fn(data) })
	}
}

func (a *openAISSEDataAccumulator) AddEventLine(line string, fn func(string, []byte)) {
	if fn == nil {
		return
	}
	trimmedLine := strings.TrimRight(line, "\r\n")
	if eventType, ok := extractOpenAISSEEventLine(trimmedLine); ok {
		a.eventType = eventType
		return
	}
	if data, ok := extractOpenAISSEDataLine(trimmedLine); ok {
		a.lines = append(a.lines, data)
		return
	}
	if strings.TrimSpace(trimmedLine) == "" {
		a.FlushEvent(fn)
	}
}

func (a *openAISSEDataAccumulator) Flush(fn func([]byte)) {
	if fn != nil {
		a.FlushEvent(func(_ string, data []byte) { fn(data) })
	}
}

func (a *openAISSEDataAccumulator) FlushEvent(fn func(string, []byte)) {
	if fn == nil {
		return
	}
	emitOpenAISSEDataPayloads(a.lines, func(data []byte) { fn(a.eventType, data) })
	clear(a.lines)
	a.lines = a.lines[:0]
	// Even a frame without data terminates the event name's scope.
	a.eventType = ""
}

func forEachOpenAISSEDataPayload(body string, fn func([]byte)) {
	if fn == nil || strings.TrimSpace(body) == "" {
		return
	}
	var acc openAISSEDataAccumulator
	for _, line := range strings.Split(body, "\n") {
		acc.AddLine(line, fn)
	}
	acc.Flush(fn)
}

func forEachOpenAISSEFrame(body string, fn func(string, []byte)) {
	if fn == nil || strings.TrimSpace(body) == "" {
		return
	}
	var parser openAICompatSSEFrameParser
	emit := func(frame openAICompatSSEFrame, ok bool) {
		if !ok {
			return
		}
		emitData := func(value string) {
			value = strings.TrimSpace(value)
			if value == "" || value == "[DONE]" {
				return
			}
			data := []byte(value)
			fn(effectiveOpenAISSEEventType(data, frame.EventType), data)
		}
		if gjson.Valid(frame.Data) {
			emitData(frame.Data)
			return
		}
		for _, value := range strings.Split(frame.Data, "\n") {
			emitData(value)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		emit(parser.AddLine(strings.TrimRight(line, "\r")))
	}
	emit(parser.Finish())
}

func emitOpenAISSEDataPayloads(lines []string, fn func([]byte)) {
	if fn == nil || len(lines) == 0 {
		return
	}
	if len(lines) == 1 {
		emitOpenAISSEDataPayload(lines[0], fn)
		return
	}
	joined := strings.Join(lines, "\n")
	if gjson.Valid(joined) {
		emitOpenAISSEDataPayload(joined, fn)
		return
	}
	for _, line := range lines {
		emitOpenAISSEDataPayload(line, fn)
	}
}

func emitOpenAISSEDataPayload(data string, fn func([]byte)) {
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return
	}
	fn([]byte(data))
}
