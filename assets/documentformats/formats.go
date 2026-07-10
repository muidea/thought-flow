package documentformats

import _ "embed"

//go:embed note-v1.md
var NoteV1 string

//go:embed research-report-v1.md
var ResearchReportV1 string

//go:embed design-doc-v1.md
var DesignDocV1 string

//go:embed blog-post-v1.md
var BlogPostV1 string

func Builtins() []string {
	return []string{NoteV1, ResearchReportV1, DesignDocV1, BlogPostV1}
}
