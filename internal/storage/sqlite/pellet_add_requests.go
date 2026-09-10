package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

// SQLite timestamps are Julian days. Retries do not extend the two-day window.
func prunePelletAddRequests(ctx context.Context, query projectQuery, now float64) error {
	if _, err := query.ExecContext(ctx, "DELETE FROM pellet_add_requests WHERE created_at < ?", now-2); err != nil {
		return pelletStorageError("expire pellet add requests", err)
	}
	return nil
}

func pelletAddFingerprint(ctx context.Context, query projectQuery, project storage.ResolvedProject, input storage.NewPellet) (string, error) {
	if input.RequestID == nil {
		return "", nil
	}
	// Hash resolved description content, normalized initial status, and stable
	// placement identity. Filename, flag order, workspace, and project aliases
	// are not part of the creation intent.
	input.RequestID = nil
	if input.ReviewTargets != nil {
		input.ReviewTargets = append([]domain.PelletReference{}, input.ReviewTargets...)
		for i, ref := range input.ReviewTargets {
			if err := ensureReferenceProject(ctx, query, project.Project, ref); err != nil {
				return "", err
			}
			input.ReviewTargets[i].ProjectCode = ""
		}
	}
	if input.Placement != nil {
		if err := ensureReferenceProject(ctx, query, project.Project, input.Placement.Target); err != nil {
			return "", err
		}
		placement := *input.Placement
		placement.Target.ProjectCode = ""
		input.Placement = &placement
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", pelletStorageError("encode pellet add inputs", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

type pelletAddReceipt struct {
	Version int            `json:"version"`
	Pellet  storage.Pellet `json:"pellet"`
}

func replayPelletAdd(ctx context.Context, query projectQuery, project storage.ResolvedProject, requestID, fingerprint string) (storage.Pellet, bool, error) {
	var storedFingerprint, result string
	err := query.QueryRowContext(ctx, `SELECT fingerprint, result_json FROM pellet_add_requests WHERE project_id = ? AND request_id = ?`, project.Project.ID, requestID).Scan(&storedFingerprint, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Pellet{}, false, nil
	}
	if err != nil {
		return storage.Pellet{}, false, pelletStorageError("read pellet add request", err)
	}
	if storedFingerprint != fingerprint {
		return storage.Pellet{}, true, domain.NewError(domain.Conflict, "request_id_conflict", "the request ID was already used with different add inputs", map[string]any{"project": project.Project.Code, "request_id": requestID})
	}
	var receipt pelletAddReceipt
	if err := json.Unmarshal([]byte(result), &receipt); err != nil {
		return storage.Pellet{}, true, pelletStorageError("decode pellet add receipt", err)
	}
	if receipt.Version != 1 || receipt.Pellet.ProjectID != project.Project.ID || receipt.Pellet.Reference.Number <= 0 {
		return storage.Pellet{}, true, pelletStorageError("decode pellet add receipt", errors.New("invalid pellet add receipt"))
	}
	// Creation results are snapshots, but public references always use the
	// current canonical code even if the project has since been renamed.
	receipt.Pellet.Reference.ProjectCode = project.Project.Code
	if receipt.Pellet.Kind == "" {
		receipt.Pellet.Kind = domain.PelletOrdinary
	}
	if receipt.Pellet.Checkpoint != nil {
		for i := range receipt.Pellet.Checkpoint.Targets {
			receipt.Pellet.Checkpoint.Targets[i].Reference = domain.PelletReference{ProjectCode: project.Project.Code, Number: receipt.Pellet.Checkpoint.Targets[i].Number}.String()
		}
	}
	return receipt.Pellet, true, nil
}

func recordPelletAdd(ctx context.Context, query projectQuery, projectID int64, requestID, fingerprint string, pellet storage.Pellet, now float64) error {
	result, err := json.Marshal(pelletAddReceipt{Version: 1, Pellet: pellet})
	if err != nil {
		return pelletStorageError("encode pellet add receipt", err)
	}
	if _, err := query.ExecContext(ctx, `INSERT INTO pellet_add_requests(project_id, request_id, fingerprint, result_json, created_at) VALUES (?, ?, ?, ?, ?)`, projectID, requestID, fingerprint, string(result), now); err != nil {
		return pelletStorageError("record pellet add request", err)
	}
	return nil
}
