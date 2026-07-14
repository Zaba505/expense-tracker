package view

import (
	"encoding/json"
	"testing"
)

// TestHTMXConfig guards the one thing that makes a refused entry visible.
//
// htmx JSON.parses the meta tag's content and falls back to its defaults if it
// cannot — silently, in the browser, where no Go test would ever see it. The
// default drops 4xx bodies on the floor, so a typo in this string would not
// break the page: it would just quietly stop showing people why their entry was
// refused, while the server went on answering 422 with a perfectly good
// explanation nobody swaps in.
func TestHTMXConfig(t *testing.T) {
	t.Parallel()

	var config struct {
		ResponseHandling []struct {
			Code string `json:"code"`
			Swap bool   `json:"swap"`
		} `json:"responseHandling"`
	}
	if err := json.Unmarshal([]byte(htmxConfig), &config); err != nil {
		t.Fatalf("htmx-config is not JSON, so htmx will ignore all of it: %v\n%s", err, htmxConfig)
	}

	// The rules are tried in order and the first match wins, so the 422 has to
	// come before the [45].. that would otherwise catch it and swap nothing.
	for _, rule := range config.ResponseHandling {
		switch rule.Code {
		case "422":
			if !rule.Swap {
				t.Fatal("the 422 rule does not swap; a refused entry would answer with its reasons and show none of them")
			}
			return
		case "[45]..":
			t.Fatal("the [45].. rule is matched before the 422 one, which makes the 422 rule dead: a refused entry would show no reasons")
		}
	}

	t.Fatal("no rule for 422, so htmx swaps nothing when an entry is refused")
}
