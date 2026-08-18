package tgauth

import (
	"testing"
	"time"
)

func deviceStore(t *testing.T) (*Store, string) {
	t.Helper()
	s := NewStore(t.TempDir())
	t.Cleanup(s.Close)
	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := s.Add(ApprovedToken{Token: "tok-dev", TelegramID: 4242, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return s, "tok-dev"
}

func addDev(t *testing.T, s *Store, token, uid, label string) {
	t.Helper()
	if _, err := s.AddDevice(token, DeviceInfo{UID: uid, Label: label, LastIP: "1.2.3.4"}); err != nil {
		t.Fatalf("AddDevice %s: %v", uid, err)
	}
}

func TestRemoveAllDevices(t *testing.T) {
	s, token := deviceStore(t)
	for _, uid := range []string{"tv-1", "phone-2", "box-3"} {
		addDev(t, s, token, uid, uid)
	}

	var forgotten []string
	OnDeviceRemoved = func(userID, uid string) {
		if userID != "tg:4242" {
			t.Errorf("hook got userID %q", userID)
		}
		forgotten = append(forgotten, uid)
	}
	t.Cleanup(func() { OnDeviceRemoved = nil })

	if n := s.RemoveAllDevices(token); n != 3 {
		t.Fatalf("removed %d, want 3", n)
	}
	tok, _ := s.Lookup(token)
	if len(tok.Devices) != 0 {
		t.Errorf("devices left: %+v", tok.Devices)
	}
	// Every unbound device must be reported, or its profile choice survives and a
	// re-paired box walks back into the previous owner's profile.
	if len(forgotten) != 3 {
		t.Errorf("hook fired for %v, want all three", forgotten)
	}
	if n := s.RemoveAllDevices(token); n != 0 {
		t.Errorf("second sweep removed %d", n)
	}
}

func TestRemoveDeviceFiresForgetHook(t *testing.T) {
	s, token := deviceStore(t)
	addDev(t, s, token, "tv-1", "TV")
	addDev(t, s, token, "phone-2", "Phone")

	var forgotten []string
	OnDeviceRemoved = func(_, uid string) { forgotten = append(forgotten, uid) }
	t.Cleanup(func() { OnDeviceRemoved = nil })

	if err := s.RemoveDevice(token, "tv-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(forgotten) != 1 || forgotten[0] != "tv-1" {
		t.Errorf("hook = %v", forgotten)
	}

	// Removing something that is not there must stay silent — the hook drives a
	// profile reset, and firing it on a no-op would log people out of nothing.
	forgotten = nil
	_ = s.RemoveDevice(token, "never-existed")
	if len(forgotten) != 0 {
		t.Errorf("hook fired for a missing device: %v", forgotten)
	}
}

func TestUnbindAllButtonOnlyWithSeveralDevices(t *testing.T) {
	s, token := deviceStore(t)
	tr := T(LangRU)

	addDev(t, s, token, "tv-1", "TV")
	tok, _ := s.Lookup(token)
	_, kb := devicesView(tok, tr)
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "unbindall:"+token {
				t.Fatal("«Отвязать все» shown for a single device")
			}
		}
	}

	addDev(t, s, token, "phone-2", "Phone")
	tok, _ = s.Lookup(token)
	text, kb := devicesView(tok, tr)
	found := false
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == "unbindall:"+token {
				found = true
			}
		}
	}
	if !found {
		t.Error("«Отвязать все» missing with two devices")
	}
	if text == "" {
		t.Error("empty device list text")
	}
}
