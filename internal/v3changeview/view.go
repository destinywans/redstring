package v3changeview

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func nowOf(s *Service) time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) view(w http.ResponseWriter, r *http.Request) {
	f, ok := parseRequest(r.URL.RawQuery)
	if !ok {
		httpapi.Error(w, 422, "invalid_change_request", "Select one service and delivery run")
		return
	}
	app, env, _ := decodeServiceID(f.ServiceID)
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	row, err := s.scope(r, app, env)
	if err != nil {
		writeReadError(w, err)
		return
	}
	user := httpapi.CurrentUser(r)
	allowed, err := s.canView(ctx, user, row, "services")
	if err != nil {
		writeReadError(w, err)
		return
	}
	if !allowed {
		writeReadError(w, pgx.ErrNoRows)
		return
	}
	allowed, err = s.canView(ctx, user, row, "events")
	if err != nil {
		writeReadError(w, err)
		return
	}
	if !allowed {
		writeReadError(w, store.ErrForbidden)
		return
	}
	end := nowOf(s)
	start := end.Add(-f.Window)
	runs, err := s.loadRuns(ctx, row, start, end)
	if err != nil {
		writeReadError(w, err)
		return
	}
	selected, err := findRun(runs, f.RunID)
	if err != nil {
		writeReadError(w, err)
		return
	}
	if selected.Run.Status != "succeeded" {
		writeReadError(w, errIncomplete)
		return
	}
	for _, item := range runs {
		if _, e := projectRun(item.Run, row.Name, item.Actor); e != nil {
			writeReadError(w, e)
			return
		}
	}
	deployments, err := s.loadDeployments(ctx, row, start, end, runs)
	if err != nil {
		writeReadError(w, err)
		return
	}
	alerts, err := s.loadAlerts(ctx, row, start, end)
	if err != nil {
		writeReadError(w, err)
		return
	}
	review, err := s.Reviews.Read(ctx, row.AppID, row.EnvID, selected.Run.ID, selected.Run.Snapshot.Digest)
	if err != nil {
		writeReadError(w, sourceErr(err))
		return
	}
	if err = validateReview(review, selected.Run.ID, row.EnvID, selected.Run.Snapshot.Digest); err != nil {
		writeReadError(w, err)
		return
	}
	deployment, err := selectedDeployment(deployments, selected.Run.ID, review.ExecutionID)
	if err != nil {
		writeReadError(w, err)
		return
	}
	alert, err := findAlert(alerts, review.IncidentID)
	if err != nil || review.ArtifactDigest != deployment.ArtifactDigest || review.CommitSHA != deployment.CommitSHA ||
		review.PlanDigest != deployment.PlanDigest || !review.RunUpdatedAt.Equal(selected.Run.UpdatedAt) ||
		!review.IncidentUpdatedAt.Equal(alert.UpdatedAt) ||
		alert.StartsAt.Before(deployment.At) || alert.StartsAt.After(deployment.At.Add(2*time.Hour)) ||
		review.ReviewedAt.Before(alert.StartsAt) {
		writeReadError(w, errIncomplete)
		return
	}
	out, err := buildView(row, selected, runs, deployments, alerts, review, start, end)
	if err != nil {
		writeReadError(w, err)
		return
	}
	if ctx.Err() != nil {
		writeReadError(w, ctx.Err())
		return
	}
	finalRuns, err := s.loadRuns(ctx, row, start, end)
	if err != nil || !sameRunPopulation(runs, finalRuns) {
		writeReadError(w, errIncomplete)
		return
	}
	finalDeployments, err := s.loadDeployments(ctx, row, start, end, finalRuns)
	if err != nil || !sameDeployments(deployments, finalDeployments) {
		writeReadError(w, errIncomplete)
		return
	}
	finalAlerts, err := s.loadAlerts(ctx, row, start, end)
	if err != nil || !sameAlerts(alerts, finalAlerts) {
		writeReadError(w, errIncomplete)
		return
	}
	finalReview, err := s.Reviews.Read(ctx, row.AppID, row.EnvID, selected.Run.ID, selected.Run.Snapshot.Digest)
	if err != nil || finalReview != review {
		writeReadError(w, errIncomplete)
		return
	}
	if err = s.finalCheck(ctx, r, row); err != nil {
		writeReadError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}

func buildView(row serviceRow, selected runRecord, runs []runRecord, deployments []deploymentRecord,
	alerts []alertRecord, review ReviewEvidence, start, end time.Time) (viewResponse, error) {
	var out viewResponse
	pipe, err := projectRun(selected.Run, row.Name, selected.Actor)
	if err != nil {
		return out, err
	}
	stages, err := projectStages(selected.Run)
	if err != nil {
		return out, err
	}
	out.Pipelines = []pipeline{pipe}
	out.Detail = detail{ID: pipe.ID, Title: pipe.Title, Brand: pipe.Brand,
		Meta:   "Reviewed run " + pipe.Ref + " · audit #" + review.ID + " · " + review.ReviewedAt.UTC().Format(time.RFC3339),
		Stages: stages}
	out.Detail.Diff.Path, out.Detail.Diff.Minus, out.Detail.Diff.Plus = review.DiffPath, review.DiffMinus, review.DiffPlus
	out.Detail.Risk.Title, out.Detail.Risk.Meta, out.Detail.Risk.Text = review.RiskTitle, review.RiskMeta, review.RiskText
	out.Detail.Alert.Text, out.Detail.Alert.Incident = review.AlertText, review.IncidentID
	out.Sources = []sourceCount{{Brand: "argo", Name: "Argo Workflows (local delivery)", Count: len(runs), On: true}}
	out.Lanes = []lane{{Service: row.Name, Marks: []mark{}, Alerts: [][2]float64{}}}
	out.Axis = make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		out.Axis = append(out.Axis, start.Add(time.Duration(i)*end.Sub(start)/4).UTC().Format("Jan 02 15:04 UTC"))
	}
	runSeries, successSeries, failedSeries, deploySeries := make([]float64, 7), make([]float64, 7), make([]float64, 7), make([]float64, 7)
	success, failed := 0, 0
	for _, item := range runs {
		r := item.Run
		bin := windowBin(r.CreatedAt, start, end)
		if bin < 0 {
			return viewResponse{}, errIncomplete
		}
		runSeries[bin]++
		switch r.Status {
		case "succeeded":
			success++
			successSeries[bin]++
		case "failed":
			failed++
			failedSeries[bin]++
			if r.UpdatedAt.Before(end) {
				out.Lanes[0].Marks = append(out.Lanes[0].Marks, mark{At: windowPercent(r.UpdatedAt, start, end), Tone: "danger", Label: "Failed run " + r.ID})
			}
		case "running", "cancelled":
			// No tone in the frozen timeline represents these states. They remain
			// source-backed in counts but do not become a success/failure mark.
		default:
			return viewResponse{}, errIncomplete
		}
	}
	for _, d := range deployments {
		bin := windowBin(d.At, start, end)
		if bin < 0 {
			return viewResponse{}, errIncomplete
		}
		deploySeries[bin]++
		out.Lanes[0].Marks = append(out.Lanes[0].Marks,
			mark{At: windowPercent(d.At, start, end), Tone: "ok", Label: "Verified GitOps deployment " + d.ExecutionID})
	}
	for _, alert := range alerts {
		left := alert.StartsAt
		if left.Before(start) {
			left = start
		}
		right := end
		if alert.EndsAt != nil && alert.EndsAt.Before(right) {
			right = *alert.EndsAt
		}
		if right.Before(left) {
			return viewResponse{}, errIncomplete
		}
		out.Lanes[0].Alerts = append(out.Lanes[0].Alerts, [2]float64{windowPercent(left, start, end), windowPercent(right, start, end)})
	}
	sort.Slice(out.Lanes[0].Marks, func(i, j int) bool { return out.Lanes[0].Marks[i].At < out.Lanes[0].Marks[j].At })
	out.KPIs = []kpi{
		{ID: "runs", Label: "Recorded delivery runs", Value: strconv.Itoa(len(runs)), Series: runSeries},
		{ID: "succeeded", Label: "Succeeded runs", Value: strconv.Itoa(success), Tone: "ok", Series: successSeries},
		{ID: "failed", Label: "Failed runs", Value: strconv.Itoa(failed), Tone: "danger", Series: failedSeries},
		{ID: "deployments", Label: "Verified GitOps deployments", Value: strconv.Itoa(len(deployments)), Series: deploySeries},
	}
	return out, nil
}

func windowBin(at, start, end time.Time) int {
	if at.Before(start) || !at.Before(end) {
		return -1
	}
	i := int(float64(at.Sub(start)) / float64(end.Sub(start)) * 7)
	if i < 0 || i > 6 {
		return -1
	}
	return i
}

func windowPercent(at, start, end time.Time) float64 {
	if at.Before(start) {
		return 0
	}
	if at.After(end) {
		return 100
	}
	return float64(at.Sub(start)) / float64(end.Sub(start)) * 100
}
