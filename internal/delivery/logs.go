package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strings"
)

// Logs reads an independent archive for completed Pods, including after Pod
// deletion. The recorded workflow identity, registered bucket and bounded key
// scope are required; client input can never select an arbitrary S3 object.
func (s *Service) Logs(ctx context.Context, run Run, nodeID string, cursor int64) (LogChunk, error) {
	if run.Provider != "native" || s.Argo == nil || cursor < 0 {
		return LogChunk{}, ErrInvalid
	}
	projection := run.Projection
	if current, err := s.Argo.Observe(ctx, run); err == nil {
		projection = current
	}
	if object, ok := s.Artifacts.(*S3Store); ok && (terminal(projection.Status) || nodeID != "") {
		var nodes []NodeResult
		for _, n := range projection.Nodes {
			if nodeID == "" || n.ID == nodeID || n.Name == nodeID {
				nodes = append(nodes, n)
			}
		}
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
		var text strings.Builder
		found, complete := false, true
		for _, n := range nodes {
			for _, a := range n.Artifacts {
				if a.Name != "main-logs" || a.Bucket != object.config.Bucket || !strings.HasPrefix(a.Key, "aiops-"+run.ID+"/") {
					continue
				}
				data, err := object.readBounded(ctx, a.Key, 16<<20)
				if err != nil {
					complete = false
					continue
				}
				found = true
				if nodeID == "" {
					text.WriteString("[" + n.Name + "]\n")
				}
				text.Write(data)
				if text.Len() > 16<<20 {
					return LogChunk{}, ErrInvalid
				}
			}
		}
		if found && complete {
			return chunkLogs(text.String(), cursor, "s3_archive"), nil
		}
	}
	live, err := s.Argo.Logs(ctx, run, nodeID)
	if err != nil {
		return LogChunk{}, err
	}
	return chunkLogs(live.Text, cursor, live.Source), nil
}
func chunkLogs(text string, cursor int64, source string) LogChunk {
	if cursor > int64(len(text)) {
		cursor = int64(len(text))
	}
	end := cursor + (64 << 10)
	if end > int64(len(text)) {
		end = int64(len(text))
	}
	return LogChunk{Text: text[cursor:end], NextCursor: end, Truncated: end < int64(len(text)), Source: source}
}
func (s *S3Store) readBounded(ctx context.Context, key string, limit int64) ([]byte, error) {
	empty := sha256.Sum256(nil)
	response, err := s.request(ctx, "GET", key, hex.EncodeToString(empty[:]), nil, 0)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, ErrInvalid
	}
	return data, err
}
