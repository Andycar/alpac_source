package httpclient

import (
	"reflect"
	"testing"
)

// resetFlareReg wipes the package-global registry so tests don't bleed into
// each other. Safe because tests in this package run serially per-package.
func resetFlareReg() {
	flareReg.Lock()
	flareReg.url = ""
	flareReg.system = make(map[string]struct{})
	flareReg.user = make(map[string]struct{})
	flareReg.Unlock()
}

func TestFlareSolverrApplyReplacesUserSet(t *testing.T) {
	resetFlareReg()
	ApplyUserFlareSolverrBalancers([]string{"kinogo", "vdbmovies"})
	if !IsBalancerFlareSolverr("kinogo") || !IsBalancerFlareSolverr("vdbmovies") {
		t.Fatalf("initial apply: expected both balancers registered")
	}
	// Re-apply with a shorter list — the removed one must actually drop out.
	ApplyUserFlareSolverrBalancers([]string{"kinogo"})
	if !IsBalancerFlareSolverr("kinogo") {
		t.Fatalf("kinogo should still be registered")
	}
	if IsBalancerFlareSolverr("vdbmovies") {
		t.Fatalf("vdbmovies should have been dropped by the second Apply")
	}
}

func TestFlareSolverrSystemSurvivesUserApply(t *testing.T) {
	resetFlareReg()
	RegisterFlareSolverrBalancerSystem("turbo")
	ApplyUserFlareSolverrBalancers([]string{"kinogo"})
	if !IsBalancerFlareSolverr("turbo") {
		t.Fatalf("system entry must survive user-set Apply (this is the whole point of the split)")
	}
	// And another reload that wipes user shouldn't touch system either.
	ApplyUserFlareSolverrBalancers(nil)
	if !IsBalancerFlareSolverr("turbo") {
		t.Fatalf("system entry must survive empty user Apply")
	}
	if IsBalancerFlareSolverr("kinogo") {
		t.Fatalf("user entry should be wiped by empty Apply")
	}
}

func TestFlareSolverrUnregisterDoesNotTouchSystem(t *testing.T) {
	resetFlareReg()
	RegisterFlareSolverrBalancerSystem("turbo")
	// Admin trying to remove the system entry via the user-facing API must
	// not actually wipe it — defense against a malicious or buggy UI PUT.
	UnregisterFlareSolverrBalancer("turbo")
	if !IsBalancerFlareSolverr("turbo") {
		t.Fatalf("Unregister must not touch the system set")
	}
}

func TestFlareSolverrAnnotatedSortsSystemFirst(t *testing.T) {
	resetFlareReg()
	RegisterFlareSolverrBalancerSystem("turbo")
	ApplyUserFlareSolverrBalancers([]string{"vdbmovies", "kinogo"})
	got := ListFlareSolverrBalancersAnnotated()
	want := []FlareSolverrBalancerEntry{
		{Name: "turbo", System: true},
		{Name: "kinogo", System: false},
		{Name: "vdbmovies", System: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("annotated list mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestFlareSolverrNamesAreLowercased(t *testing.T) {
	resetFlareReg()
	ApplyUserFlareSolverrBalancers([]string{"  KinoGo  ", "VDBmovies"})
	if !IsBalancerFlareSolverr("kinogo") || !IsBalancerFlareSolverr("vdbmovies") {
		t.Fatalf("apply should normalize whitespace and case")
	}
}
