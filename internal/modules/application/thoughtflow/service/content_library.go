package service

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"thoughtflow/internal/pkg/contentlibrary"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/models"
)

func (s *Service) registerContentLibraryRoutes() {
	for _, r := range [][2]string{{"GET", "/api/content-library"}, {"POST", "/api/content-library/sources"}, {"GET", "/api/content-library/sources/:id"}, {"GET", "/api/content-library/sets/:id"}, {"GET", "/api/content-library/sets/:id/markdown"}, {"GET", "/api/topics/:id/content-library"}, {"GET", "/api/topics/:id/content-links"}, {"PUT", "/api/topics/:id/content-links"}} {
		s.registry.AddHandler(r[1], r[0], s.handleContentLibrary)
	}
}

func (s *Service) topicDisplay(ctx context.Context, value any) any {
	if s.workspace == nil {
		return value
	}
	library := contentlibrary.New(s.workspace.RootPath)
	switch v := value.(type) {
	case []models.Topic:
		for i := range v {
			v[i] = library.TopicDisplay(ctx, v[i])
		}
		return v
	case models.Topic:
		return library.TopicDisplay(ctx, v)
	case models.TopicDetail:
		v.Topic = library.TopicDisplay(ctx, v.Topic)
		return v
	case models.CatalogView:
		v.Topic = library.TopicDisplay(ctx, v.Topic)
		return v
	}
	return value
}
func (s *Service) handleContentLibrary(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if s.workspace == nil {
		setError(w, r, ds.ErrUnavailable)
		return
	}
	store := contentlibrary.New(s.workspace.RootPath)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	q := r.URL.Query()
	var value any
	var e error
	if len(parts) == 3 && parts[2] == "sources" && r.Method == "POST" {
		var selection contentlibrary.SourceRequest
		if e = decodeWorkspace(r, &selection); e == nil {
			value, e = store.Sources(ctx, selection)
		}
	} else if len(parts) == 4 && parts[2] == "sources" && r.Method == "GET" {
		value, e = store.ResolveSource(ctx, models.ComposeSource{SourceType: models.ComposeSourceTypeFrozenThought, SourceID: parts[3]})
	} else if len(parts) == 2 {
		page, size := 1, 24
		for k, dst := range map[string]*int{"page": &page, "page_size": &size} {
			if q.Get(k) != "" {
				v, err := strconv.Atoi(q.Get(k))
				if err != nil || v < 1 || v > 1000000 {
					setError(w, r, ds.ErrInvalid)
					return
				}
				*dst = v
			}
		}
		value, e = store.List(ctx, contentlibrary.Query{Layout: q.Get("layout"), Kind: q.Get("kind"), State: q.Get("state"), Search: q.Get("q"), Sort: q.Get("sort"), Page: page, Size: size, Overview: q.Get("overview") == "1"})
	} else if parts[1] == "content-library" && len(parts) == 5 && parts[2] == "sets" && parts[4] == "markdown" {
		var raw []byte
		raw, e = store.Markdown(ctx, parts[3], contentlibrary.Selection{Kind: q.Get("kind"), ID: q.Get("version")})
		if e == nil {
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="`+parts[3]+`-markdown.zip"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
	} else if parts[1] == "content-library" && len(parts) == 4 && parts[2] == "sets" {
		value, e = store.Detail(ctx, parts[3], contentlibrary.Selection{Kind: q.Get("kind"), ID: q.Get("version")})
	} else if parts[1] == "topics" && len(parts) == 4 {
		switch parts[3] {
		case "content-library":
			value, e = store.Topic(ctx, parts[2])
		case "content-links":
			if r.Method == "GET" {
				value, e = store.Links(ctx, parts[2])
			} else {
				var req contentlibrary.LinkRequest
				if e = decodeWorkspace(r, &req); e == nil {
					value, e = store.WriteLinks(ctx, parts[2], req)
					if e == nil {
						s.workspaceChanged(ctx, parts[2], "", "topic.content_links_updated", []string{contentlibrary.LinksPath(parts[2])})
					}
				}
			}
		default:
			e = ds.ErrInvalid
		}
	} else {
		e = ds.ErrInvalid
	}
	if e != nil {
		setError(w, r, e)
		return
	}
	writeJSON(w, r, 200, value)
}
