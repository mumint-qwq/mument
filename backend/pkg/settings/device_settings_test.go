package settings

import (
	"errors"
	"testing"

	"github.com/gtsteffaniak/filebrowser/backend/internal/database/users"
)

func deviceBool(v bool) *bool { return &v }

func TestDeviceSettingsProfileRoundTrip(t *testing.T) {
	user := &users.User{}
	user.Username = "alice"
	user.SingleClick = false
	user.Devices = users.DeviceSettings{
		"device-0001": {
			Label:     "Chrome · Linux",
			LastSeen:  42,
			Overrides: users.DeviceOverrides{SingleClick: deviceBool(true)},
		},
		"device-0002": {
			Label:     "Safari · iOS",
			LastSeen:  43,
			Overrides: users.DeviceOverrides{SingleClick: deviceBool(false)},
		},
	}
	data, err := ProfileJSONFromUser(user)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	var restored users.User
	if err := ApplyProfileToUser(&restored, data); err != nil {
		t.Fatalf("apply profile: %v", err)
	}
	if restored.SingleClick != false {
		t.Fatal("expected the account default to survive the profile round trip")
	}
	first, ok := restored.Devices["device-0001"]
	if !ok {
		t.Fatal("expected the device record to round-trip")
	}
	if first.Label != "Chrome · Linux" || first.LastSeen != 42 {
		t.Fatalf("unexpected record: %+v", first)
	}
	if first.Overrides.SingleClick == nil || !*first.Overrides.SingleClick {
		t.Fatal("expected singleClick true")
	}
	second, ok := restored.Devices["device-0002"]
	if !ok {
		t.Fatal("expected the second device to round-trip")
	}
	if second.Overrides.SingleClick == nil || *second.Overrides.SingleClick {
		t.Fatal("expected an explicit false to round-trip")
	}
}

func TestDeviceSettingsAreNotSeededFromDefaults(t *testing.T) {
	defaults := UserDefaults{Listing: UserDefaultsListing{SingleClick: true}}
	if profile := ProfileFromUserDefaults(defaults); profile.Devices != nil {
		t.Fatal("the defaults template must never carry device records")
	}

	fresh := &users.User{}
	ApplyFullProfileFromDefaults(fresh, defaults)
	if len(fresh.Devices) != 0 {
		t.Fatalf("a new user must not inherit device records, got %v", fresh.Devices)
	}

	existing := &users.User{}
	existing.Devices = users.DeviceSettings{
		"device-0001": {Label: "Chrome", Overrides: users.DeviceOverrides{SingleClick: deviceBool(false)}},
	}
	ApplyFullProfileFromDefaults(existing, defaults)
	if len(existing.Devices) != 1 {
		t.Fatal("applying the defaults template must preserve existing device records")
	}
}

func TestValidateDeviceOverridesNotEnforced(t *testing.T) {
	user := &users.User{FrontendUser: users.FrontendUser{Username: "alice"}}
	devices := users.DeviceSettings{
		"device-0001": {Overrides: users.DeviceOverrides{SingleClick: deviceBool(true)}},
	}
	enforced := UserDefaultsEnforcement{
		Listing: UserDefaultsListingEnforcement{SingleClick: true},
	}

	err := ValidateDeviceOverridesNotEnforced(devices, enforced, user)
	var locked ErrEnforcedUserField
	if !errors.As(err, &locked) {
		t.Fatalf("expected ErrEnforcedUserField, got %v", err)
	}
	if locked.Path != "listing.singleClick" {
		t.Fatalf("unexpected enforcement path %q", locked.Path)
	}

	// An unrelated policy must not block this override.
	otherPolicy := UserDefaultsEnforcement{
		Listing: UserDefaultsListingEnforcement{DateFormat: true},
	}
	if err := ValidateDeviceOverridesNotEnforced(devices, otherPolicy, user); err != nil {
		t.Fatalf("expected unrelated enforcement to pass, got %v", err)
	}

	// Clearing the override is always allowed, even when the field is locked.
	cleared := users.DeviceSettings{
		"device-0001": {Overrides: users.DeviceOverrides{}},
	}
	if err := ValidateDeviceOverridesNotEnforced(cleared, enforced, user); err != nil {
		t.Fatalf("expected a reset to be allowed, got %v", err)
	}

	// Admins are exempt from user-defaults enforcement.
	admin := &users.User{FrontendUser: users.FrontendUser{Username: "admin"}}
	admin.Permissions.Admin = true
	if err := ValidateDeviceOverridesNotEnforced(devices, enforced, admin); err != nil {
		t.Fatalf("expected admins to be exempt, got %v", err)
	}
}

func TestStripEnforcedDeviceOverrides(t *testing.T) {
	user := &users.User{FrontendUser: users.FrontendUser{Username: "alice"}}
	user.Devices = users.DeviceSettings{
		"device-0001": {
			Label:     "Chrome · Linux",
			LastSeen:  99,
			Overrides: users.DeviceOverrides{SingleClick: deviceBool(true)},
		},
	}
	enforced := UserDefaultsEnforcement{
		Listing: UserDefaultsListingEnforcement{SingleClick: true},
	}
	StripEnforcedDeviceOverrides(user, enforced)
	record := user.Devices["device-0001"]
	if record.Overrides.SingleClick != nil {
		t.Fatal("expected the enforced singleClick override to be removed")
	}
	if record.Label != "Chrome · Linux" || record.LastSeen != 99 {
		t.Fatalf("expected the record to survive, got %+v", record)
	}

	// Without enforcement the override must stay.
	user.Devices = users.DeviceSettings{
		"device-0001": {Overrides: users.DeviceOverrides{SingleClick: deviceBool(true)}},
	}
	StripEnforcedDeviceOverrides(user, UserDefaultsEnforcement{})
	if user.Devices["device-0001"].Overrides.SingleClick == nil {
		t.Fatal("expected no strip when singleClick is not enforced")
	}

	// Admins are exempt from enforcement, so their override must stay too.
	admin := &users.User{FrontendUser: users.FrontendUser{Username: "admin"}}
	admin.Permissions.Admin = true
	admin.Devices = users.DeviceSettings{
		"device-0001": {Overrides: users.DeviceOverrides{SingleClick: deviceBool(true)}},
	}
	StripEnforcedDeviceOverrides(admin, enforced)
	if admin.Devices["device-0001"].Overrides.SingleClick == nil {
		t.Fatal("expected admins to keep device overrides under enforcement")
	}
}

func TestApplyEnforcedDefaultsClearsDeviceOverride(t *testing.T) {
	user := &users.User{FrontendUser: users.FrontendUser{Username: "alice"}}
	user.Devices = users.DeviceSettings{
		"device-0001": {Overrides: users.DeviceOverrides{SingleClick: deviceBool(true)}},
	}
	defaults := UserDefaults{Listing: UserDefaultsListing{SingleClick: false}}
	enforced := UserDefaultsEnforcement{
		Listing: UserDefaultsListingEnforcement{SingleClick: true},
	}
	ApplyEnforcedDefaultsFrom(user, defaults, enforced)
	if user.SingleClick {
		t.Fatal("expected the enforced global value to win")
	}
	record, ok := user.Devices["device-0001"]
	if !ok {
		t.Fatal("expected the device record to remain")
	}
	if record.Overrides.SingleClick != nil {
		t.Fatal("expected the enforced default to clear the device override")
	}
}
