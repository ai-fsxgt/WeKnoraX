package im

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	appservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestConsumeBindingCodeMultiGroupUniqueness(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { require.NoError(t, os.Chdir(previousDir)) })

	dbPath := filepath.Join(t.TempDir(), "im-bindings.db")
	require.NoError(t, database.RunMigrationsWithOptions("sqlite3://unused",
		database.MigrationOptions{SQLiteDBPath: dbPath}))
	db, err := gorm.Open(sqlite.Open(dbPath+"?_foreign_keys=on"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	require.NoError(t, db.Exec(
		"INSERT INTO tenants (id, name, business, status) VALUES (9001, 'im-workspace', 'im-test', 'active')",
	).Error)
	for _, userID := range []string{"im-user-1", "im-user-2"} {
		require.NoError(t, db.Exec(
			"INSERT INTO users (id, username, email, password_hash, is_active) VALUES (?, ?, ?, 'unused', 1)",
			userID, userID, userID+"@example.invalid",
		).Error)
		require.NoError(t, db.Exec(
			"INSERT INTO tenant_members (user_id, tenant_id, role, status) VALUES (?, 9001, 'owner', 'active')",
			userID,
		).Error)
	}
	require.NoError(t, db.Create(&IMChannel{
		ID: "im-bot", Platform: string(PlatformDingtalk), Enabled: true, AllowGroupChat: true,
	}).Error)

	userRepo := apprepo.NewUserRepository(db)
	tenantService := appservice.NewTenantService(apprepo.NewTenantRepository(db), nil)
	memberService := appservice.NewTenantMemberService(apprepo.NewTenantMemberRepository(db), nil, nil, nil)
	svc := &Service{
		db:            db,
		userService:   appservice.NewUserService(nil, userRepo, nil, tenantService, memberService, nil),
		tenantService: tenantService,
		memberService: memberService,
	}
	ctx := context.Background()

	bind := func(userID string, chatType ChatType, externalID string) *UserBinding {
		t.Helper()
		request, err := svc.CreateBindingRequest(ctx, userID, "im-bot", 9001)
		require.NoError(t, err)
		binding, err := svc.ConsumeBindingCode(ctx, "im-bot", chatType, externalID, "", request.Code)
		require.NoError(t, err)
		require.Equal(t, userID, binding.UserID)
		require.Equal(t, uint64(9001), binding.TenantID)
		require.Equal(t, chatType, binding.ChatType)
		require.Equal(t, externalID, binding.ExternalUserID)
		consumed, err := svc.GetBindingRequest(userID, request.ID)
		require.NoError(t, err)
		require.Equal(t, IMBindingRequestConsumed, consumed.Status)
		require.NotNil(t, consumed.BindingID)
		require.Equal(t, binding.ID, *consumed.BindingID)
		return binding
	}

	group1 := bind("im-user-1", ChatTypeGroup, "group-1")
	group2 := bind("im-user-1", ChatTypeGroup, "group-2")
	direct := bind("im-user-1", ChatTypeDirect, "direct-1")
	require.NotEqual(t, group1.ID, group2.ID)
	require.NotEqual(t, group1.ID, direct.ID)

	for _, tc := range []struct {
		name       string
		userID     string
		chatType   ChatType
		externalID string
		wantErr    error
	}{
		{"second direct identity", "im-user-1", ChatTypeDirect, "direct-2", ErrBindingUserInUse},
		{"another account claiming the group", "im-user-2", ChatTypeGroup, "group-1", ErrBindingIdentityInUse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := svc.CreateBindingRequest(ctx, tc.userID, "im-bot", 9001)
			require.NoError(t, err)
			binding, err := svc.ConsumeBindingCode(ctx, "im-bot", tc.chatType, tc.externalID, "", request.Code)
			require.ErrorIs(t, err, tc.wantErr)
			require.Nil(t, binding)
			pending, err := svc.GetBindingRequest(tc.userID, request.ID)
			require.NoError(t, err)
			require.Equal(t, IMBindingRequestPending, pending.Status)
			require.Nil(t, pending.BindingID)
		})
	}

	bindings, err := svc.ListUserBindings("im-user-1")
	require.NoError(t, err)
	var bindingIDs []string
	for _, binding := range bindings {
		bindingIDs = append(bindingIDs, binding.ID)
	}
	require.ElementsMatch(t, []string{group1.ID, group2.ID, direct.ID}, bindingIDs)
	otherBindings, err := svc.ListUserBindings("im-user-2")
	require.NoError(t, err)
	require.Empty(t, otherBindings)
}
