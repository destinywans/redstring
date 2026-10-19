package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

type conversationEnvironmentAuthorizerFunc func(context.Context, core.User, rbac.V3Decision) error

func (f conversationEnvironmentAuthorizerFunc) Authorize(ctx context.Context, user core.User, decision rbac.V3Decision) error {
	return f(ctx, user, decision)
}

func TestAllowedConversationEnvironmentsIncludesCanvasOnlyView(t *testing.T) {
	grants := map[string]map[string]bool{
		"chat":   {"conversation": true},
		"canvas": {"canvas": true},
		"both":   {"conversation": true, "canvas": true},
	}
	seen := map[string]int{}
	authorizer := conversationEnvironmentAuthorizerFunc(func(_ context.Context, user core.User, decision rbac.V3Decision) error {
		if user.ID != "owner" || decision.Action != "view" {
			t.Fatal("unexpected user or action")
		}
		seen[decision.EnvironmentID]++
		if grants[decision.EnvironmentID][decision.Module] {
			return nil
		}
		return store.ErrForbidden
	})
	got, err := allowedConversationEnvironments(context.Background(), core.User{ID: "owner"}, []string{"chat", "canvas", "both", "none"}, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"chat", "canvas", "both"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("authorized union: got %v, want %v", got, want)
	}
	if seen["chat"] != 1 || seen["canvas"] != 2 || seen["both"] != 1 || seen["none"] != 2 {
		t.Fatalf("each environment should be checked only until granted: %+v", seen)
	}
}

func TestAllowedConversationEnvironmentsFailsClosedOnPolicyError(t *testing.T) {
	storageErr := errors.New("policy database unavailable")
	authorizer := conversationEnvironmentAuthorizerFunc(func(_ context.Context, _ core.User, decision rbac.V3Decision) error {
		if decision.EnvironmentID == "broken" {
			return storageErr
		}
		return nil
	})
	got, err := allowedConversationEnvironments(context.Background(), core.User{ID: "owner"}, []string{"chat", "broken"}, authorizer)
	if !errors.Is(err, storageErr) || got != nil {
		t.Fatalf("authorization failure must not yield a partial environment set: %v, %v", got, err)
	}
}
