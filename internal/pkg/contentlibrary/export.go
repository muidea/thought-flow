package contentlibrary

import (
	"archive/zip"
	"bytes"
	"context"
	"path/filepath"
	"strings"

	ds "thoughtflow/internal/pkg/documentset"
)

// Markdown exports fixed saved originals. It is a reading download, not a
// delivery approval, resource bundle, or a backup of the workspace.
func (s *Store) Markdown(ctx context.Context, id string, selection Selection) ([]byte, error) {
	d, err := s.Detail(ctx, id, selection)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	seen := map[string]bool{}
	total := 0
	for _, group := range []struct {
		name    string
		members []Member
	}{{"documents", d.Members}, {"sources", d.Sources}} {
		for _, member := range group.members {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			name := member.Filename
			if name == "" {
				name = strings.ReplaceAll(member.Key, ":", "-") + ".md"
			}
			if filepath.Base(name) != name || strings.ContainsAny(name, "/\\") || !strings.HasSuffix(strings.ToLower(name), ".md") {
				return nil, ds.ErrInvalid
			}
			name = group.name + "/" + name
			if seen[name] {
				return nil, ds.ErrInvalid
			}
			seen[name] = true
			total += len(member.Body)
			if total > 64<<20 {
				return nil, ds.ErrBudget
			}
			file, e := writer.Create(name)
			if e != nil {
				return nil, e
			}
			if _, e = file.Write([]byte(member.Body)); e != nil {
				return nil, e
			}
		}
	}
	notice, err := writer.Create("READING-NOTICE.txt")
	if err != nil {
		return nil, err
	}
	if _, err = notice.Write([]byte("Fixed saved Markdown originals. This reading download is not a formal delivery approval or a workspace backup. Linked attachments are not included.\n固定版本 Markdown 原件，仅供阅读；不代表正式交付批准，不是工作区备份；链接附件未包含。\nSet: " + id + "\nVersion: " + d.Selection.Kind + "/" + d.Selection.ID + "\n")); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
