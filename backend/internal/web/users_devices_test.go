package web

import (
	"strings"
	"testing"
)

const deviceTestID = "11111111-2222-4333-8444-555555555555"

func TestDevicesPatchFromBody(t *testing.T) {
	validBody := func(overrides string) string {
		return `{"which":["devices"],"data":{"devices":{"` + deviceTestID + `":{"label":"Chrome · Linux","overrides":` + overrides + `}}}}`
	}

	t.Run("ignores requests without devices", func(t *testing.T) {
		devices, ok, err := devicesPatchFromBody([]byte(`{"which":["locale"],"data":{"locale":"de"}}`), []string{"locale"})
		if err != nil || ok || devices != nil {
			t.Fatalf("expected no device handling, got ok=%v err=%v devices=%v", ok, err, devices)
		}
	})

	t.Run("accepts a single false override", func(t *testing.T) {
		devices, ok, err := devicesPatchFromBody([]byte(validBody(`{"singleClick":false}`)), []string{"devices"})
		if err != nil || !ok {
			t.Fatalf("unexpected result ok=%v err=%v", ok, err)
		}
		if len(devices) != 1 {
			t.Fatalf("expected one device, got %d", len(devices))
		}
		record := devices[deviceTestID]
		if record.Overrides.SingleClick == nil || *record.Overrides.SingleClick {
			t.Fatal("expected explicit false to survive")
		}
	})

	t.Run("accepts clearing the override", func(t *testing.T) {
		devices, ok, err := devicesPatchFromBody([]byte(validBody("{}")), []string{"devices"})
		if err != nil || !ok {
			t.Fatalf("unexpected result ok=%v err=%v", ok, err)
		}
		if devices[deviceTestID].Overrides.SingleClick != nil {
			t.Fatal("expected no override after reset")
		}
	})

	t.Run("accepts unrelated account fields in the same request", func(t *testing.T) {
		body := `{"which":["devices","locale"],"data":{"locale":"de","devices":{"` + deviceTestID + `":{"overrides":{"singleClick":true}}}}}`
		if _, ok, err := devicesPatchFromBody([]byte(body), []string{"devices", "locale"}); err != nil || !ok {
			t.Fatalf("expected a combined request to parse, got ok=%v err=%v", ok, err)
		}
	})

	rejected := map[string]struct {
		which []string
		body  string
	}{
		"mixed with account singleClick": {
			which: []string{"devices", "singleClick"},
			body:  `{"which":["devices","singleClick"],"data":{"singleClick":true,"devices":{}}}`,
		},
		"missing data.devices": {
			which: []string{"devices"},
			body:  `{"which":["devices"],"data":{}}`,
		},
		"missing data": {
			which: []string{"devices"},
			body:  `{"which":["devices"]}`,
		},
		"empty devices map": {
			which: []string{"devices"},
			body:  `{"which":["devices"],"data":{"devices":{}}}`,
		},
		"two devices": {
			which: []string{"devices"},
			body:  `{"which":["devices"],"data":{"devices":{"` + deviceTestID + `":{"overrides":{}},"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee":{"overrides":{}}}}}`,
		},
		"null devices": {
			which: []string{"devices"},
			body:  `{"which":["devices"],"data":{"devices":null}}`,
		},
		"extra override field": {
			which: []string{"devices"},
			body:  validBody(`{"singleClick":true,"dateFormat":true}`),
		},
		"null singleClick": {
			which: []string{"devices"},
			body:  validBody(`{"singleClick":null}`),
		},
		"string singleClick": {
			which: []string{"devices"},
			body:  validBody(`{"singleClick":"true"}`),
		},
		"reserved device id": {
			which: []string{"devices"},
			body:  `{"which":["devices"],"data":{"devices":{"__proto__":{"overrides":{}}}}}`,
		},
		"invalid body": {
			which: []string{"devices"},
			body:  `{"which":["devices"],"data":`,
		},
	}
	for name, tc := range rejected {
		if _, _, err := devicesPatchFromBody([]byte(tc.body), tc.which); err == nil {
			t.Fatalf("expected %s to be rejected", name)
		}
	}
}

func TestDevicePatchKeepsLastSeenServerOwned(t *testing.T) {
	// The parser accepts a client lastSeen for compatibility but the merge later
	// replaces it; this guards the documented contract at the wire boundary.
	body := `{"which":["devices"],"data":{"devices":{"` + deviceTestID + `":{"lastSeen":1,"overrides":{"singleClick":true}}}}}`
	devices, ok, err := devicesPatchFromBody([]byte(body), []string{"devices"})
	if err != nil || !ok {
		t.Fatalf("unexpected result ok=%v err=%v", ok, err)
	}
	if devices[deviceTestID].LastSeen != 1 {
		t.Fatal("parser keeps the wire value for validation only")
	}
	negative := `{"which":["devices"],"data":{"devices":{"` + deviceTestID + `":{"lastSeen":-5,"overrides":{}}}}}`
	if _, _, err := devicesPatchFromBody([]byte(negative), []string{"devices"}); err == nil {
		t.Fatal("expected a negative lastSeen to be rejected")
	}
}

func TestDevicePatchErrorsStayReadable(t *testing.T) {
	body := `{"which":["devices"],"data":{"devices":{"` + deviceTestID + `":{"overrides":{"dateFormat":true}}}}}`
	_, _, err := devicesPatchFromBody([]byte(body), []string{"devices"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), deviceTestID) {
		t.Fatal("validation errors must not echo the device id")
	}
}
