package contentreference

import "testing"

func TestPoliciesKeepFixedAndFollowingIdentitiesDistinct(t *testing.T) {
	for _, test := range []struct {
		link  Link
		valid bool
	}{
		{Link{SetID: "set-1", Policy: "latest_archive"}, true},
		{Link{SetID: "set-1", Policy: "latest_finalized"}, true},
		{Link{SetID: "set-1", Policy: "latest_working", Selection: Selection{Kind: "archive", ID: "run-1"}}, false},
		{Link{SetID: "set-1", Policy: "pinned", Selection: Selection{Kind: "archive", ID: "run-1"}}, true},
		{Link{SetID: "set-1", Policy: "pinned", Selection: Selection{Kind: "revision", ID: "rev-1"}}, true},
		{Link{SetID: "set-1", Policy: "pinned"}, false},
		{Link{SetID: "../secret", Policy: "latest_archive"}, false},
		{Link{SetID: "set-1", Policy: "latest"}, false},
	} {
		if Valid(test.link) != test.valid {
			t.Fatal(test)
		}
	}
	links := Links{Schema: 1, Generation: 1, OperationID: "save", Entries: []Link{{SetID: "set-1", Policy: "latest_archive"}}}
	if !Validate(links) {
		t.Fatal(links)
	}
	links.Entries = append(links.Entries, links.Entries[0])
	if Validate(links) {
		t.Fatal("duplicate reference accepted")
	}
}
