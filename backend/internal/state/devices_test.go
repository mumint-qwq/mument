package state

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/sqldb"
	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
	"github.com/gtsteffaniak/filebrowser/backend/pkg/settings"
)

func setupDeviceState(t *testing.T) string {
	t.Helper()
	t.Setenv("FILEBROWSER_ONLYOFFICE_SECRET", "")
	settings.Initialize("../../../_docker/src/noauth/backend/config.yaml")
	settings.Env.IsPlaywright = true

	dbPath := filepath.Join(t.TempDir(), "filebrowser.sqlite")
	if _, err := Initialize(dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Close() })
	return dbPath
}

func createDeviceTestUser(t *testing.T, username string) *users.User {
	t.Helper()
	u := &users.User{
		FrontendUser: users.FrontendUser{
			Username: username,
			NonAdminEditable: users.NonAdminEditable{
				Locale: "en",
			},
		},
	}
	if err := CreateUser(u, "password"); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return u
}

func devicePatchUser(id uint64, username, deviceID string, value *bool, label string) *users.User {
	patch := &users.User{ID: id, FrontendUser: users.FrontendUser{Username: username}}
	patch.Devices = users.DeviceSettings{
		deviceID: {
			Label:     label,
			Overrides: users.DeviceOverrides{SingleClick: value},
		},
	}
	return patch
}

func deviceValue(t *testing.T, u *users.User, deviceID string) *bool {
	t.Helper()
	record, ok := u.Devices[deviceID]
	if !ok {
		return nil
	}
	if record.Overrides.SingleClick == nil {
		return nil
	}
	v := *record.Overrides.SingleClick
	return &v
}

func TestUpdateUserDeviceSettingsRoundTrip(t *testing.T) {
	setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")

	const deviceA = "11111111-2222-4333-8444-555555555555"
	const deviceB = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	enabled := true
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, &enabled, "Chrome · Linux"), "", "devices"); err != nil {
		t.Fatalf("patch device A: %v", err)
	}
	loaded, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SingleClick {
		t.Fatal("the account-wide singleClick default must not change")
	}
	if got := deviceValue(t, &loaded, deviceA); got == nil || !*got {
		t.Fatalf("expected device A override true, got %v", got)
	}
	if loaded.Devices[deviceA].Label != "Chrome · Linux" {
		t.Fatalf("unexpected label %q", loaded.Devices[deviceA].Label)
	}
	if loaded.Devices[deviceA].LastSeen <= 0 {
		t.Fatal("expected a server-generated lastSeen")
	}

	// A second device keeps its own value and the first record survives.
	disabled := false
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceB, &disabled, "Safari · iOS"), "", "devices"); err != nil {
		t.Fatalf("patch device B: %v", err)
	}
	loaded, err = GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := deviceValue(t, &loaded, deviceA); got == nil || !*got {
		t.Fatal("expected device A to survive the device B save")
	}
	if got := deviceValue(t, &loaded, deviceB); got == nil || *got {
		t.Fatal("expected device B explicit false to survive")
	}

	// The SQL projection must round-trip, not just the in-memory cache.
	clearUserRecordCache()
	fromSQL, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := deviceValue(t, &fromSQL, deviceA); got == nil || !*got {
		t.Fatal("expected device A to reload from SQL")
	}
	if got := deviceValue(t, &fromSQL, deviceB); got == nil || *got {
		t.Fatal("expected device B explicit false to reload from SQL")
	}
	if fromSQL.SingleClick {
		t.Fatal("account singleClick default must stay false after SQL reload")
	}

	// Resetting device A keeps the record but clears the override.
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, nil, ""), "", "devices"); err != nil {
		t.Fatalf("reset device A: %v", err)
	}
	clearUserRecordCache()
	afterReset, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := afterReset.Devices[deviceA]; !ok {
		t.Fatal("reset should keep the device record for label/retention")
	}
	if afterReset.Devices[deviceA].Overrides.SingleClick != nil {
		t.Fatal("reset should clear the override")
	}
	if afterReset.Devices[deviceA].Label != "Chrome · Linux" {
		t.Fatalf("reset should keep the label, got %q", afterReset.Devices[deviceA].Label)
	}
	if got := deviceValue(t, &afterReset, deviceB); got == nil || *got {
		t.Fatal("reset of device A must not touch device B")
	}
}

func TestUpdateUserDeviceSettingsConcurrentDevices(t *testing.T) {
	setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")

	const deviceA = "11111111-2222-4333-8444-555555555555"
	const deviceB = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	enabled := true
	disabled := false

	errs := make(chan error, 2)
	go func() {
		errs <- UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, &enabled, "Chrome"), "", "devices")
	}()
	go func() {
		errs <- UpdateUser(devicePatchUser(u.ID, u.Username, deviceB, &disabled, "Safari"), "", "devices")
	}()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent device save failed: %v", err)
		}
	}

	clearUserRecordCache()
	loaded, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Devices) != 2 {
		t.Fatalf("expected both devices to be stored, got %d", len(loaded.Devices))
	}
	if got := deviceValue(t, &loaded, deviceA); got == nil || !*got {
		t.Fatal("expected device A to survive concurrent writes")
	}
	if got := deviceValue(t, &loaded, deviceB); got == nil || *got {
		t.Fatal("expected device B to survive concurrent writes")
	}
}

func TestUpdateUserDeviceSettingsConcurrentWithUnrelatedField(t *testing.T) {
	setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")

	const deviceA = "11111111-2222-4333-8444-555555555555"
	enabled := true
	errs := make(chan error, 2)
	go func() {
		errs <- UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, &enabled, "Chrome"), "", "devices")
	}()
	go func() {
		localePatch := &users.User{ID: u.ID, FrontendUser: users.FrontendUser{Username: u.Username}}
		localePatch.Locale = "de"
		errs <- UpdateUser(localePatch, "", "locale")
	}()
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent save failed: %v", err)
		}
	}

	clearUserRecordCache()
	loaded, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Locale != "de" {
		t.Fatalf("expected the unrelated locale save to persist, got %q", loaded.Locale)
	}
	if got := deviceValue(t, &loaded, deviceA); got == nil || !*got {
		t.Fatal("expected the device record to survive an unrelated field save")
	}
	if loaded.SingleClick {
		t.Fatal("account singleClick default must not change")
	}
}

func TestUpdateUserDeviceSettingsRejectsInvalidPatch(t *testing.T) {
	setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")

	const deviceA = "11111111-2222-4333-8444-555555555555"
	enabled := true
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, &enabled, "Chrome"), "", "devices"); err != nil {
		t.Fatal(err)
	}

	multi := devicePatchUser(u.ID, u.Username, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", &enabled, "Two")
	multi.Devices[deviceA] = users.DeviceRecord{Label: "Also", Overrides: users.DeviceOverrides{SingleClick: &enabled}}
	if err := UpdateUser(multi, "", "devices"); err == nil {
		t.Fatal("expected a multi-device patch to be rejected")
	}

	reserved := &users.User{ID: u.ID, FrontendUser: users.FrontendUser{Username: u.Username}}
	reserved.Devices = users.DeviceSettings{
		"__proto__": {Overrides: users.DeviceOverrides{SingleClick: &enabled}},
	}
	if err := UpdateUser(reserved, "", "devices"); err == nil {
		t.Fatal("expected a reserved device id to be rejected")
	}

	clearUserRecordCache()
	loaded, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Devices) != 1 {
		t.Fatalf("rejected patches must not change stored devices, got %d", len(loaded.Devices))
	}
	if got := deviceValue(t, &loaded, deviceA); got == nil || !*got {
		t.Fatal("expected the original device record to be unchanged")
	}
}

func TestUpdateUserDeviceCacheConsistentWhenSQLWriteFails(t *testing.T) {
	dbPath := setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")

	const deviceA = "11111111-2222-4333-8444-555555555555"
	const deviceB = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	enabled := true
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, &enabled, "Chrome"), "", "devices"); err != nil {
		t.Fatal(err)
	}

	// Remove the row behind the cache's back so the next UPDATE affects no rows.
	other, _, err := sqldb.NewSQLStoreWithOptions(dbPath, sqldb.NewSQLStoreOpts{SkipQuickSetup: true})
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	if err := other.DeleteUserByID(u.ID); err != nil {
		t.Fatalf("delete row: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("close second store: %v", err)
	}

	disabled := false
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceB, &disabled, "Safari"), "", "devices"); err == nil {
		t.Fatal("expected the SQL write to fail")
	}

	// The cache must still expose the last confirmed record, not the failed patch.
	loaded, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := deviceValue(t, &loaded, deviceA); got == nil || !*got {
		t.Fatal("expected the confirmed device record to remain in cache")
	}
	if _, ok := loaded.Devices[deviceB]; ok {
		t.Fatal("the failed patch must not leak into the cache")
	}
}

func TestUpdateUserDeviceSettingsSurviveUsernameChange(t *testing.T) {
	setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")
	const deviceA = "11111111-2222-4333-8444-555555555555"
	enabled := true
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, &enabled, "Chrome"), "", "devices"); err != nil {
		t.Fatal(err)
	}

	rename := &users.User{ID: u.ID, FrontendUser: users.FrontendUser{Username: "alicia"}}
	if err := UpdateUser(rename, "", "username"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	clearUserRecordCache()
	loaded, err := GetUserByUsername("alicia")
	if err != nil {
		t.Fatal(err)
	}
	if got := deviceValue(t, &loaded, deviceA); got == nil || !*got {
		t.Fatal("expected the device record to follow the renamed user")
	}
	if _, err := GetUserByUsername("alice"); err == nil {
		t.Fatal("expected the old username to be gone")
	}
}

func TestCreateUserDoesNotInheritDevices(t *testing.T) {
	setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")
	const deviceA = "11111111-2222-4333-8444-555555555555"
	enabled := true
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, &enabled, "Chrome"), "", "devices"); err != nil {
		t.Fatal(err)
	}
	bob := createDeviceTestUser(t, "bob")
	loaded, err := GetUserByUsername("bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Devices) != 0 {
		t.Fatalf("new user must not inherit device records: %v", loaded.Devices)
	}
	if bob.Devices != nil && len(bob.Devices) != 0 {
		t.Fatal("create must not seed device records")
	}
}

func TestUpdateUserDeviceSettingsCapacity(t *testing.T) {
	setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")
	enabled := true
	for i := 0; i < users.MaxDeviceRecords+1; i++ {
		id := fmt.Sprintf("device-%03d-aaaaaaaa", i)
		if err := UpdateUser(devicePatchUser(u.ID, u.Username, id, &enabled, "Browser"), "", "devices"); err != nil {
			t.Fatalf("device %d: %v", i, err)
		}
	}
	clearUserRecordCache()
	loaded, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Devices) != users.MaxDeviceRecords {
		t.Fatalf("expected %d devices, got %d", users.MaxDeviceRecords, len(loaded.Devices))
	}
	last := fmt.Sprintf("device-%03d-aaaaaaaa", users.MaxDeviceRecords)
	if got := deviceValue(t, &loaded, last); got == nil || !*got {
		t.Fatal("the most recently saved device must be retained")
	}
	if _, ok := loaded.Devices["device-000-aaaaaaaa"]; ok {
		t.Fatal("the oldest device should have been pruned")
	}
}

func TestUpdateUserStripsEnforcedDeviceOverride(t *testing.T) {
	snapshotUserDefaultsGlobals(t)
	setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")

	const deviceA = "11111111-2222-4333-8444-555555555555"
	enabled := true
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, &enabled, "Chrome"), "", "devices"); err != nil {
		t.Fatal(err)
	}

	// Admin turns on enforcement for the account-wide singleClick value.
	userDefaultsMu.Lock()
	userDefaultsDefault = settings.UserDefaults{Listing: settings.UserDefaultsListing{SingleClick: true}}
	userDefaultsEnforcedDefault = settings.UserDefaultsEnforcement{
		Listing: settings.UserDefaultsListingEnforcement{SingleClick: true},
	}
	userDefaultsMu.Unlock()

	loaded, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	loaded.SingleClick = true
	if !ApplyEnforcedSyncToUser(&loaded) {
		t.Fatal("expected the enforced sync to detect and strip the device override")
	}
	if loaded.Devices[deviceA].Overrides.SingleClick != nil {
		t.Fatal("expected the in-memory override to be stripped by the sync")
	}
	// Startup persistence path: write the changed profile fields back.
	if err := UpdateUser(&loaded, "", "singleClick"); err != nil {
		t.Fatalf("persist enforced sync: %v", err)
	}

	clearUserRecordCache()
	persisted, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Devices[deviceA].Overrides.SingleClick != nil {
		t.Fatal("expected the stripped override to be persisted")
	}
	if persisted.Devices[deviceA].Label != "Chrome" {
		t.Fatal("expected the device record itself to remain")
	}

	// Turning enforcement off must not resurrect the cleared override.
	userDefaultsMu.Lock()
	userDefaultsEnforcedDefault = settings.UserDefaultsEnforcement{}
	userDefaultsMu.Unlock()
	clearUserRecordCache()
	afterDisable, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if afterDisable.Devices[deviceA].Overrides.SingleClick != nil {
		t.Fatal("clearing enforcement must not revive the old override")
	}
}

func TestUpdateUserKeepsAdminDeviceOverrideUnderEnforcement(t *testing.T) {
	snapshotUserDefaultsGlobals(t)
	setupDeviceState(t)

	admin := &users.User{
		FrontendUser: users.FrontendUser{
			Username: "adminuser",
			NonAdminEditable: users.NonAdminEditable{
				Locale: "en",
			},
		},
	}
	if err := CreateUser(admin, "password"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	// CreateUser applies the defaults profile, which resets Permissions. Grant
	// admin afterwards, as the real quick-setup/admin path does.
	admin.Permissions.Admin = true
	if err := UpdateUser(admin, "", "permissions"); err != nil {
		t.Fatalf("grant admin: %v", err)
	}
	reloadedAdmin, err := GetUserByUsername(admin.Username)
	if err != nil {
		t.Fatalf("reload admin: %v", err)
	}
	admin = &reloadedAdmin
	if !admin.Permissions.Admin {
		t.Fatal("expected an admin user")
	}

	const deviceA = "11111111-2222-4333-8444-555555555555"
	enabled := true
	if err := UpdateUser(devicePatchUser(admin.ID, admin.Username, deviceA, &enabled, "Chrome"), "", "devices"); err != nil {
		t.Fatal(err)
	}

	// Enforcement locks singleClick for normal users. Admins are exempt by the
	// existing EnforcementAppliesToUser rule, so validation and cleanup must
	// both leave the admin override alone.
	userDefaultsMu.Lock()
	userDefaultsDefault = settings.UserDefaults{Listing: settings.UserDefaultsListing{SingleClick: true}}
	userDefaultsEnforcedDefault = settings.UserDefaultsEnforcement{
		Listing: settings.UserDefaultsListingEnforcement{SingleClick: true},
	}
	userDefaultsMu.Unlock()

	if settings.EnforcementAppliesToUser(admin) {
		t.Fatal("test admin must be exempt from enforcement")
	}
	localePatch := &users.User{ID: admin.ID, FrontendUser: users.FrontendUser{Username: admin.Username}}
	localePatch.Locale = "de"
	if err := UpdateUser(localePatch, "", "locale"); err != nil {
		t.Fatalf("unrelated admin save under enforcement: %v", err)
	}

	clearUserRecordCache()
	loaded, err := GetUserByUsername(admin.Username)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Locale != "de" {
		t.Fatalf("expected the unrelated save to persist, got %q", loaded.Locale)
	}
	if got := deviceValue(t, &loaded, deviceA); got == nil || !*got {
		t.Fatal("admin device override must survive enforcement and an unrelated save")
	}
}

func TestGetUserReturnsIsolatedDeviceMap(t *testing.T) {
	setupDeviceState(t)
	u := createDeviceTestUser(t, "alice")
	const deviceA = "11111111-2222-4333-8444-555555555555"
	enabled := true
	if err := UpdateUser(devicePatchUser(u.ID, u.Username, deviceA, &enabled, "Chrome"), "", "devices"); err != nil {
		t.Fatal(err)
	}
	first, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	record := first.Devices[deviceA]
	record.Overrides.SingleClick = nil
	first.Devices[deviceA] = record
	second, err := GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := deviceValue(t, &second, deviceA); got == nil || !*got {
		t.Fatal("mutating a returned user must not modify the shared cache")
	}
}
