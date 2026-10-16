package main

import (
	"net/url"
	"strings"
	"testing"
)

func items(titles ...string) []Item {
	result := make([]Item, 0, len(titles))
	for _, title := range titles {
		result = append(result, Item{Title: title})
	}
	return result
}

func titles(list []Item) []string {
	result := make([]string, 0, len(list))
	for _, item := range list {
		result = append(result, item.Title)
	}
	return result
}

func TestFilterFromQuery(t *testing.T) {
	q, err := url.ParseQuery("feed=http://a/b&title=T&re=x&neg=true&re=y&neg=false")
	if err != nil {
		t.Fatal(err)
	}

	filter, err := FilterFromQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	if filter.Feed != "http://a/b" || filter.Title != "T" {
		t.Fatalf("unexpected head: %+v", filter)
	}
	want := []Rule{{Re: "x", Neg: true}, {Re: "y", Neg: false}}
	if len(filter.Rules) != 2 || filter.Rules[0] != want[0] || filter.Rules[1] != want[1] {
		t.Fatalf("rules = %+v, want %+v", filter.Rules, want)
	}
}

func TestFilterFromQueryWithoutNeg(t *testing.T) {
	q, _ := url.ParseQuery("feed=http://a&re=x&re=y")
	filter, err := FilterFromQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	for i, rule := range filter.Rules {
		if rule.Neg {
			t.Fatalf("rule %d should default to keep", i)
		}
	}
}

func TestFilterFromQueryMismatchedNeg(t *testing.T) {
	q, _ := url.ParseQuery("feed=http://a&re=x&re=y&neg=true")
	if _, err := FilterFromQuery(q); err == nil {
		t.Fatal("expected an error for mismatched neg count")
	}
}

func TestQueryRoundTrip(t *testing.T) {
	original := Filter{
		Feed:  "https://example.com/rss",
		Title: "Эхо (часть)",
		Rules: []Rule{{Re: "(?i)(LIVE)", Neg: true}, {Re: "(?i)(Шульман)", Neg: false}},
	}

	q, err := url.ParseQuery(original.Query().Encode())
	if err != nil {
		t.Fatal(err)
	}
	back, err := FilterFromQuery(q)
	if err != nil {
		t.Fatal(err)
	}

	if back.Feed != original.Feed || back.Title != original.Title {
		t.Fatalf("head lost: %+v", back)
	}
	for i := range original.Rules {
		if back.Rules[i] != original.Rules[i] {
			t.Fatalf("rule %d = %+v, want %+v", i, back.Rules[i], original.Rules[i])
		}
	}
}

// A filter with only positive rules must not emit neg at all, so that the URL
// stays compatible with the format documented before this change.
func TestQueryOmitsNegWhenAllPositive(t *testing.T) {
	f := Filter{Feed: "https://a", Rules: []Rule{{Re: "x"}, {Re: "y"}}}
	if negs := f.Query()["neg"]; len(negs) != 0 {
		t.Fatalf("neg = %v, want none", negs)
	}
}

func apply(t *testing.T, f Filter, list ...string) []string {
	t.Helper()
	regexes, err := f.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return titles(f.Apply(items(list...), regexes))
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestApply(t *testing.T) {
	f := Filter{Rules: []Rule{{Re: "(?i)live", Neg: true}, {Re: "(?i)шульман"}}}

	got := apply(t, f, "Шульман про всё", "LIVE: Шульман", "Шульман LIVE", "Кто-то другой")
	if want := []string{"Шульман про всё"}; !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Two keep rules mean "either of these", not "both at once".
func TestApplyKeepRulesAreOred(t *testing.T) {
	f := Filter{Rules: []Rule{{Re: "(?i)шульман"}, {Re: "(?i)наки"}}}

	got := apply(t, f, "Шульман про всё", "Наки про всё", "Шульман и Наки", "Кто-то другой")
	want := []string{"Шульман про всё", "Наки про всё", "Шульман и Наки"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A drop rule has to survive the OR: it removes an episode even when another
// keep rule accepted it.
func TestApplyDropRuleOverridesKeep(t *testing.T) {
	f := Filter{Rules: []Rule{
		{Re: "(?i)шульман"},
		{Re: "(?i)наки"},
		{Re: "(?i)live", Neg: true},
	}}

	got := apply(t, f, "Шульман про всё", "LIVE: Наки", "Наки про всё", "Кто-то другой")
	want := []string{"Шульман про всё", "Наки про всё"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Several drop rules all have to pass, so they still behave as an AND.
func TestApplyDropRulesAreAnded(t *testing.T) {
	f := Filter{Rules: []Rule{{Re: "(?i)live", Neg: true}, {Re: "(?i)повтор", Neg: true}}}

	got := apply(t, f, "Шульман про всё", "LIVE: Наки", "Повтор выпуска", "Кто-то другой")
	want := []string{"Шульман про всё", "Кто-то другой"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// With no keep rule there is nothing to opt into, so everything the drop rules
// leave alone stays.
func TestApplyWithOnlyDropRulesKeepsTheRest(t *testing.T) {
	f := Filter{Rules: []Rule{{Re: "(?i)live", Neg: true}}}

	got := apply(t, f, "LIVE: Наки", "Кто-то другой")
	if want := []string{"Кто-то другой"}; !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Order must not matter, whichever mix of rules is used.
func TestApplyIsOrderIndependent(t *testing.T) {
	list := []string{"Шульман про всё", "LIVE: Наки", "Наки про всё", "Кто-то другой"}
	rules := []Rule{{Re: "(?i)шульман"}, {Re: "(?i)наки"}, {Re: "(?i)live", Neg: true}}

	first := apply(t, Filter{Rules: rules}, list...)
	shuffled := []Rule{rules[2], rules[1], rules[0]}
	second := apply(t, Filter{Rules: shuffled}, list...)

	if !equal(first, second) {
		t.Fatalf("%v != %v", first, second)
	}
}

func TestApplyWithoutRulesKeepsEverything(t *testing.T) {
	f := Filter{}
	regexes, _ := f.Compile()
	if got := f.Apply(items("a", "b"), regexes); len(got) != 2 {
		t.Fatalf("got %v, want both items", titles(got))
	}
}

func TestValidate(t *testing.T) {
	f := Filter{Rules: []Rule{{Re: "ok"}, {Re: "(unclosed"}, {Re: "  "}}}
	errs := f.Validate()
	if errs[0] != "" {
		t.Fatalf("rule 0 should be valid, got %q", errs[0])
	}
	if errs[1] == "" || errs[2] == "" {
		t.Fatalf("rules 1 and 2 should be invalid, got %q and %q", errs[1], errs[2])
	}
}

func TestCompileReportsBadPattern(t *testing.T) {
	f := Filter{Rules: []Rule{{Re: "(unclosed"}}}
	if _, err := f.Compile(); err == nil {
		t.Fatal("expected a compile error")
	}
}

// The builder decides between an inline and a stored link by the length of the
// encoded URL, so a Cyrillic pattern has to be measured after escaping, where
// every letter costs six characters rather than one.
func TestQueryLengthCountsEscaping(t *testing.T) {
	f := Filter{Feed: "https://a.example/rss", Rules: []Rule{{Re: "Шульман"}}}
	encoded := f.Query().Encode()

	if want := "re=%D0%A8%D1%83%D0%BB%D1%8C%D0%BC%D0%B0%D0%BD"; !strings.Contains(encoded, want) {
		t.Fatalf("encoded = %q, want it to contain %q", encoded, want)
	}
	// 7 Cyrillic letters at 6 characters each, not 7.
	if got := len(encoded) - len("feed=https%3A%2F%2Fa.example%2Frss&re="); got != 42 {
		t.Fatalf("escaped pattern is %d characters, want 42", got)
	}
}
