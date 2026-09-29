package v3knowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/store"
)

// Drafts are deterministic outlines of bounded, current source data. The
// source text is never interpreted as an instruction to the server or an
// executable command. Every generated step still needs human review.
const maxDraftSourceBytes = 64 << 10

var errSourceTooLarge = errors.New("source exceeds Runbook draft limit")

func (s *Service) loadDraftSource(r *http.Request, tx pgx.Tx, in createInput) (sourceBinding, error) {
	binding, err := s.validateSource(r, tx, in)
	if err != nil {
		return binding, err
	}
	binding.SourceID = in.From
	switch in.Source {
	case "blank":
		return binding, nil
	case "template":
		for _, template := range knowledge.Templates() {
			if template.ID == in.From && template.ID != "blank" {
				binding.Title = template.Title
				binding.Outline = template.Body
				binding.ContentHash = store.Hash(template.Body)
				binding.Steps = []Step{}
				return binding, nil
			}
		}
		return binding, store.ErrNotFound
	case "postmortem":
		var rawSections, rawActions []byte
		var incidentID string
		var validSize bool
		err = tx.QueryRow(r.Context(), `SELECT pm.id,pm.incident_id,
  CASE WHEN octet_length(pm.sections::text)+octet_length(pm.actions::text)<=$2
    THEN pm.sections ELSE '[]'::jsonb END,
  CASE WHEN octet_length(pm.sections::text)+octet_length(pm.actions::text)<=$2
    THEN pm.actions ELSE '[]'::jsonb END,
  octet_length(pm.sections::text)+octet_length(pm.actions::text)<=$2
 FROM v3_postmortems pm
 JOIN ops_incidents i ON i.id=pm.incident_id AND i.application_id=pm.application_id
  AND i.investigation_id=pm.investigation_id AND i.status='resolved'
 JOIN application_environments app_env ON app_env.id=i.environment_id
  AND app_env.application_id=pm.application_id AND app_env.key=pm.environment_id AND NOT app_env.archived
 JOIN investigations inv ON inv.id=pm.investigation_id AND inv.application_id=pm.application_id AND inv.status='closed'
 JOIN investigation_reports report ON report.investigation_id=inv.id
 JOIN events closed ON closed.seq=pm.close_event_seq AND closed.investigation_id=inv.id AND closed.type='investigation.closed'
 WHERE (pm.id=$1 OR pm.incident_id=$1) AND pm.status='published'
 AND pm.application_id=$3 AND pm.environment_id=$4
 ORDER BY CASE WHEN pm.id=$1 THEN 0 ELSE 1 END LIMIT 1
 FOR SHARE OF pm,i,app_env,inv,report,closed`, in.From, maxDraftSourceBytes, binding.ApplicationID, scopeIDs(r)[0]).
			Scan(&binding.SourceID, &incidentID, &rawSections, &rawActions, &validSize)
		if err != nil {
			return binding, err
		}
		if !validSize {
			return binding, errSourceTooLarge
		}
		var sections []struct{ Key, Title, Text string }
		var actions []struct{ Text, Owner, Due string }
		if err = json.Unmarshal(rawSections, &sections); err != nil {
			return binding, err
		}
		if err = json.Unmarshal(rawActions, &actions); err != nil {
			return binding, err
		}
		if len(sections)+len(actions) > 100 {
			return binding, errSourceTooLarge
		}
		binding.Title = "Runbook from postmortem " + incidentID
		binding.Outline = "Published postmortem sections and actions are unverified source notes. Review every step before use."
		for _, section := range sections {
			if !validSourceText(section.Title, 200) || !validSourceBody(section.Text) {
				return binding, errSourceTooLarge
			}
			title := "Review: " + firstRunes(section.Title, 190)
			body := section.Text
			if len([]rune(section.Title)) > 190 {
				body = "Source section: " + section.Title + "\n\n" + body
				if !validSourceBody(body) {
					return binding, errSourceTooLarge
				}
			}
			binding.Steps = append(binding.Steps, Step{Kind: "check", Title: title, Body: body, Readonly: true})
		}
		for _, action := range actions {
			if !validSourceText(action.Text, 2000) {
				return binding, errSourceTooLarge
			}
			title := firstRunes(action.Text, 200)
			body := ""
			if len([]rune(action.Text)) > 200 {
				body = action.Text
				if !validSourceBody(body) {
					return binding, errSourceTooLarge
				}
			}
			binding.Steps = append(binding.Steps, Step{Kind: "action", Title: title, Body: body, Approval: true})
		}
		binding.ContentHash = store.Hash(string(rawSections) + "\n" + string(rawActions))
	case "conversation":
		var title string
		var count, size int64
		err = tx.QueryRow(r.Context(), `SELECT c.title,
  (SELECT count(*) FROM v3_conversation_messages m WHERE m.conversation_id=c.id AND m.state='completed'),
  (SELECT COALESCE(sum(octet_length(m.parts::text)),0) FROM v3_conversation_messages m WHERE m.conversation_id=c.id AND m.state='completed')
 FROM v3_conversations c WHERE c.id=$1 FOR SHARE OF c`, in.From).
			Scan(&title, &count, &size)
		if err != nil {
			return binding, err
		}
		if count > 40 || size > maxDraftSourceBytes {
			return binding, errSourceTooLarge
		}
		rows, queryErr := tx.Query(r.Context(), `SELECT id,sort_order,role,
 CASE WHEN octet_length(parts::text)<=$2 THEN parts ELSE '[]'::jsonb END,
 octet_length(parts::text)<=$2
 FROM v3_conversation_messages
 WHERE conversation_id=$1 AND state='completed' ORDER BY sort_order,id LIMIT 41 FOR SHARE`, in.From, maxDraftSourceBytes)
		if queryErr != nil {
			return binding, queryErr
		}
		var transcript strings.Builder
		messageCount := 0
		for rows.Next() {
			var messageID string
			var sortOrder int64
			var role string
			var raw []byte
			var validSize bool
			if err = rows.Scan(&messageID, &sortOrder, &role, &raw, &validSize); err != nil {
				break
			}
			messageCount++
			if !validSize || messageCount > 40 {
				err = errSourceTooLarge
				break
			}
			var parts []struct{ Type, Markdown string }
			if err = json.Unmarshal(raw, &parts); err != nil {
				break
			}
			for partIndex, part := range parts {
				if part.Type != "text" || strings.TrimSpace(part.Markdown) == "" {
					continue
				}
				if !validSourceBody(part.Markdown) || len(binding.Steps) >= 40 {
					err = errSourceTooLarge
					break
				}
				title := fmt.Sprintf("Review %s message %d", role, len(binding.Steps)+1)
				binding.Steps = append(binding.Steps, Step{Kind: "check", Title: title, Body: part.Markdown, Readonly: true})
				entry, marshalErr := json.Marshal(struct {
					MessageID string `json:"message_id"`
					SortOrder int64  `json:"sort_order"`
					Role      string `json:"role"`
					PartIndex int    `json:"part_index"`
					Markdown  string `json:"markdown"`
				}{messageID, sortOrder, role, partIndex, part.Markdown})
				if marshalErr != nil {
					err = marshalErr
					break
				}
				transcript.Write(entry)
				transcript.WriteByte('\n')
				if transcript.Len() > maxDraftSourceBytes {
					err = errSourceTooLarge
					break
				}
			}
			if err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return binding, err
		}
		if len(binding.Steps) == 0 {
			return binding, errSourceEmpty
		}
		binding.Title = boundedSourceTitle(title, "Runbook from conversation")
		binding.Outline = "Completed conversation text is unverified source material. No command or tool call was inferred."
		binding.ContentHash = store.Hash(transcript.String())
	case "import":
		var name string
		var content []byte
		var size int
		err = tx.QueryRow(r.Context(), `SELECT name,size_bytes,
	 CASE WHEN size_bytes<=$2 AND octet_length(content)<=$2 THEN content ELSE ''::bytea END
	 FROM v3_uploads WHERE id=$1 FOR SHARE`, in.From, maxDraftSourceBytes).
			Scan(&name, &size, &content)
		if err != nil {
			return binding, err
		}
		if size > maxDraftSourceBytes {
			return binding, errSourceTooLarge
		}
		if len(content) == 0 || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			return binding, errSourceEmpty
		}
		binding.Title = boundedSourceTitle(strings.TrimSuffix(name, ".md"), "Imported Runbook outline")
		binding.Outline = "Uploaded Markdown is unverified source material. Review all sections and commands before use."
		binding.ContentHash = store.Hash(string(content))
		binding.Steps, err = importedReviewSteps(string(content))
		if err != nil {
			return binding, err
		}
	}
	binding.Steps = normalizeSteps(binding.Steps)
	return binding, nil
}

func validSourceText(s string, max int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && len([]rune(s)) <= max
}

func validSourceBody(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && len(s) <= 16000
}

func firstRunes(s string, n int) string {
	characters := []rune(s)
	if len(characters) > n {
		characters = characters[:n]
	}
	return string(characters)
}

func boundedSourceTitle(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" || !validSourceText(s, 180) {
		return fallback
	}
	return s
}

func importedReviewSteps(markdown string) ([]Step, error) {
	// Keep the entire Markdown as source data. Do not turn fenced commands or
	// prose into executable steps by guessing their intent.
	if len(markdown) > 16000 {
		return nil, errSourceTooLarge
	}
	return []Step{{N: 1, Kind: "check", Title: "Review imported Markdown", Body: markdown, Readonly: true}}, nil
}
