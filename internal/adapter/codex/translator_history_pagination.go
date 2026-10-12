package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/jsonrpcutil"
)

// A history read owns only its exact response IDs, never thread selection or
// turn ownership. Each page has one outstanding request for this job.
type historyReadJob struct {
	commandID, threadID, detailTurnID string
	history                           agentproto.ThreadHistoryRecord
	turnIDs, itemIDs, cursors         map[string]bool
	items                             []agentproto.ThreadHistoryItemRecord
	pages                             int
}

type pendingHistoryPage struct {
	job    *historyReadJob
	method string
}

func (t *Translator) translateSharedHistoryRead(command agentproto.Command) ([][]byte, error) {
	if strings.TrimSpace(command.Target.ThreadID) == "" {
		return nil, fmt.Errorf("history read requires a thread id")
	}
	job := &historyReadJob{
		commandID: command.CommandID, threadID: command.Target.ThreadID, detailTurnID: command.Target.TurnID,
		turnIDs: map[string]bool{}, itemIDs: map[string]bool{}, cursors: map[string]bool{},
	}
	return t.requestHistoryPage(job, "thread/read", "")
}

func (t *Translator) requestHistoryPage(job *historyReadJob, method, cursor string) ([][]byte, error) {
	params := map[string]any{"threadId": job.threadID}
	if method == "thread/read" {
		params["includeTurns"] = false
	} else {
		params["limit"], params["sortDirection"] = 50, "asc"
		if cursor != "" {
			params["cursor"] = cursor
		}
		if method == "thread/items/list" {
			params["turnId"] = job.detailTurnID
		}
	}
	id := t.NextRequest("thread-history-page")
	frame, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	t.pendingHistoryPages[id] = pendingHistoryPage{job: job, method: method}
	return [][]byte{append(frame, '\n')}, nil
}

func (t *Translator) finishHistoryRead(job *historyReadJob, errorMessage string) Result {
	event := agentproto.Event{Kind: agentproto.EventThreadHistoryRead, CommandID: job.commandID, ThreadID: job.threadID, ErrorMessage: errorMessage}
	if errorMessage == "" {
		event.ThreadHistory = &job.history
	}
	return Result{Suppress: true, Events: []agentproto.Event{event}}
}

func (t *Translator) advanceHistoryRead(job *historyReadJob, method, cursor string) Result {
	frames, err := t.requestHistoryPage(job, method, cursor)
	if err != nil {
		return t.finishHistoryRead(job, err.Error())
	}
	return Result{Suppress: true, OutboundToCodex: frames}
}

func (t *Translator) observeHistoryPage(requestID string, message map[string]any) (Result, bool) {
	pending, ok := t.pendingHistoryPages[requestID]
	if !ok {
		return Result{}, false
	}
	delete(t.pendingHistoryPages, requestID)
	job := pending.job
	fail := func(reason string) (Result, bool) { return t.finishHistoryRead(job, reason), true }
	if errMsg := jsonrpcutil.ExtractErrorMessage(message); errMsg != "" {
		return fail(errMsg)
	}
	object, ok := message["result"].(map[string]any)
	if !ok {
		return fail("history response has no result object")
	}
	if pending.method == "thread/read" {
		job.history.Thread = parseThreadRecord(object)
		if job.history.Thread.ThreadID != job.threadID {
			return fail("history metadata thread id does not match requested thread")
		}
		return t.advanceHistoryRead(job, "thread/turns/list", ""), true
	}
	if threadID := lookupString(object, "threadId"); threadID != "" && threadID != job.threadID {
		return fail("history page thread id does not match requested thread")
	}
	if pending.method == "thread/items/list" {
		if turnID := lookupString(object, "turnId"); turnID != "" && turnID != job.detailTurnID {
			return fail("history page turn id does not match requested turn")
		}
	}
	data, ok := object["data"].([]any)
	if !ok {
		return fail("history page has no data array")
	}
	cursor, ok := object["nextCursor"].(string)
	if object["nextCursor"] != nil && !ok {
		return fail("history page has invalid next cursor")
	}
	job.pages++
	if job.pages > 10000 || (cursor != "" && (len(data) == 0 || job.cursors[pending.method+":"+cursor])) {
		return fail("history pagination did not progress")
	}
	if pending.method == "thread/turns/list" {
		turns := parseThreadHistoryTurns(data)
		if len(turns) != len(data) {
			return fail("history page contains invalid turns")
		}
		for _, turn := range turns {
			if job.turnIDs[turn.TurnID] {
				return fail("history pagination repeated a turn")
			}
			job.turnIDs[turn.TurnID] = true
			job.history.Turns = append(job.history.Turns, turn)
		}
	} else {
		items, err := parseHistoryPageItems(data, job.detailTurnID)
		if err != nil {
			return fail(err.Error())
		}
		for _, item := range items {
			if item.ItemID != "" && job.itemIDs[item.ItemID] {
				return fail("history pagination repeated an item")
			}
			job.itemIDs[item.ItemID] = true
			job.items = append(job.items, item)
		}
	}
	if cursor != "" {
		job.cursors[pending.method+":"+cursor] = true
		return t.advanceHistoryRead(job, pending.method, cursor), true
	}
	if pending.method == "thread/turns/list" && job.detailTurnID != "" {
		if !job.turnIDs[job.detailTurnID] {
			return fail("requested history turn was not found")
		}
		return t.advanceHistoryRead(job, "thread/items/list", ""), true
	}
	if pending.method == "thread/items/list" {
		for i := range job.history.Turns {
			if job.history.Turns[i].TurnID == job.detailTurnID {
				job.history.Turns[i].Items = job.items
				break
			}
		}
	}
	return t.finishHistoryRead(job, ""), true
}

// thread/items/list returns timing/turn envelopes around the native item.
// Unwrap only this paginated API; private thread/read still uses flat items.
func parseHistoryPageItems(data []any, turnID string) ([]agentproto.ThreadHistoryItemRecord, error) {
	nativeItems := make([]any, 0, len(data))
	for _, value := range data {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("history page contains invalid item entry")
		}
		if entryTurnID, exists := entry["turnId"]; exists {
			entryTurn, ok := entryTurnID.(string)
			if !ok || entryTurn != turnID {
				return nil, fmt.Errorf("history item turn id does not match requested turn")
			}
		}
		if wrapped, exists := entry["item"]; exists {
			item, ok := wrapped.(map[string]any)
			if !ok || item == nil {
				return nil, fmt.Errorf("history page contains invalid wrapped item")
			}
			nativeItems = append(nativeItems, item)
		} else {
			nativeItems = append(nativeItems, entry)
		}
	}
	items := parseThreadHistoryItems(nativeItems)
	if len(items) != len(data) {
		return nil, fmt.Errorf("history page contains invalid items")
	}
	return items, nil
}
