package testcodex

import (
	"encoding/json"
	"fmt"
	"strings"
)

type changePeer struct {
	mode           string
	threads, turns int
	input          json.RawMessage
	mainTurn       string
}

func (p *changePeer) handle(method string, id, params json.RawMessage, write func(any)) bool {
	if !strings.HasPrefix(p.mode, "schedule_change_") {
		return false
	}
	if method != "thread/start" && method != "turn/start" && method != "turn/steer" {
		return false
	}
	reply := func(result any) { write(map[string]any{"id": id, "result": result}) }
	var request struct {
		ThreadID, Model string
		Input           []struct{ Text string }
	}
	must(json.Unmarshal(params, &request))
	switch method {
	case "thread/start":
		thread := "thread"
		if request.Model == "gpt-5.6-terra" {
			p.threads++
			thread = fmt.Sprintf("change-assessor-%d", p.threads)
		}
		reply(map[string]any{"thread": map[string]any{"id": thread}})
		return true
	case "turn/start":
		if strings.HasPrefix(request.ThreadID, "change-assessor-") {
			turn := "assessment-turn"
			reply(map[string]any{"turn": map[string]any{"id": turn}})
			if p.mode == "schedule_change_wait" {
				return true
			}
			if p.mode == "schedule_change_finish_first" || p.mode == "schedule_change_cosmetic" {
				p.complete(write)
			}
			text := `{"significant":true,"reason":"The acceptance criteria changed.","follow_up":"Implement the updated requirement and verify it."}`
			if p.mode == "schedule_change_cosmetic" {
				text = `{"significant":false,"reason":"Only wording changed.","follow_up":""}`
			}
			if p.mode == "schedule_change_invalid" {
				text = `{"significant":false}`
			}
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": request.ThreadID, "turnId": turn, "item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": text}}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": request.ThreadID, "turn": map[string]any{"id": turn, "status": "completed"}}})
			return true
		}
		p.turns++
		p.mainTurn = fmt.Sprintf("implementation-%d", p.turns)
		p.input = append(json.RawMessage(nil), params...)
		reply(map[string]any{"turn": map[string]any{"id": p.mainTurn}})
		if p.turns > 1 || p.mode == "schedule_change_boundary" {
			p.complete(write)
		}
		return true
	case "turn/steer":
		reply(map[string]any{"turnId": p.mainTurn})
		// Deliberately return the original result to exercise a final-answer race.
		p.complete(write)
		return true
	}
	return false
}

func (p *changePeer) complete(write func(any)) {
	report := completeScheduled("schedule_success", p.input)
	write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread", "turnId": p.mainTurn, "item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": report}}})
	write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": p.mainTurn, "status": "completed"}}})
}
