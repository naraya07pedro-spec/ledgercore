package ledger

import "testing"

func TestPostingValidation(t *testing.T) {
	valid := Posting{Currency: "USD", Entries: []Entry{{DemoTreasury, -20}, {DemoOperating, 20}}}
	cases := map[string]struct {
		posting Posting
		want    bool
	}{
		"balanced":             {valid, true},
		"unbalanced":           {Posting{Currency: "USD", Entries: []Entry{{DemoTreasury, -20}, {DemoOperating, 21}}}, false},
		"repeat account":       {Posting{Currency: "USD", Entries: []Entry{{DemoTreasury, -20}, {DemoTreasury, 20}}}, false},
		"unsupported currency": {Posting{Currency: "GBP", Entries: valid.Entries}, false},
		"zero":                 {Posting{Currency: "USD", Entries: []Entry{{DemoTreasury, 0}, {DemoOperating, 0}}}, false},
		"overflow bound":       {Posting{Currency: "USD", Entries: []Entry{{DemoTreasury, -9000000000000001}, {DemoOperating, 9000000000000001}}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if validate(&tc.posting) != tc.want {
				t.Fatal("unexpected validation outcome")
			}
		})
	}
}
