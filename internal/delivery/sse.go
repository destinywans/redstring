package delivery

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

func (s *Service) registerEvents(api *httpapi.Server) {
	api.Register("GET /api/v1/delivery/runs/{id}/stream", "viewer", func(w http.ResponseWriter, r *http.Request) {
		run, err := s.Get(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		after, err := intQuery(r, "after", 0)
		if raw := r.Header.Get("Last-Event-ID"); raw != "" {
			after, err = strconv.ParseInt(raw, 10, 64)
		}
		if err != nil || after < 0 {
			respond(w, nil, ErrInvalid)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			respond(w, nil, ErrUnsupported)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(w)
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		w.WriteHeader(200)
		flusher.Flush()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if err = s.access(r.Context(), httpapi.CurrentUser(r).ID, run.ApplicationID, "view"); err != nil {
				return
			}
			events, e := s.Repository.Events(r.Context(), run.ID, after)
			if e != nil {
				return
			}
			for _, event := range events {
				raw, _ := json.Marshal(event)
				if _, e = fmt.Fprintf(w, "id: %d\nevent: delivery\ndata: %s\n\n", event.Sequence, raw); e != nil {
					return
				}
				after = event.Sequence
			}
			if len(events) == 0 {
				if _, e = fmt.Fprint(w, ": heartbeat\n\n"); e != nil {
					return
				}
			}
			flusher.Flush()
			if len(events) == 200 {
				continue
			}
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
			}
		}
	})
}
