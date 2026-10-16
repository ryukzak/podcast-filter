package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Rule is a single filtering step: keep items whose title matches Re, or drop
// them instead when Neg is set.
type Rule struct {
	Re  string `json:"re"`
	Neg bool   `json:"neg"`
}

// Filter is the complete description of a filtered feed. It is what we encode
// into a /filter URL and what we store in bbolt behind a GUID.
type Filter struct {
	Feed  string `json:"feed"`
	Title string `json:"title"`
	Rules []Rule `json:"rules"`
}

// FilterFromQuery reads the historical query format: feed, title, repeated re
// and optional repeated neg.
func FilterFromQuery(q url.Values) (Filter, error) {
	res := q["re"]
	negs := q["neg"]
	if len(negs) > 0 && len(res) != len(negs) {
		return Filter{}, fmt.Errorf("number of negs should be equal to REs, if not empty")
	}

	f := Filter{
		Feed:  q.Get("feed"),
		Title: q.Get("title"),
		Rules: make([]Rule, 0, len(res)),
	}
	for i, re := range res {
		negative := false
		if len(negs) > 0 {
			negative = negs[i] == "true"
		}
		f.Rules = append(f.Rules, Rule{Re: re, Neg: negative})
	}

	return f, nil
}

// Query encodes the filter back into the query format understood by /filter.
// neg is emitted for every rule or for none at all, so that the result stays
// readable by older clients.
func (f Filter) Query() url.Values {
	q := url.Values{}
	q.Set("feed", f.Feed)
	if f.Title != "" {
		q.Set("title", f.Title)
	}

	anyNegative := false
	for _, rule := range f.Rules {
		if rule.Neg {
			anyNegative = true
		}
	}

	for _, rule := range f.Rules {
		q.Add("re", rule.Re)
		if anyNegative {
			q.Add("neg", fmt.Sprintf("%t", rule.Neg))
		}
	}

	return q
}

// Validate reports a per-rule compile error, with an empty string for the rules
// that are fine. An invalid rule is not fatal: the builder keeps working while
// the user is still typing a pattern.
func (f Filter) Validate() []string {
	errs := make([]string, len(f.Rules))
	for i, rule := range f.Rules {
		if strings.TrimSpace(rule.Re) == "" {
			errs[i] = "pattern is empty"
			continue
		}
		if _, err := regexp.Compile(rule.Re); err != nil {
			errs[i] = err.Error()
		}
	}
	return errs
}

// Compile returns one regexp per rule. Rules that do not compile are reported
// through the error.
func (f Filter) Compile() ([]*regexp.Regexp, error) {
	regexes := make([]*regexp.Regexp, 0, len(f.Rules))
	for _, rule := range f.Rules {
		regex, err := regexp.Compile(rule.Re)
		if err != nil {
			return nil, fmt.Errorf("can't compile %q: %w", rule.Re, err)
		}
		regexes = append(regexes, regex)
	}
	return regexes, nil
}

// Apply keeps the items the rules accept, see accepts for how the rules
// combine.
func (f Filter) Apply(items []Item, regexes []*regexp.Regexp) []Item {
	filtered := make([]Item, 0, len(items))
	for _, item := range items {
		if f.accepts(item, regexes) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// accepts combines the rules the way an include/exclude list does: an episode
// has to match at least one keep rule, and must not match any drop rule.
//
// Keep rules are OR'd rather than AND'd because asking for two of them almost
// always means "either of these", not "both at once" — two names AND'd together
// match the handful of episodes featuring both, which is rarely the intent.
// Drop rules stay mandatory: OR'ing them in would make them no-ops, since any
// other rule accepting the episode would overrule the exclusion.
//
// A filter with no keep rules keeps everything the drop rules do not remove.
func (f Filter) accepts(item Item, regexes []*regexp.Regexp) bool {
	keepSeen, keepMatched := false, false

	for i, regex := range regexes {
		matched := regex.MatchString(item.Title)
		if f.Rules[i].Neg {
			if matched {
				return false
			}
			continue
		}
		keepSeen = true
		keepMatched = keepMatched || matched
	}

	return !keepSeen || keepMatched
}
