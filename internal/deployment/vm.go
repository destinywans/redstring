package deployment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/wans/aiops-canvas/internal/delivery"
	"io"
	"os/exec"
	"time"
)

type vmAdapter struct{ service *Service }
type helperResponse struct {
	Status          string `json:"status"`
	Revision        string `json:"revision"`
	ActiveOperation string `json:"active_operation"`
	Observed        struct {
		Revision string `json:"revision"`
		Identity string `json:"identity"`
		Active   bool   `json:"active"`
	} `json:"observed"`
}

func (a *vmAdapter) call(ctx context.Context, t Target, body any) (helperResponse, error) {
	raw, e := json.Marshal(body)
	if e != nil {
		return helperResponse{}, e
	}
	v := t.VM
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes", "-o", "ForwardAgent=no", "-o", "ConnectTimeout=10", "-o", "UserKnownHostsFile="+v.KnownHostsFile, "-i", v.PrivateKeyFile, "-p", fmt.Sprint(v.Port), v.User+"@"+v.Host, "sudo -n /usr/local/sbin/aiops-deploy-helper")
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stderr = io.Discard
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return helperResponse{}, e
	}
	if e = cmd.Start(); e != nil {
		return helperResponse{}, e
	}
	result, readErr := io.ReadAll(io.LimitReader(stdout, 1<<20))
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil || len(result) >= 1<<20 {
		return helperResponse{}, ErrUnknown
	}
	var out helperResponse
	e = json.Unmarshal(result, &out)
	return out, e
}
func (a *vmAdapter) Prepare(ctx context.Context, t Target, source delivery.VerifiedSource, id string) (Prepared, error) {
	out, e := a.call(ctx, t, map[string]string{"action": "inspect", "target_id": t.VM.RemoteTargetID})
	if e != nil {
		return Prepared{}, e
	}
	if out.Status != "ready" || out.ActiveOperation != "" || !sha.MatchString(out.Revision) {
		return Prepared{}, ErrDrift
	}
	return Prepared{ExpectedRevision: out.Revision}, nil
}
func (a *vmAdapter) Check(ctx context.Context, t Target, p Plan) error {
	out, e := a.call(ctx, t, map[string]string{"action": "inspect", "target_id": t.VM.RemoteTargetID})
	if e != nil {
		return e
	}
	if out.Status != "ready" || out.ActiveOperation != "" || out.Revision != p.Prepared.ExpectedRevision {
		return ErrDrift
	}
	return nil
}
func (a *vmAdapter) Apply(ctx context.Context, t Target, p Plan, operation string) (Result, error) {
	body := map[string]string{"action": "apply", "target_id": t.VM.RemoteTargetID, "operation_id": operation, "plan_digest": p.Digest, "artifact_digest": p.Source.ArtifactDigest, "expected_revision": p.Prepared.ExpectedRevision}
	if t.Kind == "systemd" {
		if a.service.ResolveArtifactURL == nil {
			return Result{}, ErrInvalid
		}
		resolved, e := a.service.ResolveArtifactURL(ctx, p.Source.ArtifactURL)
		if e != nil {
			return Result{}, e
		}
		body["artifact_url"] = resolved
	}
	out, e := a.call(ctx, t, body)
	if e != nil {
		return Result{}, e
	}
	return helperResult(out), nil
}
func (a *vmAdapter) Reconcile(ctx context.Context, t Target, p Plan, operation string) (Result, error) {
	out, e := a.call(ctx, t, map[string]string{"action": "status", "target_id": t.VM.RemoteTargetID, "operation_id": operation})
	if e != nil {
		return Result{}, e
	}
	return helperResult(out), nil
}
func helperResult(out helperResponse) Result {
	status := out.Status
	switch status {
	case "succeeded", "running", "not_found":
	default:
		status = "needs_attention"
	}
	return Result{Status: status, Revision: out.Observed.Revision, ObservedAt: time.Now().UTC()}
}
