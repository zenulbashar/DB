package domain

import "testing"

func TestCanTransitionResource(t *testing.T) {
	legal := [][2]ResourceState{
		{StateReady, StateSuspending},
		{StateSuspending, StateSuspended},
		{StateSuspending, StateError},
		{StateSuspended, StateResuming},
		{StateResuming, StateReady},
		{StateResuming, StateError},
		{StateReady, StateResizing},
		{StateResizing, StateReady},
		{StateResizing, StateError},
	}
	for _, e := range legal {
		if !CanTransitionResource(e[0], e[1]) {
			t.Errorf("expected %s → %s to be legal", e[0], e[1])
		}
	}

	illegal := [][2]ResourceState{
		{StateReady, StateResuming},  // can't resume a running branch
		{StateReady, StateSuspended}, // must pass through suspending
		{StateSuspended, StateReady}, // must pass through resuming
		{StateSuspended, StateSuspending},
		{StateProvisioning, StateSuspending},
		{StateReady, StateReady}, // self-edge is not a transition
		{StateResuming, StateSuspending},
		{StateDeleting, StateResuming},
	}
	for _, e := range illegal {
		if CanTransitionResource(e[0], e[1]) {
			t.Errorf("expected %s → %s to be illegal", e[0], e[1])
		}
	}
}
