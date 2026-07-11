package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ssePingInterval keeps the stream and any intermediary connections alive during
// quiet periods.
const ssePingInterval = 25 * time.Second

// sse streams the live event feed as Server-Sent Events. Each recorded event is
// sent as one JSON `data:` frame (the display projection, matching the overview
// table). The stream stays open until the client disconnects.
func (s *Service) sse(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Events == nil {
		http.Error(w, "live feed unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsubscribe := s.cfg.Events.Subscribe()
	defer unsubscribe()

	ctx := r.Context()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(ssePingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case e, open := <-ch:
			if !open {
				return
			}
			payload, err := json.Marshal(toEventRow(e))
			if err != nil {
				s.logger.Warn("console: sse marshal", "error", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}
