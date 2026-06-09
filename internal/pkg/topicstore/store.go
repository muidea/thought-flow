package topicstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/workspace"
)

type Store struct {
	rootPath string
	weaver   WeaveProvider
}

type MatchFunc func(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent) (models.TopicMembership, bool)

type WeaveProvider interface {
	Weave(ctx context.Context, req models.TopicWeaveRequest) (models.TopicWeaveResult, error)
}

type Option func(*Store)

func WithWeaveProvider(provider WeaveProvider) Option {
	return func(s *Store) {
		s.weaver = provider
	}
}

func New(rootPath string, options ...Option) *Store {
	store := &Store{rootPath: rootPath}
	for _, option := range options {
		option(store)
	}
	return store
}

func (s *Store) Create(ctx context.Context, req models.TopicCreateRequest) (models.Topic, error) {
	_ = ctx
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return models.Topic{}, errors.New("topic name is required")
	}
	slug := Slugify(name)
	if slug == "" {
		return models.Topic{}, errors.New("topic slug is empty")
	}
	if _, err := s.Get(ctx, slug); err == nil {
		return models.Topic{}, fmt.Errorf("topic %s already exists", slug)
	}
	now := time.Now().UTC()
	autoWeave := true
	if req.AutoWeave != nil {
		autoWeave = *req.AutoWeave
	}
	topic := models.Topic{
		ID:          slug,
		Name:        name,
		Slug:        slug,
		Description: strings.TrimSpace(req.Description),
		Rules:       normalizeRule(req.Rules),
		Outline:     req.Outline,
		AutoWeave:   autoWeave,
		CreatedAt:   now,
		UpdatedAt:   now,
		Members:     []string{},
	}
	if err := s.writeTopic(topic); err != nil {
		return models.Topic{}, err
	}
	if err := s.writeDocument(topic, initialDocument(topic)); err != nil {
		return models.Topic{}, err
	}
	return topic, nil
}

func (s *Store) Update(ctx context.Context, id string, req models.TopicUpdateRequest) (models.Topic, error) {
	_ = ctx
	topic, err := s.Get(ctx, id)
	if err != nil {
		return models.Topic{}, err
	}
	if strings.TrimSpace(req.Name) != "" {
		topic.Name = strings.TrimSpace(req.Name)
	}
	topic.Description = strings.TrimSpace(req.Description)
	topic.Rules = normalizeRule(req.Rules)
	topic.Outline = req.Outline
	if req.AutoWeave != nil {
		topic.AutoWeave = *req.AutoWeave
	}
	topic.UpdatedAt = time.Now().UTC()
	if err := s.writeTopic(topic); err != nil {
		return models.Topic{}, err
	}
	return topic, nil
}

func (s *Store) Get(ctx context.Context, id string) (models.Topic, error) {
	_ = ctx
	path, err := s.topicPath(id)
	if err != nil {
		return models.Topic{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return models.Topic{}, err
	}
	var topic models.Topic
	if err := yaml.Unmarshal(raw, &topic); err != nil {
		return models.Topic{}, err
	}
	return topic, nil
}

func (s *Store) Detail(ctx context.Context, id string) (models.TopicDetail, error) {
	topic, err := s.Get(ctx, id)
	if err != nil {
		return models.TopicDetail{}, err
	}
	document, _ := s.ReadDocument(ctx, topic.ID)
	memberships, err := s.detailMemberships(topic, document)
	if err != nil {
		return models.TopicDetail{}, err
	}
	return models.TopicDetail{Topic: topic, Document: document, Members: memberships}, nil
}

func (s *Store) detailMemberships(topic models.Topic, document string) ([]models.TopicMembership, error) {
	facts, err := s.readMemberships(topic)
	if err != nil {
		return nil, err
	}
	factByThought := map[string]models.TopicMembership{}
	for _, membership := range facts {
		factByThought[membership.ThoughtID] = membership
	}
	if len(topic.Members) == 0 && len(facts) > 0 {
		return facts, nil
	}
	memberships := make([]models.TopicMembership, 0, len(topic.Members))
	for _, thoughtID := range topic.Members {
		membership, ok := factByThought[thoughtID]
		if !ok {
			membership = inferDocumentMembership(document, topic.ID, thoughtID)
			if membership.CreatedAt.IsZero() {
				membership.CreatedAt = topic.CreatedAt
			}
			if membership.UpdatedAt.IsZero() {
				membership.UpdatedAt = topic.UpdatedAt
			}
		}
		memberships = append(memberships, normalizeMembershipForRead(topic, thoughtID, membership))
	}
	sort.Slice(memberships, func(left, right int) bool {
		return memberships[left].ThoughtID < memberships[right].ThoughtID
	})
	return memberships, nil
}

func inferDocumentMembership(document string, topicID string, thoughtID string) models.TopicMembership {
	membership := models.TopicMembership{
		TopicID:   topicID,
		ThoughtID: thoughtID,
		MatchType: "accepted",
		Score:     1,
		Status:    "woven",
	}
	section := documentSectionForThought(document, thoughtID)
	matchLine := firstMatchLine(section)
	if matchLine == "" {
		return membership
	}
	reasons := parseMatchReasons(matchLine)
	if len(reasons) == 0 {
		return membership
	}
	membership.Reasons = reasons
	membership.MatchType = matchTypeFromReasons(reasons)
	if strings.HasPrefix(reasons[0], "semantic:") {
		if score, err := strconv.ParseFloat(strings.TrimPrefix(reasons[0], "semantic:"), 64); err == nil {
			membership.Score = score
		}
	}
	return membership
}

func documentSectionForThought(document string, thoughtID string) string {
	sourceIdx := strings.Index(document, thoughtID+".md")
	if sourceIdx < 0 {
		return ""
	}
	start := strings.LastIndex(document[:sourceIdx], "\n## ")
	altStart := strings.LastIndex(document[:sourceIdx], "\n### ")
	if altStart > start {
		start = altStart
	}
	if start < 0 {
		start = 0
	}
	end := strings.Index(document[sourceIdx:], "\n## ")
	if end < 0 {
		return document[start:]
	}
	return document[start : sourceIdx+end]
}

func firstMatchLine(section string) string {
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Match:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Match:"))
		}
	}
	return ""
}

func parseMatchReasons(matchLine string) []string {
	ret := []string{}
	for _, item := range strings.Split(matchLine, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			ret = append(ret, item)
		}
	}
	return ret
}

func matchTypeFromReasons(reasons []string) string {
	if len(reasons) == 0 {
		return "accepted"
	}
	allTags := true
	for _, reason := range reasons {
		switch {
		case strings.HasPrefix(reason, "semantic:"):
			return "semantic"
		case strings.HasPrefix(reason, "keyword:") || reason == "keywords.all":
			return "keyword"
		case strings.HasPrefix(reason, "tag:"):
		default:
			allTags = false
		}
	}
	if allTags {
		return "tag"
	}
	if contains(reasons, "manual include") {
		return "manual"
	}
	return "accepted"
}

func (s *Store) List(ctx context.Context) ([]models.Topic, error) {
	_ = ctx
	topicsRoot := filepath.Join(s.rootPath, "topics")
	entries, err := os.ReadDir(topicsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return []models.Topic{}, nil
	}
	if err != nil {
		return nil, err
	}
	ret := []models.Topic{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		topic, err := s.Get(ctx, entry.Name())
		if err == nil {
			ret = append(ret, topic)
		}
	}
	sort.Slice(ret, func(left, right int) bool {
		return ret[left].UpdatedAt.After(ret[right].UpdatedAt)
	})
	return ret, nil
}

func (s *Store) ReadDocument(ctx context.Context, id string) (string, error) {
	_ = ctx
	topic, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.rootPath, "topics", topic.Slug, "index.md")
	if err := workspace.EnsureInside(s.rootPath, path); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Store) MatchThought(topic models.Topic, thought models.Thought, content models.ThoughtContent) (models.TopicMembership, bool) {
	now := time.Now().UTC()
	if contains(topic.Rules.ManualExclude, thought.ID) {
		return models.TopicMembership{}, false
	}
	if contains(topic.Rules.ManualInclude, thought.ID) {
		return models.TopicMembership{
			TopicID:   topic.ID,
			ThoughtID: thought.ID,
			MatchType: "manual",
			Score:     1,
			Reasons:   []string{"manual include"},
			Status:    "accepted",
			CreatedAt: now,
			UpdatedAt: now,
		}, true
	}

	searchText := strings.ToLower(strings.Join([]string{
		thought.UserTitle,
		thought.ExtractedTitle,
		thought.Summary,
		content.Original,
		content.ExtractedContent,
		content.AINotes,
	}, "\n"))
	reasons := []string{}
	score := 0.0

	for _, excluded := range topic.Rules.Keywords.Exclude {
		if excluded != "" && strings.Contains(searchText, strings.ToLower(excluded)) {
			return models.TopicMembership{}, false
		}
	}
	for _, required := range topic.Rules.Keywords.All {
		if required != "" && !strings.Contains(searchText, strings.ToLower(required)) {
			return models.TopicMembership{}, false
		}
	}
	for _, keyword := range topic.Rules.Keywords.Any {
		if keyword != "" && strings.Contains(searchText, strings.ToLower(keyword)) {
			reasons = append(reasons, "keyword:"+keyword)
			score += 0.4
		}
	}
	allTags := append(append([]string{}, thought.UserTags...), thought.AITags...)
	for _, expected := range topic.Rules.Tags.Any {
		if containsFold(allTags, expected) {
			reasons = append(reasons, "tag:"+expected)
			score += 0.5
		}
	}
	if len(topic.Rules.Keywords.All) > 0 && len(reasons) == 0 {
		reasons = append(reasons, "keywords.all")
		score += 0.3
	}
	if score <= 0 {
		return models.TopicMembership{}, false
	}
	if score > 1 {
		score = 1
	}
	matchType := "keyword"
	if len(reasons) > 0 {
		allTagReasons := true
		for _, reason := range reasons {
			if !strings.HasPrefix(reason, "tag:") {
				allTagReasons = false
				break
			}
		}
		if allTagReasons {
			matchType = "tag"
		}
	}
	return models.TopicMembership{
		TopicID:   topic.ID,
		ThoughtID: thought.ID,
		MatchType: matchType,
		Score:     score,
		Reasons:   reasons,
		Status:    "accepted",
		CreatedAt: now,
		UpdatedAt: now,
	}, true
}

func (s *Store) AddMembership(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent, membership models.TopicMembership) (models.Topic, bool, error) {
	if contains(topic.Members, thought.ID) {
		_, membershipChanged, err := s.upsertMembership(topic, thought.ID, membership, time.Now().UTC())
		if err != nil {
			return models.Topic{}, false, err
		}
		currentThought, currentContent, err := markdown.ReadThought(s.rootPath, thought.ID)
		if err == nil {
			thought = currentThought
			content = currentContent
		} else if !errors.Is(err, os.ErrNotExist) {
			return models.Topic{}, false, err
		}
		changed, err := s.updateThoughtTopicLink(topic, thought, content, true)
		return topic, changed || membershipChanged, err
	}
	topic.Members = append(topic.Members, thought.ID)
	sort.Strings(topic.Members)
	now := time.Now().UTC()
	membership = normalizeMembership(topic, thought.ID, membership, now)
	topic.MemberCount = len(topic.Members)
	topic.LastActiveAt = &now
	topic.UpdatedAt = now

	document, err := s.ReadDocument(context.Background(), topic.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			document = initialDocument(topic)
		} else {
			return models.Topic{}, false, err
		}
	}
	document = s.weaveDocument(ctx, topic, document, thought, content, membership)
	document = updateTopicDocumentMembers(document, topic.Members)
	topic.WordCount = countWords(document)
	if err := s.writeDocument(topic, document); err != nil {
		return models.Topic{}, false, err
	}
	if _, _, err := s.upsertMembership(topic, thought.ID, membership, now); err != nil {
		return models.Topic{}, false, err
	}
	if err := s.writeTopic(topic); err != nil {
		return models.Topic{}, false, err
	}
	if _, err := s.updateThoughtTopicLink(topic, thought, content, true); err != nil {
		return models.Topic{}, false, err
	}
	return topic, true, nil
}

func (s *Store) Rebuild(ctx context.Context, id string) (models.Topic, int, []string, error) {
	return s.RebuildWithMatcher(ctx, id, func(_ context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent) (models.TopicMembership, bool) {
		return s.MatchThought(topic, thought, content)
	})
}

func (s *Store) RebuildWithMatcher(ctx context.Context, id string, matcher MatchFunc) (models.Topic, int, []string, error) {
	topic, err := s.Get(ctx, id)
	if err != nil {
		return models.Topic{}, 0, nil, err
	}
	if matcher == nil {
		matcher = func(_ context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent) (models.TopicMembership, bool) {
			return s.MatchThought(topic, thought, content)
		}
	}
	previousMembers := append([]string{}, topic.Members...)
	topic.Members = []string{}
	topic.MemberCount = 0
	topic.WordCount = 0
	topic.LastActiveAt = nil
	document := initialDocument(topic)
	matchedThoughts := map[string]models.Thought{}
	matchedContents := map[string]models.ThoughtContent{}
	matchedMemberships := map[string]models.TopicMembership{}
	thoughtsRoot := filepath.Join(s.rootPath, "thoughts")
	count := 0
	err = filepath.WalkDir(thoughtsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		thoughtID := strings.TrimSuffix(filepath.Base(path), ".md")
		thought, content, err := markdown.ReadThought(s.rootPath, thoughtID)
		if err != nil {
			return err
		}
		membership, ok := matcher(ctx, topic, thought, content)
		if !ok {
			return nil
		}
		topic.Members = append(topic.Members, thought.ID)
		matchedThoughts[thought.ID] = thought
		matchedContents[thought.ID] = content
		matchedMemberships[thought.ID] = normalizeMembership(topic, thought.ID, membership, time.Now().UTC())
		document = s.weaveDocument(ctx, topic, document, thought, content, membership)
		count++
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return models.Topic{}, 0, nil, err
	}
	sort.Strings(topic.Members)
	now := time.Now().UTC()
	document = updateTopicDocumentMembers(document, topic.Members)
	topic.MemberCount = len(topic.Members)
	topic.WordCount = countWords(document)
	topic.LastActiveAt = &now
	topic.UpdatedAt = now
	if err := s.writeDocument(topic, document); err != nil {
		return models.Topic{}, 0, nil, err
	}
	for _, thoughtID := range topic.Members {
		if _, _, err := s.upsertMembership(topic, thoughtID, matchedMemberships[thoughtID], now); err != nil {
			return models.Topic{}, 0, nil, err
		}
	}
	if err := s.removeStaleMemberships(topic, topic.Members); err != nil {
		return models.Topic{}, 0, nil, err
	}
	if err := s.writeTopic(topic); err != nil {
		return models.Topic{}, 0, nil, err
	}
	changedThoughtPaths := []string{}
	for _, thoughtID := range topic.Members {
		changed, err := s.updateThoughtTopicLink(topic, matchedThoughts[thoughtID], matchedContents[thoughtID], true)
		if err != nil {
			return models.Topic{}, 0, nil, err
		}
		if changed {
			changedThoughtPaths = append(changedThoughtPaths, matchedThoughts[thoughtID].Path)
		}
	}
	for _, thoughtID := range previousMembers {
		if contains(topic.Members, thoughtID) {
			continue
		}
		thought, content, err := markdown.ReadThought(s.rootPath, thoughtID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return models.Topic{}, 0, nil, err
		}
		changed, err := s.updateThoughtTopicLink(topic, thought, content, false)
		if err != nil {
			return models.Topic{}, 0, nil, err
		}
		if changed {
			changedThoughtPaths = append(changedThoughtPaths, thought.Path)
		}
	}
	return topic, count, changedThoughtPaths, nil
}

func (s *Store) writeTopic(topic models.Topic) error {
	path, err := s.topicPath(topic.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(topic)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tmp)
	}()
	return os.Rename(tmp, path)
}

func (s *Store) writeDocument(topic models.Topic, document string) error {
	path := filepath.Join(s.rootPath, "topics", topic.Slug, "index.md")
	if err := workspace.EnsureInside(s.rootPath, path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, []byte(document), 0o644); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tmp)
	}()
	return os.Rename(tmp, path)
}

func (s *Store) upsertMembership(topic models.Topic, thoughtID string, membership models.TopicMembership, now time.Time) (models.TopicMembership, bool, error) {
	next := normalizeMembership(topic, thoughtID, membership, now)
	current, err := s.readMembership(topic, thoughtID)
	if err == nil {
		current = normalizeMembershipForRead(topic, thoughtID, current)
		next.CreatedAt = current.CreatedAt
		if sameMembershipFact(current, next) {
			return current, false, nil
		}
		next.UpdatedAt = now
	} else if !errors.Is(err, os.ErrNotExist) {
		return models.TopicMembership{}, false, err
	}
	if err := s.writeMembership(topic, next); err != nil {
		return models.TopicMembership{}, false, err
	}
	return next, true, nil
}

func (s *Store) writeMembership(topic models.Topic, membership models.TopicMembership) error {
	path, err := s.membershipPath(topic, membership.ThoughtID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(membership)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tmp)
	}()
	return os.Rename(tmp, path)
}

func (s *Store) readMembership(topic models.Topic, thoughtID string) (models.TopicMembership, error) {
	path, err := s.membershipPath(topic, thoughtID)
	if err != nil {
		return models.TopicMembership{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return models.TopicMembership{}, err
	}
	var membership models.TopicMembership
	if err := yaml.Unmarshal(raw, &membership); err != nil {
		return models.TopicMembership{}, err
	}
	return normalizeMembershipForRead(topic, thoughtID, membership), nil
}

func (s *Store) readMemberships(topic models.Topic) ([]models.TopicMembership, error) {
	dir, err := s.membershipDir(topic)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []models.TopicMembership{}, nil
	}
	if err != nil {
		return nil, err
	}
	memberships := []models.TopicMembership{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		thoughtID := strings.TrimSuffix(entry.Name(), ".yaml")
		membership, err := s.readMembership(topic, thoughtID)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, membership)
	}
	sort.Slice(memberships, func(left, right int) bool {
		return memberships[left].ThoughtID < memberships[right].ThoughtID
	})
	return memberships, nil
}

func (s *Store) removeStaleMemberships(topic models.Topic, activeThoughtIDs []string) error {
	dir, err := s.membershipDir(topic)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	active := map[string]struct{}{}
	for _, thoughtID := range activeThoughtIDs {
		active[thoughtID] = struct{}{}
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		thoughtID := strings.TrimSuffix(entry.Name(), ".yaml")
		if _, ok := active[thoughtID]; ok {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := workspace.EnsureInside(s.rootPath, path); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) membershipDir(topic models.Topic) (string, error) {
	slug := Slugify(firstNonEmpty(topic.Slug, topic.ID))
	if slug == "" {
		return "", errors.New("topic id is required")
	}
	path := filepath.Join(s.rootPath, "topics", slug, "memberships")
	if err := workspace.EnsureInside(s.rootPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) membershipPath(topic models.Topic, thoughtID string) (string, error) {
	thoughtID = strings.TrimSpace(thoughtID)
	if thoughtID == "" {
		return "", errors.New("thought id is required")
	}
	if strings.ContainsAny(thoughtID, `/\`) {
		return "", fmt.Errorf("invalid thought id %q", thoughtID)
	}
	dir, err := s.membershipDir(topic)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, thoughtID+".yaml")
	if err := workspace.EnsureInside(s.rootPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) topicPath(id string) (string, error) {
	slug := Slugify(id)
	if slug == "" {
		return "", errors.New("topic id is required")
	}
	path := filepath.Join(s.rootPath, "topics", slug, "topic.yaml")
	if err := workspace.EnsureInside(s.rootPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func normalizeRule(rule models.TopicRule) models.TopicRule {
	rule.Keywords.Any = normalizeList(rule.Keywords.Any)
	rule.Keywords.All = normalizeList(rule.Keywords.All)
	rule.Keywords.Exclude = normalizeList(rule.Keywords.Exclude)
	rule.Tags.Any = normalizeList(rule.Tags.Any)
	rule.ManualInclude = normalizeList(rule.ManualInclude)
	rule.ManualExclude = normalizeList(rule.ManualExclude)
	return rule
}

func normalizeMembership(topic models.Topic, thoughtID string, membership models.TopicMembership, now time.Time) models.TopicMembership {
	if topic.ID != "" {
		membership.TopicID = topic.ID
	}
	if thoughtID != "" {
		membership.ThoughtID = thoughtID
	}
	if membership.MatchType == "" {
		membership.MatchType = "accepted"
	}
	if membership.Status == "" {
		membership.Status = "accepted"
	}
	if membership.Score == 0 && (membership.MatchType == "accepted" || membership.MatchType == "manual") {
		membership.Score = 1
	}
	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = now
	}
	if membership.UpdatedAt.IsZero() {
		membership.UpdatedAt = membership.CreatedAt
	}
	return membership
}

func normalizeMembershipForRead(topic models.Topic, thoughtID string, membership models.TopicMembership) models.TopicMembership {
	if topic.ID != "" {
		membership.TopicID = topic.ID
	}
	if thoughtID != "" {
		membership.ThoughtID = thoughtID
	}
	if membership.MatchType == "" {
		membership.MatchType = "accepted"
	}
	if membership.Status == "" {
		membership.Status = "accepted"
	}
	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = topic.CreatedAt
	}
	if membership.UpdatedAt.IsZero() {
		membership.UpdatedAt = topic.UpdatedAt
	}
	return membership
}

func sameMembershipFact(left models.TopicMembership, right models.TopicMembership) bool {
	if left.TopicID != right.TopicID ||
		left.ThoughtID != right.ThoughtID ||
		left.MatchType != right.MatchType ||
		left.Score != right.Score ||
		left.Status != right.Status {
		return false
	}
	return sameStringSet(left.Reasons, right.Reasons)
}

func initialDocument(topic models.Topic) string {
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString("id: ")
	builder.WriteString(topic.ID)
	builder.WriteString("\ntype: topic\n")
	builder.WriteString("members: []\n")
	builder.WriteString("---\n\n# ")
	builder.WriteString(topic.Name)
	builder.WriteString("\n")
	if topic.Description != "" {
		builder.WriteString("\n")
		builder.WriteString(topic.Description)
		builder.WriteString("\n")
	}
	for _, node := range topic.Outline {
		if strings.TrimSpace(node.Title) != "" {
			builder.WriteString("\n## ")
			builder.WriteString(strings.TrimSpace(node.Title))
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func updateTopicDocumentMembers(document string, members []string) string {
	members = append([]string{}, members...)
	sort.Strings(members)
	block := renderMembersBlock(members)
	if !strings.HasPrefix(document, "---\n") {
		return "---\n" + block + "---\n\n" + strings.TrimLeft(document, "\n")
	}
	end := strings.Index(document[4:], "\n---")
	if end < 0 {
		return "---\n" + block + "---\n\n" + strings.TrimLeft(document, "\n")
	}
	frontMatter := document[4 : 4+end]
	body := document[4+end:]
	lines := strings.Split(frontMatter, "\n")
	nextLines := []string{}
	for idx := 0; idx < len(lines); idx++ {
		line := lines[idx]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "members:") {
			for idx+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[idx+1]), "-") {
				idx++
			}
			continue
		}
		nextLines = append(nextLines, line)
	}
	return "---\n" + strings.Join(nextLines, "\n") + "\n" + block + body
}

func renderMembersBlock(members []string) string {
	if len(members) == 0 {
		return "members: []\n"
	}
	var builder strings.Builder
	builder.WriteString("members:\n")
	for _, member := range members {
		builder.WriteString("  - ")
		builder.WriteString(member)
		builder.WriteString("\n")
	}
	return builder.String()
}

func appendThoughtSection(rootPath string, topic models.Topic, document string, thought models.Thought, content models.ThoughtContent, membership models.TopicMembership) string {
	title := firstNonEmpty(thought.DisplayTitle, thought.UserTitle, thought.ExtractedTitle, thought.ID)
	body := firstNonEmpty(thought.Summary, firstLine(content.AINotes), firstLine(content.ExtractedContent), firstLine(content.Original))
	sourceRel := sourceRelativePath(rootPath, topic, thought)

	var builder strings.Builder
	builder.WriteString(strings.TrimRight(document, "\n"))
	builder.WriteString("\n\n## ")
	builder.WriteString(title)
	builder.WriteString("\n\n")
	if body != "" {
		builder.WriteString(body)
		builder.WriteString("\n\n")
	}
	if len(membership.Reasons) > 0 {
		builder.WriteString("Match: ")
		builder.WriteString(strings.Join(membership.Reasons, ", "))
		builder.WriteString("\n\n")
	}
	builder.WriteString("> Sources: [[")
	builder.WriteString(sourceRel)
	builder.WriteString("]]\n")
	return builder.String()
}

func (s *Store) weaveDocument(ctx context.Context, topic models.Topic, document string, thought models.Thought, content models.ThoughtContent, membership models.TopicMembership) string {
	sourceRel := sourceRelativePath(s.rootPath, topic, thought)
	if s.weaver != nil {
		result, err := s.weaver.Weave(ctx, models.TopicWeaveRequest{
			Topic:           topic,
			CurrentDocument: document,
			Thought:         thought,
			Content:         content,
			Membership:      membership,
			SourceLink:      sourceRel,
		})
		if err == nil && strings.Contains(result.Document, sourceRel) {
			return result.Document
		}
	}
	return appendThoughtSection(s.rootPath, topic, document, thought, content, membership)
}

func sourceRelativePath(rootPath string, topic models.Topic, thought models.Thought) string {
	sourceRel := thought.Path
	topicDir := filepath.Join(rootPath, "topics", topic.Slug)
	thoughtPath := filepath.Join(rootPath, filepath.FromSlash(thought.Path))
	if rel, err := filepath.Rel(topicDir, thoughtPath); err == nil {
		sourceRel = filepath.ToSlash(rel)
	}
	return sourceRel
}

func (s *Store) updateThoughtTopicLink(topic models.Topic, thought models.Thought, content models.ThoughtContent, include bool) (bool, error) {
	if thought.ID == "" {
		return false, errors.New("thought id is required")
	}
	nextTopicIDs := append([]string{}, thought.TopicIDs...)
	if include {
		nextTopicIDs = appendMissing(nextTopicIDs, topic.ID)
	} else {
		nextTopicIDs = removeValue(nextTopicIDs, topic.ID)
	}
	sort.Strings(nextTopicIDs)
	nextLinks := setTopicLink(s.rootPath, topic, thought, content.Links, include)
	nextStatus := models.TopicStatusUnmatched
	if len(nextTopicIDs) > 0 {
		nextStatus = models.TopicStatusMatched
	}
	if sameStringSet(thought.TopicIDs, nextTopicIDs) && strings.TrimSpace(content.Links) == strings.TrimSpace(nextLinks) && thought.TopicStatus == nextStatus {
		return false, nil
	}
	thought.TopicIDs = nextTopicIDs
	thought.TopicStatus = nextStatus
	thought.UpdatedAt = time.Now().UTC()
	content.Links = nextLinks
	if err := markdown.WriteThought(s.rootPath, thought, content); err != nil {
		return false, err
	}
	return true, nil
}

func setTopicLink(rootPath string, topic models.Topic, thought models.Thought, links string, include bool) string {
	marker := topicLinkMarker(topic.ID)
	lines := []string{}
	for _, line := range strings.Split(strings.TrimSpace(links), "\n") {
		if strings.Contains(line, marker) {
			continue
		}
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if include {
		if len(lines) > 0 && !contains(lines, "Topics:") {
			lines = append(lines, "")
		}
		if !contains(lines, "Topics:") {
			lines = append(lines, "Topics:")
		}
		lines = append(lines, topicLinkLine(rootPath, topic, thought))
	}
	if !include && len(lines) == 1 && lines[0] == "Topics:" {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func topicLinkLine(rootPath string, topic models.Topic, thought models.Thought) string {
	topicPath := filepath.Join(rootPath, "topics", topic.Slug, "index.md")
	thoughtDir := filepath.Dir(filepath.Join(rootPath, filepath.FromSlash(thought.Path)))
	relativePath := filepath.ToSlash(filepath.Join("topics", topic.Slug, "index.md"))
	if rel, err := filepath.Rel(thoughtDir, topicPath); err == nil {
		relativePath = filepath.ToSlash(rel)
	}
	return fmt.Sprintf("- [[%s|%s]] %s", relativePath, topic.Name, topicLinkMarker(topic.ID))
}

func topicLinkMarker(topicID string) string {
	return "<!-- topic:" + topicID + " -->"
}

var slugCleanup = regexp.MustCompile(`[^a-z0-9\-]+`)

func Slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, " ", "-")
	value = slugCleanup.ReplaceAllString(value, "")
	value = strings.Trim(value, "-")
	return value
}

func normalizeList(values []string) []string {
	seen := map[string]struct{}{}
	ret := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ret = append(ret, value)
	}
	return ret
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func appendMissing(values []string, expected string) []string {
	if contains(values, expected) {
		return values
	}
	return append(values, expected)
}

func removeValue(values []string, expected string) []string {
	ret := []string{}
	for _, value := range values {
		if value != expected {
			ret = append(ret, value)
		}
	}
	return ret
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string{}, left...)
	right = append([]string{}, right...)
	sort.Strings(left)
	sort.Strings(right)
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	line := strings.TrimSpace(strings.Split(value, "\n")[0])
	if len(line) > 240 {
		return line[:240]
	}
	return line
}

func countWords(value string) int {
	return len(strings.Fields(value))
}
