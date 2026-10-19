package v3canvas

import (
	"context"
	"errors"

	"github.com/destinywans/redstring/internal/core"
	"github.com/jackc/pgx/v5"
)

// CreateBlankIncidentInvestigation writes the same blank investigation canvas
// as the regular V3 creator in the caller's transaction. The caller must hold
// the current application/environment mapping and session locks and authorize
// both canvas.view and canvas.change against that mapping before calling it.
// It does not run a model or infer alert, metric, or change evidence.
func CreateBlankIncidentInvestigation(ctx context.Context, tx pgx.Tx, user core.User, environmentKey, applicationID, environmentRow, title string) (string, error) {
	if tx == nil || user.ID == "" || environmentRow == "" || applicationID == "" || title == "" || !canvasEnvironment(environmentKey) {
		return "", errors.New("invalid incident canvas scope")
	}
	s := &Service{}
	id, failure := s.createInvestigationCanvas(ctx, tx, user, environmentKey,
		createRequest{Title: title, Kind: "investigation", Services: []string{applicationID}},
		mappedService{environmentRow: environmentRow}, nil, "")
	if failure != nil {
		return "", errors.New(failure.text)
	}
	return id, nil
}
