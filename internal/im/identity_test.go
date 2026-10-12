package im

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestWithIMIdentity(t *testing.T) {
	const tenantID uint64 = 42
	binding := &UserBinding{ID: "binding-1", TenantID: tenantID, UserID: "user-1"}
	member := &types.TenantMember{TenantID: tenantID, UserID: "user-1", Role: types.TenantRoleContributor}
	ctx := withIMIdentity(context.Background(), binding, member, &IncomingMessage{ChatType: ChatTypeDirect})

	gotTenant, ok := types.TenantIDFromContext(ctx)
	if !ok || gotTenant != tenantID {
		t.Fatalf("TenantID = %d (ok=%v), want %d", gotTenant, ok, tenantID)
	}

	userID, ok := types.UserIDFromContext(ctx)
	if !ok {
		t.Fatalf("UserID = %q (ok=%v), want bound user", userID, ok)
	}
	if want := "user-1"; userID != want {
		t.Fatalf("UserID = %q, want %q", userID, want)
	}
	if role := types.TenantRoleFromContext(ctx); role != types.TenantRoleContributor {
		t.Fatalf("TenantRole = %v, want %v", role, types.TenantRoleContributor)
	}

	principal, ok := types.PrincipalFromContext(ctx)
	if !ok {
		t.Fatalf("Principal missing")
	}
	if principal.Type != types.PrincipalIMUser || principal.ID != "binding-1" {
		t.Fatalf("Principal = %#v, want binding-scoped im_user", principal)
	}

	if !types.IsMCPOAuthNonInteractive(ctx) {
		t.Fatal("IM context should mark MCP OAuth as non-interactive")
	}
}
