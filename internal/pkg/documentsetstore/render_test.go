package documentsetstore

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/parser"
)

func TestOfflineAnchorsMatchUnicodeReaderAndValidation(t *testing.T) {
	md := goldmark.New(goldmark.WithParserOptions(parser.WithAutoHeadingID()))
	var out bytes.Buffer
	if e := md.Convert([]byte("# 数据 结构\n\n## 数据 结构\n"), &out, parser.WithContext(parser.NewContext(parser.WithIDs(headingIDs{})))); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{`id="数据-结构"`, `id="数据-结构-1"`} {
		if !strings.Contains(out.String(), id) {
			t.Fatal(out.String())
		}
	}
}
