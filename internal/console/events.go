package console

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/store"
)

// kvPair is one scalar payload field, rendered as a key/value row.
type kvPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// eventItem is a memory an event surfaced or touched, resolved from an item id to
// its current index entry. Missing is true when the id no longer resolves (the
// memory was deleted or the id predates the index).
type eventItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Project     string `json:"project,omitempty"`
	Missing     bool   `json:"missing,omitempty"`
}

// eventDetailData is the payload for a single event's detail page.
type eventDetailData struct {
	Event   eventRow    `json:"event"`
	Content string      `json:"content,omitempty"` // verbatim injected/large text, if any
	Items   []eventItem `json:"items,omitempty"`   // resolved memories the event referenced
	Fields  []kvPair    `json:"fields,omitempty"`  // remaining scalar payload fields
	RawJSON string      `json:"rawJson"`           // pretty-printed full payload
}

// eventDetail renders one event-log entry in full: the verbatim injected content,
// any memories it surfaced (resolved to live index entries), the remaining
// payload fields, and the raw JSON. This is what a Recent-activity row links to.
func (s *Service) eventDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.cfg.Events == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	ev, ok, err := s.cfg.Events.ByID(ctx, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	data := eventDetailData{
		Event:   toEventRow(ev),
		Content: payloadStr(ev.Payload, "content"),
		RawJSON: prettyPayload(ev.Payload),
	}
	if items, err := s.resolveEventItems(ctx, ev); err != nil {
		s.serverError(w, r, err)
		return
	} else {
		data.Items = items
	}
	data.Fields = scalarFields(ev.Payload, "content", "item_ids")

	if r.URL.Query().Get("peek") == "1" {
		s.renderFragment(w, r, "event", data)
		return
	}
	s.render(w, r, "event", pageData{Title: "Event " + shortID(ev.ID), Active: "overview", Data: data})
}

// resolveEventItems turns the memory ids an event referenced (ItemID plus any
// payload item_ids) into display rows, looking each up in the memory index.
// Order follows first appearance; unresolved ids are kept and marked Missing.
func (s *Service) resolveEventItems(ctx context.Context, ev core.Event) ([]eventItem, error) {
	ids := injectedEventItemIDs(ev)
	if len(ids) == 0 {
		return nil, nil
	}
	byID, err := store.MemoriesByIDs(ctx, s.cfg.DB, ids)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]eventItem, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if m, ok := byID[id]; ok {
			out = append(out, eventItem{
				ID: m.ID, Name: m.Name, Kind: string(m.Kind),
				Description: m.Description, Project: m.Project,
			})
			continue
		}
		out = append(out, eventItem{ID: id, Missing: true})
	}
	return out, nil
}

// scalarFields projects a payload's remaining fields into sorted key/value rows,
// skipping the named keys (handled elsewhere) and empty values. Non-string values
// are compact-JSON encoded so nested data still shows.
func scalarFields(p map[string]any, skip ...string) []kvPair {
	if len(p) == 0 {
		return nil
	}
	skipped := make(map[string]struct{}, len(skip))
	for _, k := range skip {
		skipped[k] = struct{}{}
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		if _, ok := skipped[k]; ok {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kvPair, 0, len(keys))
	for _, k := range keys {
		v := stringifyValue(p[k])
		if v == "" {
			continue
		}
		out = append(out, kvPair{Key: k, Value: v})
	}
	return out
}

// stringifyValue renders a payload value for a key/value row: strings verbatim,
// everything else as compact JSON.
func stringifyValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// prettyPayload renders a payload as indented JSON for the raw disclosure, or ""
// when there is nothing to show.
func prettyPayload(p map[string]any) string {
	if len(p) == 0 {
		return ""
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}
