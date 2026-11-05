// Package contentreference defines persisted topic references to saved sets.
package contentreference

import "thoughtflow/internal/pkg/objectlocation"

type Selection struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type Link struct {
	SetID     string    `json:"set_id"`
	Policy    string    `json:"policy"`
	Selection Selection `json:"selection"`
}
type Links struct {
	Schema      int    `json:"schema_version"`
	Generation  uint64 `json:"generation"`
	OperationID string `json:"operation_id"`
	Entries     []Link `json:"entries"`
}

func Valid(l Link) bool {
	if !objectlocation.ValidID(l.SetID) {
		return false
	}
	switch l.Policy {
	case "latest_archive", "latest_working", "latest_finalized":
		return l.Selection == (Selection{})
	case "pinned":
		return (l.Selection.Kind == "archive" || l.Selection.Kind == "revision") && objectlocation.ValidID(l.Selection.ID)
	}
	return false
}

func Validate(links Links) bool {
	if links.Schema != 1 || links.Generation == 0 || !objectlocation.ValidID(links.OperationID) || len(links.Entries) > 500 {
		return false
	}
	seen := map[string]bool{}
	for _, link := range links.Entries {
		if seen[link.SetID] || !Valid(link) {
			return false
		}
		seen[link.SetID] = true
	}
	return true
}
