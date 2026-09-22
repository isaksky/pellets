package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

var _ storage.PlanningReader = (*WebReader)(nil)
var _ storage.PlanningWriter = (*WebWriter)(nil)

type planningReceipt struct {
	draft  storage.PlanningDraft
	pellet storage.Pellet
}

func freezePlanningState(state storage.PlanningState) (storage.PlanningState, error) {
	if err := storage.ValidatePlanningState(state); err != nil {
		return state, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return state, err
	}
	var frozen storage.PlanningState
	if err = json.Unmarshal(encoded, &frozen); err != nil {
		return state, err
	}
	if frozen.Messages == nil {
		frozen.Messages = []storage.PlanningMessage{}
	}
	if frozen.Drafts == nil {
		frozen.Drafts = []storage.PlanningDraft{}
	}
	return frozen, nil
}
func planningVersion(chatID, revision int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(revision, 10)
}
func planningReference(project storage.Project, number int64) string {
	return domain.PelletReference{ProjectCode: project.Code, Number: number}.String()
}

func readPlanningReceipts(ctx context.Context, q runQuery, project storage.Project, chatID int64) (map[string]planningReceipt, error) {
	rows, err := q.QueryContext(ctx, `SELECT draft_id,pellet_number,draft_json,pellet_json FROM planning_draft_creations WHERE project_id=? AND chat_id=? ORDER BY draft_id`, project.ID, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	receipts := map[string]planningReceipt{}
	for rows.Next() {
		var id, draftJSON, pelletJSON string
		var number int64
		if err := rows.Scan(&id, &number, &draftJSON, &pelletJSON); err != nil {
			return nil, err
		}
		var receipt planningReceipt
		if err := json.Unmarshal([]byte(draftJSON), &receipt.draft); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(pelletJSON), &receipt.pellet); err != nil {
			return nil, err
		}
		if receipt.draft.ID != id || receipt.pellet.ProjectID != project.ID || receipt.pellet.Reference.Number != number {
			return nil, storage.InvalidPlanningState("planning creation evidence is inconsistent")
		}
		receipt.draft.CreatedNumber = number
		receipt.draft.CreatedReference = planningReference(project, number)
		receipt.pellet.Reference.ProjectCode = project.Code
		receipts[id] = receipt
		if len(receipts) > storage.MaxPlanningDrafts {
			return nil, storage.InvalidPlanningState("planning creation evidence exceeds the chat limit")
		}
	}
	return receipts, rows.Err()
}

func readPlanningChat(ctx context.Context, q runQuery, project storage.Project, id int64) (chat storage.PlanningChat, revision int64, err error) {
	if id < 1 {
		return chat, 0, storage.PlanningChatNotFound(id)
	}
	var stateJSON, created, updated string
	err = q.QueryRowContext(ctx, `SELECT c.chat_id,c.project_id,p.code,c.revision,c.state_json,strftime('%Y-%m-%dT%H:%M:%fZ',c.created_at),strftime('%Y-%m-%dT%H:%M:%fZ',c.updated_at) FROM planning_chats c JOIN projects p ON p.project_id=c.project_id WHERE c.project_id=? AND c.chat_id=?`, project.ID, id).Scan(&chat.ID, &chat.ProjectID, &chat.ProjectCode, &revision, &stateJSON, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return chat, 0, storage.PlanningChatNotFound(id)
	}
	if err != nil {
		return chat, 0, err
	}
	if err = json.Unmarshal([]byte(stateJSON), &chat.State); err != nil {
		return chat, 0, err
	}
	chat.State, err = freezePlanningState(chat.State)
	if err != nil {
		return chat, 0, err
	}
	chat.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return chat, 0, err
	}
	chat.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return chat, 0, err
	}
	project.Code, chat.Version = chat.ProjectCode, planningVersion(id, revision)
	receipts, err := readPlanningReceipts(ctx, q, project, id)
	if err != nil {
		return chat, 0, err
	}
	if err = validatePlanningReceipts(&chat.State, receipts); err != nil {
		return chat, 0, err
	}
	return chat, revision, nil
}
func (reader *WebReader) ReadPlanningChat(ctx context.Context, project storage.Project, id int64) (storage.PlanningChat, error) {
	tx, err := reader.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return storage.PlanningChat{}, err
	}
	defer tx.Rollback()
	if err = ensureStoredProject(ctx, tx, project); err != nil {
		return storage.PlanningChat{}, err
	}
	chat, _, err := readPlanningChat(ctx, tx, project, id)
	if err != nil {
		return chat, err
	}
	return chat, tx.Commit()
}
func (reader *WebReader) LatestPlanningChat(ctx context.Context, project storage.Project) (*storage.PlanningChat, error) {
	tx, err := reader.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = ensureStoredProject(ctx, tx, project); err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, "SELECT chat_id FROM planning_chats WHERE project_id=? ORDER BY chat_id DESC LIMIT 1", project.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	chat, _, err := readPlanningChat(ctx, tx, project, id)
	if err != nil {
		return nil, err
	}
	return &chat, tx.Commit()
}

func (writer *WebWriter) CreatePlanningChat(ctx context.Context, project storage.Project, requestID string, state storage.PlanningState) (result storage.PlanningChat, err error) {
	if !storage.ValidPlanningID(requestID) {
		return result, storage.InvalidPlanningState("a stable planning request ID is required")
	}
	state, err = freezePlanningState(state)
	if err != nil {
		return result, err
	}
	if err = validatePlanningReceipts(&state, nil); err != nil {
		return result, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return result, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
	db := ProjectDatabase{db: writer.db}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		if err := ensureStoredProject(ctx, conn, project); err != nil {
			return err
		}
		var id int64
		var existing string
		err := conn.QueryRowContext(ctx, "SELECT chat_id,request_fingerprint FROM planning_chats WHERE project_id=? AND request_id=?", project.ID, requestID).Scan(&id, &existing)
		if err == nil {
			if existing != fingerprint {
				return storage.PlanningRequestConflict()
			}
			result, _, err = readPlanningChat(ctx, conn, project, id)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := validatePlanningWorkspace(ctx, conn, project, 0, state.WorkspaceID); err != nil {
			return err
		}
		now, err := captureJulianTimestamp(ctx, conn)
		if err != nil {
			return err
		}
		inserted, err := conn.ExecContext(ctx, "INSERT INTO planning_chats(project_id,request_id,request_fingerprint,revision,state_json,created_at,updated_at) VALUES(?,?,?,1,?,?,?)", project.ID, requestID, fingerprint, string(encoded), now, now)
		if err != nil {
			return err
		}
		id, err = inserted.LastInsertId()
		if err != nil {
			return err
		}
		result, _, err = readPlanningChat(ctx, conn, project, id)
		return err
	})
	return
}

// Zero is reserved for legacy chats that have not yet chosen a workspace.
// Once bound, a chat cannot be retargeted, including by omitting the field.
func validatePlanningWorkspace(ctx context.Context, conn *sql.Conn, project storage.Project, before, after int64) error {
	if before != 0 && before != after {
		return storage.InvalidPlanningState("this chat is bound to its workspace; start a new chat to use another workspace")
	}
	if after == 0 {
		return nil
	}
	var found int
	if err := conn.QueryRowContext(ctx, "SELECT 1 FROM project_workspaces WHERE project_id=? AND workspace_id=?", project.ID, after).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.InvalidPlanningState("the planning workspace does not belong to this project")
		}
		return err
	}
	return nil
}

// Created drafts are historical proposal receipts. A generic state save can
// change neither their content nor their stable pellet mapping, nor erase them.
func validatePlanningReceipts(state *storage.PlanningState, receipts map[string]planningReceipt) error {
	seen := map[string]bool{}
	for i := range state.Drafts {
		draft := &state.Drafts[i]
		if receipt, exists := receipts[draft.ID]; exists {
			content := func(d storage.PlanningDraft) storage.PlanningDraft {
				d.CreatedNumber = 0
				d.CreatedReference = ""
				d.Selected = false
				return d
			}
			if !reflect.DeepEqual(content(*draft), content(receipt.draft)) || draft.CreatedNumber != 0 && draft.CreatedNumber != receipt.draft.CreatedNumber {
				return storage.InvalidPlanningState("created planning drafts are immutable; edit the actual pellet instead")
			}
			draft.CreatedNumber, draft.CreatedReference = receipt.draft.CreatedNumber, receipt.draft.CreatedReference
			seen[draft.ID] = true
			if state.RefiningDraftID == draft.ID {
				return storage.InvalidPlanningState("refine an uncreated draft instead of a created pellet")
			}
		} else if draft.CreatedNumber != 0 || draft.CreatedReference != "" {
			return storage.InvalidPlanningState("only explicit planning creation can record a created pellet")
		}
	}
	if len(seen) != len(receipts) {
		return storage.InvalidPlanningState("created planning drafts must remain in their chat")
	}
	return storage.ValidatePlanningState(*state)
}
func savePlanningState(ctx context.Context, conn *sql.Conn, project storage.Project, chatID, revision int64, state storage.PlanningState) (storage.PlanningChat, error) {
	if err := storage.ValidatePlanningState(state); err != nil {
		return storage.PlanningChat{}, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return storage.PlanningChat{}, err
	}
	now, err := captureJulianTimestamp(ctx, conn)
	if err != nil {
		return storage.PlanningChat{}, err
	}
	updated, err := conn.ExecContext(ctx, "UPDATE planning_chats SET state_json=?,revision=revision+1,updated_at=MAX(updated_at,?) WHERE project_id=? AND chat_id=? AND revision=?", string(encoded), now, project.ID, chatID, revision)
	if err != nil {
		return storage.PlanningChat{}, err
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return storage.PlanningChat{}, err
		}
		return storage.PlanningChat{}, fmt.Errorf("planning chat revision changed inside its writer transaction")
	}
	chat, _, err := readPlanningChat(ctx, conn, project, chatID)
	return chat, err
}
func (writer *WebWriter) SavePlanningChat(ctx context.Context, project storage.Project, id int64, expectedVersion string, state storage.PlanningState) (result storage.PlanningChat, err error) {
	state, err = freezePlanningState(state)
	if err != nil {
		return result, err
	}
	db := ProjectDatabase{db: writer.db}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		if err := ensureStoredProject(ctx, conn, project); err != nil {
			return err
		}
		current, revision, err := readPlanningChat(ctx, conn, project, id)
		if err != nil {
			return err
		}
		project.Code = current.ProjectCode
		receipts, err := readPlanningReceipts(ctx, conn, project, id)
		if err != nil {
			return err
		}
		stateErr := validatePlanningWorkspace(ctx, conn, project, current.State.WorkspaceID, state.WorkspaceID)
		if stateErr == nil {
			stateErr = validatePlanningReceipts(&state, receipts)
		}
		if stateErr == nil && (len(state.Messages) < len(current.State.Messages) || !reflect.DeepEqual(state.Messages[:len(current.State.Messages)], current.State.Messages)) {
			stateErr = storage.InvalidPlanningState("saved planning messages are immutable; start a new chat to begin another conversation")
		}
		if stateErr == nil && expectedVersion != "" && reflect.DeepEqual(state, current.State) {
			// Replaying a response lost after commit must not manufacture a
			// conflict or another revision. Different content still requires CAS.
			result = current
			return nil
		}
		if expectedVersion == "" || expectedVersion != current.Version {
			return storage.PlanningConflict(current)
		}
		if stateErr != nil {
			return stateErr
		}
		result, err = savePlanningState(ctx, conn, project, id, revision, state)
		return err
	})
	return
}

func (writer *WebWriter) CreatePlanningPellets(ctx context.Context, project storage.Project, id int64, expectedVersion string, draftIDs []string) (result storage.PlanningCreation, err error) {
	result.Pellets = []storage.Pellet{}
	if expectedVersion == "" || len(draftIDs) == 0 || len(draftIDs) > storage.MaxPlanningDrafts {
		return result, storage.InvalidPlanningState("choose a bounded set of draft IDs and an exact chat version")
	}
	wanted := map[string]bool{}
	for _, draftID := range draftIDs {
		if !storage.ValidPlanningID(draftID) || wanted[draftID] {
			return result, storage.InvalidPlanningState("selected draft IDs must be valid and unique")
		}
		wanted[draftID] = true
	}
	db := ProjectDatabase{db: writer.db}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		if err := ensureStoredProject(ctx, conn, project); err != nil {
			return err
		}
		current, revision, err := readPlanningChat(ctx, conn, project, id)
		if err != nil {
			return err
		}
		project.Code = current.ProjectCode
		receipts, err := readPlanningReceipts(ctx, conn, project, id)
		if err != nil {
			return err
		}
		allCreated := true
		for draftID := range wanted {
			if _, exists := receipts[draftID]; !exists {
				allCreated = false
			}
		}
		// A response lost after commit can be replayed forever, even after later
		// chat edits or pellet purge. Partial stale requests must still conflict.
		if allCreated {
			for _, draft := range current.State.Drafts {
				if wanted[draft.ID] {
					result.Pellets = append(result.Pellets, receipts[draft.ID].pellet)
				}
			}
			result.Chat = current
			return nil
		}
		if expectedVersion != current.Version {
			return storage.PlanningConflict(current)
		}
		selected := []int{}
		for i, draft := range current.State.Drafts {
			if wanted[draft.ID] {
				selected = append(selected, i)
			}
		}
		if len(selected) != len(wanted) {
			return storage.InvalidPlanningState("a selected planning draft is no longer in this chat")
		}
		// Validate the complete batch before allocating any queue number or rank.
		inputs := map[string]storage.NewPellet{}
		for _, i := range selected {
			draft := current.State.Drafts[i]
			if _, done := receipts[draft.ID]; done {
				continue
			}
			description := draft.Description
			if strings.TrimSpace(draft.Acceptance) != "" {
				if description != "" {
					description += "\n\n"
				}
				description += "Acceptance criteria\n" + draft.Acceptance
			}
			if strings.TrimSpace(description) == "" {
				return storage.InvalidPlanningState("selected drafts require a description or acceptance criteria before creation")
			}
			var group *string
			if draft.Group != "" {
				value := draft.Group
				group = &value
			}
			input, err := validateNewPellet(storage.NewPellet{Kind: domain.PelletOrdinary, Model: draft.Model, ReasoningEffort: draft.ReasoningEffort, Title: draft.Title, Description: description, Group: group, Status: domain.PelletOpen})
			if err != nil {
				return err
			}
			inputs[draft.ID] = input
		}
		actualProject, err := loadProject(ctx, conn, project.ID)
		if err != nil {
			return err
		}
		selectedProject, err := resolvedForScalarWrite(actualProject)
		if err != nil {
			return err
		}
		for _, i := range selected {
			draft := &current.State.Drafts[i]
			if receipt, done := receipts[draft.ID]; done {
				result.Pellets = append(result.Pellets, receipt.pellet)
				continue
			}
			pellet, err := createPelletInTransaction(ctx, conn, selectedProject, inputs[draft.ID])
			if err != nil {
				return err
			}
			draft.CreatedNumber, draft.CreatedReference, draft.Selected = pellet.Reference.Number, pellet.Reference.String(), false
			draftJSON, err := json.Marshal(draft)
			if err != nil {
				return err
			}
			pelletJSON, err := json.Marshal(pellet)
			if err != nil {
				return err
			}
			if _, err = conn.ExecContext(ctx, "INSERT INTO planning_draft_creations(chat_id,project_id,draft_id,pellet_number,draft_json,pellet_json) VALUES(?,?,?,?,?,?)", id, project.ID, draft.ID, pellet.Reference.Number, string(draftJSON), string(pelletJSON)); err != nil {
				return err
			}
			result.Pellets = append(result.Pellets, pellet)
		}
		current.State.RefiningDraftID = ""
		result.Chat, err = savePlanningState(ctx, conn, project, id, revision, current.State)
		return err
	})
	if err != nil {
		result = storage.PlanningCreation{Pellets: []storage.Pellet{}}
	}
	return
}
