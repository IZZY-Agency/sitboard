package model

import "testing"

func TestKnownZeroValueIsUnknown(t *testing.T) {
	if (Known[string]{}).OK {
		t.Fatal("zero Known[string] must be unknown")
	}
	if (Known[Status]{}).OK || (Known[bool]{}).OK || (Known[SessionKey]{}).OK {
		t.Fatal("zero Known of any type must be unknown")
	}
}

func TestOfAndUnknown(t *testing.T) {
	k := Of(StatusBusy)
	if !k.OK || k.V != StatusBusy {
		t.Fatalf("Of(busy) = %+v", k)
	}
	if u := Unknown[string](); u.OK {
		t.Fatal("Unknown() reports a value")
	}
	if v, ok := Of("x").Get(); !ok || v != "x" {
		t.Fatalf("Get = %q, %v", v, ok)
	}
}

func TestFactsImplementFact(t *testing.T) {
	facts := []Fact{
		SessionFact{},
		SessionGone{},
		TerminalFact{},
		TerminalGone{},
		TerminalSelected{},
		SourceReady{},
	}
	if len(facts) != 6 {
		t.Fatal("unexpected fact count")
	}
}
