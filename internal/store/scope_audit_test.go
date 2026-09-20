package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestAuditSearchPaginationAndFilters(t *testing.T) {
	s := platformDatabase(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('auditor','audit@example.invalid','Readable Actor','admin','unused')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		action := "application.updated"
		if i == 1 {
			action = "literal%value"
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome) VALUES('auditor','legacy',$1,$2,'success')`, action, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.SearchAudit(ctx, AuditFilter{ActorID: "auditor", Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if first.Items[0]["actor_name"] != "Readable Actor" {
		t.Fatal("actor display name missing")
	}
	// An insert between reads must not duplicate or displace the next page.
	if _, err = s.DB.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome) VALUES('auditor','new','later','success')`); err != nil {
		t.Fatal(err)
	}
	second, err := s.SearchAudit(ctx, AuditFilter{ActorID: "auditor", Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 2 {
		t.Fatalf("second page: %+v %v", second, err)
	}
	for _, a := range first.Items {
		for _, b := range second.Items {
			if a["id"] == b["id"] {
				t.Fatal("pagination repeated a record")
			}
		}
	}
	exact, err := s.SearchAudit(ctx, AuditFilter{Query: "%", ApplicationID: "legacy"})
	if err != nil || len(exact.Items) != 1 {
		t.Fatalf("literal wildcard search: %+v %v", exact, err)
	}
	exact, err = s.SearchAudit(ctx, AuditFilter{Query: "Readable Actor", Action: "application.updated", Outcome: "success"})
	if err != nil || len(exact.Items) != 5 {
		t.Fatalf("combined filters: %+v %v", exact, err)
	}
	future := time.Now().Add(time.Hour)
	empty, err := s.SearchAudit(ctx, AuditFilter{From: &future})
	if err != nil || len(empty.Items) != 0 {
		t.Fatal("time filter ignored")
	}
	for _, f := range []AuditFilter{{Cursor: "-1"}, {Cursor: "bad"}, {Limit: 251}, {From: &future, To: &future}} {
		if _, err = s.SearchAudit(ctx, f); err == nil {
			t.Fatal("invalid audit filter accepted")
		}
	}
}
