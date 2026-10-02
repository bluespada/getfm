package free

import (
	"testing"

	"github.com/bluespada/getfm/pkg/provider"
)

func model(id string, priced bool, in, out float64) provider.Model {
	return provider.Model{ID: id, Priced: priced, PromptPrice: in, CompletionPrice: out}
}

func TestParseSpecRejectsUnknownRule(t *testing.T) {
	if _, err := ParseSpec("magic", ":free", "", ""); err == nil {
		t.Fatal("expected an error for an unknown rule")
	}
}

func TestSuffixRule(t *testing.T) {
	r, err := ParseSpec("suffix", ":free", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := r.Match(model("a/b:free", true, 0, 0), Facts{}); !ok || reason != ReasonSuffix {
		t.Errorf("suffix model: ok=%v reason=%q", ok, reason)
	}
	if ok, _ := r.Match(model("a/b", true, 0, 0), Facts{}); ok {
		t.Error("a paid model matched the suffix rule")
	}
}

func TestZeroPriceRuleRequiresPublishedPricing(t *testing.T) {
	r, err := ParseSpec("zero", ":free", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Published as zero on both sides.
	if ok, reason := r.Match(model("free/no-price", true, 0, 0), Facts{}); !ok || reason != ReasonZero {
		t.Errorf("zero-price model: ok=%v reason=%q", ok, reason)
	}
	// No pricing at all: absence of pricing is not evidence of being free.
	if ok, _ := r.Match(model("unknown/pricing", false, 0, 0), Facts{}); ok {
		t.Error("a model with unpublished pricing matched the zero rule")
	}
	// Zero in but not out is still a charge.
	if ok, _ := r.Match(model("half/price", true, 0, 0.5), Facts{}); ok {
		t.Error("a model priced on output matched the zero rule")
	}
}

func TestFreeAlwaysProvider(t *testing.T) {
	r, _ := ParseSpec("suffix", ":free", "", "")
	if ok, reason := r.Match(model("local/llama", false, 0, 0), Facts{FreeAlways: true}); !ok || reason != ReasonProvider {
		t.Errorf("free_always: ok=%v reason=%q", ok, reason)
	}
}

// TestSpecNamesOnlyTheRulesItApplies pins the meaning of the -free value: a
// spec switches on exactly the rules it names, so "zero" is the zero price
// rule alone rather than everything.
func TestSpecNamesOnlyTheRulesItApplies(t *testing.T) {
	// A model advertising free access in its id, and one advertised by price.
	suffixModel := model("vendor/paid:free", true, 5, 5)
	zeroModel := model("vendor/free-priced", true, 0, 0)

	cases := []struct {
		spec        string
		wantsSuffix bool
		wantsZero   bool
	}{
		{"suffix", true, false},
		{"zero", false, true},
		{"price", false, true},
		{"all", true, true},
		{"suffix,zero", true, true},
		{"zero,suffix", true, true},
		{"none", false, false},
		{"", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			r, err := ParseSpec(tc.spec, ":free", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if ok, reason := r.Match(suffixModel, Facts{}); ok != tc.wantsSuffix {
				t.Errorf("suffix model: matched=%v reason=%q, want matched=%v", ok, reason, tc.wantsSuffix)
			}
			if ok, reason := r.Match(zeroModel, Facts{}); ok != tc.wantsZero {
				t.Errorf("zero price model: matched=%v reason=%q, want matched=%v", ok, reason, tc.wantsZero)
			}
		})
	}
}

func TestSpecSuffixRuleNeedsASuffix(t *testing.T) {
	for _, spec := range []string{"suffix", "all"} {
		if _, err := ParseSpec(spec, "", "", ""); err == nil {
			t.Errorf("%q with an empty -suffix should be rejected", spec)
		}
	}
	// The zero rule does not read the suffix, so it must not demand one.
	if _, err := ParseSpec("zero", "", "", ""); err != nil {
		t.Errorf("zero with an empty -suffix should be accepted, got %v", err)
	}
}

func TestAllowAndDenyApplyWhateverTheSpec(t *testing.T) {
	for _, spec := range []string{"none", ""} {
		r, err := ParseSpec(spec, ":free", "vendor/allowed", "vendor/denied")
		if err != nil {
			t.Fatal(err)
		}
		if ok, reason := r.Match(model("vendor/allowed", false, 1, 1), Facts{}); !ok || reason != ReasonAllow {
			t.Errorf("spec %q: allowed model matched=%v reason=%q", spec, ok, reason)
		}
		if ok, _ := r.Match(model("vendor/denied", true, 0, 0), Facts{}); ok {
			t.Errorf("spec %q: deny should still win", spec)
		}
	}
}

func TestDenyBeatsAllow(t *testing.T) {
	r, err := ParseSpec("all", ":free", "vendor/a:free, vendor/b:free", "vendor/b:free")
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := r.Match(model("vendor/a:free", true, 0, 0), Facts{}); !ok || reason != ReasonAllow {
		t.Errorf("allowed model: ok=%v reason=%q", ok, reason)
	}
	if ok, _ := r.Match(model("vendor/b:free", true, 0, 0), Facts{}); ok {
		t.Error("deny did not override allow")
	}
}

func TestExactEntriesDoNotMatchVariants(t *testing.T) {
	// "vendor/b" and "vendor/b:free" are distinct models, so denying one must
	// not silently exclude the other.
	r, err := ParseSpec("suffix", ":free", "", "vendor/b")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.Match(model("vendor/b:free", true, 0, 0), Facts{}); !ok {
		t.Error("exact deny leaked onto the :free variant; use a wildcard instead")
	}
}

func TestWildcardsAndCase(t *testing.T) {
	r, err := ParseSpec("suffix", ":free", "", "nvidia/*, *Nemotron*")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"nvidia/anything:free", "vendor/NEMOTRON-3:free"} {
		if ok, _ := r.Match(model(id, true, 0, 0), Facts{}); ok {
			t.Errorf("%q should have been denied", id)
		}
	}
	if ok, _ := r.Match(model("qwen/qwen3:free", true, 0, 0), Facts{}); !ok {
		t.Error("unrelated model was denied")
	}
}

func TestNoneDisablesImplicitRules(t *testing.T) {
	r, err := ParseSpec("none", ":free", "vendor/a", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.Match(model("x/y:free", true, 0, 0), Facts{}); ok {
		t.Error("-free none should not match anything implicitly")
	}
	if ok, _ := r.Match(model("vendor/a", false, 0, 0), Facts{}); !ok {
		t.Error("explicit allow should still apply under -free none")
	}
}
