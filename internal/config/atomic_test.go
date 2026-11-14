package config

import (
	"errors"
	"testing"
)

type atomicProbe struct {
	writeErr, errorSync error
	short               bool
	synced              bool
}

func (p *atomicProbe) Write(b []byte) (int, error) {
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	if p.short {
		return len(b) - 1, nil
	}
	return len(b), nil
}
func (p *atomicProbe) Sync() error { p.synced = true; return p.errorSync }
func TestControlAtomicWriteStages(t *testing.T) {
	boom := errors.New("injected")
	for _, tc := range []struct {
		p     *atomicProbe
		stage string
		sync  bool
	}{{&atomicProbe{writeErr: boom}, "write", false}, {&atomicProbe{short: true}, "write", false}, {&atomicProbe{errorSync: boom}, "sync", true}, {&atomicProbe{}, "", true}} {
		stage, e := writeAndSync(tc.p, []byte("value"))
		if stage != tc.stage || (e != nil) != (tc.stage != "") || tc.p.synced != tc.sync {
			t.Fatalf("stage %q error %v sync %v", stage, e, tc.p.synced)
		}
	}
}

func TestControlAtomicReplaceFailurePreservesState(t *testing.T) {
	t.Setenv("CURIOUS_CONFIG", controlTestPath(t))
	old := controlReplace
	defer func() { controlReplace = old }()
	err := ControlWithLock("https://example.test", func(s *ControlStore) error {
		state, e := s.Load()
		if e != nil {
			return e
		}
		state.Pending["old"] = ControlPending{Handle: "old", Kind: "cli", Proof: "synthetic"}
		if e = s.Save(state); e != nil {
			return e
		}
		controlReplace = func(string, string) error { return errors.New("injected replace failure") }
		state.Pending["new"] = ControlPending{Handle: "new", Kind: "cli", Proof: "synthetic"}
		if e = s.Save(state); e == nil {
			return errors.New("replace failure was ignored")
		}
		saved, e := s.Load()
		if e != nil {
			return e
		}
		if len(saved.Pending) != 1 || saved.Pending["old"].Handle != "old" {
			return errors.New("original state changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
