package calendar

import "testing"

func TestRegisterIsIdempotent(t *testing.T) {
	p := NewPushTokens(t.TempDir())
	p.Register(1, PushToken{Token: "abc", Platform: "android"})
	p.Register(1, PushToken{Token: "abc", Platform: "android"})

	if got := p.Count(1); got != 1 {
		t.Fatalf("re-registering the same token duplicated it: %d entries", got)
	}
}

// TestTokenMovesToTheNewOwner: an FCM token belongs to an app install, not to an
// account. Someone signing in on a box that was already bound must take the
// token over — otherwise that device keeps buzzing with the previous account's
// subscriptions.
func TestTokenMovesToTheNewOwner(t *testing.T) {
	p := NewPushTokens(t.TempDir())
	p.Register(1, PushToken{Token: "abc"})
	p.Register(2, PushToken{Token: "abc"})

	if got := p.TokensOf(1); len(got) != 0 {
		t.Fatalf("the previous owner still receives push: %v", got)
	}
	if got := p.TokensOf(2); len(got) != 1 || got[0] != "abc" {
		t.Fatalf("the new owner did not get the token: %v", got)
	}
}

func TestUnregisterDropsEverywhere(t *testing.T) {
	p := NewPushTokens(t.TempDir())
	p.Register(1, PushToken{Token: "abc"})
	p.Register(1, PushToken{Token: "def"})

	p.Unregister("abc")

	got := p.TokensOf(1)
	if len(got) != 1 || got[0] != "def" {
		t.Fatalf("want only the surviving token, got %v", got)
	}
}

// TestTokensAreCappedPerUser: a client bug that mints a fresh token every launch
// must not turn one alert into an unbounded fan-out.
func TestTokensAreCappedPerUser(t *testing.T) {
	p := NewPushTokens(t.TempDir())
	for i := range maxTokensPerUser + 10 {
		p.Register(1, PushToken{Token: string(rune('a' + i%26)) + string(rune('0'+i/26))})
	}
	if got := p.Count(1); got > maxTokensPerUser {
		t.Fatalf("cap not enforced: %d tokens", got)
	}
}

// TestRegistryPersists: a restart must not silently stop push for everyone.
func TestRegistryPersists(t *testing.T) {
	dir := t.TempDir()
	p := NewPushTokens(dir)
	p.Register(7, PushToken{Token: "abc", Platform: "android"})

	reloaded := NewPushTokens(dir)
	got := reloaded.TokensOf(7)
	if len(got) != 1 || got[0] != "abc" {
		t.Fatalf("registry did not survive a reload: %v", got)
	}
}

func TestUnregisterDeviceDropsItsToken(t *testing.T) {
	p := NewPushTokens(t.TempDir())
	p.Register(1, PushToken{Token: "abc", DeviceUID: "box-1"})
	p.Register(1, PushToken{Token: "def", DeviceUID: "phone-2"})

	p.UnregisterDevice(1, "box-1")

	got := p.TokensOf(1)
	if len(got) != 1 || got[0] != "def" {
		t.Fatalf("unbinding a device left the wrong tokens: %v", got)
	}
}
