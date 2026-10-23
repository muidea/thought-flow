package topicstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/workspace"
)

var topicLocks sync.Map

func (s *Store) lock(id string) func() {
	root, _ := filepath.Abs(s.rootPath)
	value, _ := topicLocks.LoadOrStore(root+"/"+Slugify(id), &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

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
	r := objectlocation.For(s.rootPath)
	if e := r.Initialize(); e != nil {
		return models.Topic{}, e
	}
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	_ = ctx
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return models.Topic{}, errors.New("topic name is required")
	}
	slug := topicSlugFromName(name)
	unlock := s.lock(slug)
	defer unlock()
	if slug == "" {
		return models.Topic{}, errors.New("topic slug is empty")
	}
	tombstonePath, e := objectlocation.SafePath(s.rootPath, deletionPath(slug))
	if e != nil {
		return models.Topic{}, e
	}
	if _, e = os.Stat(tombstonePath); e == nil {
		return models.Topic{}, objectlocation.ErrConflict
	} else if !os.IsNotExist(e) {
		return models.Topic{}, e
	}
	mode := req.Mode
	if mode == "" {
		mode = models.TopicModeAggregate
	}
	if mode != models.TopicModeAggregate && mode != models.TopicModeCollection {
		return models.Topic{}, objectlocation.ErrInvalid
	}
	if mode == models.TopicModeCollection && !objectlocation.ValidID(req.OperationID) {
		return models.Topic{}, objectlocation.ErrInvalid
	}
	var op models.WorkspaceOperation
	if req.OperationID != "" {
		var e error
		op, e = r.Begin(req.OperationID, "topic_create", "", slug, req)
		if e != nil {
			return models.Topic{}, e
		}
		if op.State == "completed" {
			return s.Get(ctx, slug)
		}
	}
	if req.OperationID != "" {
		var persisted models.Topic
		if e := objectlocation.ReadJSON(s.rootPath, "topics/"+slug+"/topic.yaml", &persisted); e == nil && persisted.LastOperationID == req.OperationID && persisted.Generation == 1 {
			path, e := objectlocation.SafePath(s.rootPath, "topics/"+slug+"/index.md")
			if e != nil {
				return models.Topic{}, e
			}
			if _, e = os.Stat(path); os.IsNotExist(e) {
				if e = objectlocation.WriteFile(s.rootPath, "topics/"+slug+"/index.md", []byte(initialDocument(persisted)), 0644); e != nil {
					return models.Topic{}, e
				}
			} else if e != nil {
				return models.Topic{}, e
			}
			if persisted.Mode == models.TopicModeCollection {
				if _, e = s.Catalog(ctx, slug); os.IsNotExist(e) {
					if e = objectlocation.WriteJSON(s.rootPath, catalogPath(slug), initialCatalog(slug, req.OperationID), false); e != nil {
						return models.Topic{}, e
					}
				} else if e != nil {
					return models.Topic{}, e
				}
			}
			if e = r.Complete(op); e != nil {
				return models.Topic{}, e
			}
			return s.Get(ctx, slug)
		}
	}
	if _, err := s.Get(ctx, slug); err == nil {
		return models.Topic{}, objectlocation.ErrConflict
	}
	now := time.Now().UTC()
	autoWeave := true
	if req.AutoWeave != nil {
		autoWeave = *req.AutoWeave
	}
	if mode == models.TopicModeCollection {
		autoWeave = false
	}
	rules, err := normalizeRule(req.Rules)
	if err != nil {
		return models.Topic{}, err
	}
	topic := models.Topic{
		SchemaVersion: 2, Mode: mode, Generation: 1, SearchPolicy: req.SearchPolicy, LastOperationID: req.OperationID,
		ID:          slug,
		Name:        name,
		Slug:        slug,
		Description: strings.TrimSpace(req.Description),
		Rules:       rules,
		Outline:     req.Outline,
		AutoWeave:   autoWeave,
		CreatedAt:   now,
		UpdatedAt:   now,
		Members:     []string{},
	}
	if topic.SearchPolicy == nil {
		topic.SearchPolicy = DefaultSearchPolicy()
	}
	if req.OperationID != "" {
		op.TargetPath = "topics/" + slug + "/topic.yaml"
		op.CandidateHash = objectlocation.HashJSON(topic)
		op.AfterGeneration = 1
		op.EventType = "topic.created"
		if e := r.Prepare(op); e != nil {
			return models.Topic{}, e
		}
	}
	if err := s.writeTopic(topic); err != nil {
		return models.Topic{}, err
	}
	if err := s.writeDocument(topic, initialDocument(topic)); err != nil {
		return models.Topic{}, err
	}
	if mode == models.TopicModeCollection {
		if e := objectlocation.WriteJSON(s.rootPath, catalogPath(slug), initialCatalog(slug, req.OperationID), false); e != nil {
			return models.Topic{}, e
		}
	}
	r.BumpEpoch()
	if req.OperationID != "" {
		if e := r.Complete(op); e != nil {
			return models.Topic{}, e
		}
	}
	return topic, nil
}

func (s *Store) Update(ctx context.Context, id string, req models.TopicUpdateRequest) (models.Topic, error) {
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(id)
	defer unlock()
	_ = ctx
	topic, err := s.Get(ctx, id)
	if err != nil {
		return models.Topic{}, err
	}
	if topic.Mode == models.TopicModeCollection {
		return models.Topic{}, errors.New("use versioned settings or catalog for collection topics")
	}
	if strings.TrimSpace(req.Name) != "" {
		topic.Name = strings.TrimSpace(req.Name)
	}
	rules, err := normalizeRule(req.Rules)
	if err != nil {
		return models.Topic{}, err
	}
	topic.Description = strings.TrimSpace(req.Description)
	topic.Rules = rules
	topic.Outline = req.Outline
	if req.AutoWeave != nil {
		topic.AutoWeave = *req.AutoWeave
	}
	topic.UpdatedAt = time.Now().UTC()
	topic.Generation++
	if err := s.writeTopic(topic); err != nil {
		return models.Topic{}, err
	}
	return topic, nil
}

func (s *Store) Get(ctx context.Context, id string) (models.Topic, error) {
	if e := objectlocation.For(s.rootPath).CheckTopicActive(id); e != nil {
		return models.Topic{}, e
	}
	path, err := s.topicPath(id)
	if err != nil {
		return models.Topic{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return models.Topic{}, err
	}
	var topic models.Topic
	if err := json.Unmarshal(raw, &topic); err != nil {
		if err = yaml.Unmarshal(raw, &topic); err != nil {
			return models.Topic{}, err
		}
	}
	if topic.ImportOperationID != "" {
		op, e := objectlocation.For(s.rootPath).Operation(topic.ImportOperationID)
		if e != nil || op.State != "completed" {
			return topic, os.ErrNotExist
		}
	}
	if topic.ID != id || topic.Slug != id {
		return models.Topic{}, objectlocation.ErrInvalid
	}
	if topic.Mode == "" {
		topic.Mode = models.TopicModeAggregate
	}
	if topic.SearchPolicy == nil {
		topic.SearchPolicy = DefaultSearchPolicy()
	}
	if topic.Mode == models.TopicModeCollection {
		c, e := s.Catalog(ctx, id)
		if e != nil {
			return models.Topic{}, e
		}
		topic.Members = []string{}
		for _, entry := range c.Entries {
			if entry.Binding.Kind == "thought" {
				topic.Members = append(topic.Members, entry.Binding.ThoughtID)
			}
		}
		topic.MemberCount = len(topic.Members)
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
	candidates, _ := s.ListSessionCandidates(ctx, topic.ID)
	if candidates == nil {
		candidates = []models.TopicSessionCandidate{}
	}
	return models.TopicDetail{Topic: topic, Document: document, Members: memberships, SessionCandidates: candidates}, nil
}

func (s *Store) detailMemberships(topic models.Topic, document string) ([]models.TopicMembership, error) {
	if topic.Mode == models.TopicModeCollection {
		out := []models.TopicMembership{}
		for _, id := range topic.Members {
			out = append(out, models.TopicMembership{TopicID: topic.ID, ThoughtID: id, MatchType: "catalog", Status: "linked"})
		}
		return out, nil
	}
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
	if containsFold(thought.TopicIDs, topic.ID) {
		return models.TopicMembership{
			TopicID:   topic.ID,
			ThoughtID: thought.ID,
			MatchType: "manual",
			Score:     1,
			Reasons:   []string{"thought.topic_ids"},
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
	if topicRulesEmpty(topic.Rules) {
		for _, keyword := range defaultTopicKeywords(topic) {
			if containsTopicKeyword(searchText, keyword) {
				reasons = append(reasons, "topic:"+keyword)
				score += 0.4
				break
			}
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

func topicRulesEmpty(rules models.TopicRule) bool {
	return len(rules.Keywords.Any) == 0 &&
		len(rules.Keywords.All) == 0 &&
		len(rules.Keywords.Exclude) == 0 &&
		len(rules.Tags.Any) == 0 &&
		len(rules.ManualInclude) == 0 &&
		len(rules.ManualExclude) == 0 &&
		!rules.Semantic.Enabled
}

func defaultTopicKeywords(topic models.Topic) []string {
	values := []string{}
	if name := strings.TrimSpace(topic.Name); name != "" {
		values = append(values, name)
	}
	if id := strings.TrimSpace(topic.ID); id != "" {
		values = append(values, id)
	}
	if slug := strings.TrimSpace(topic.Slug); slug != "" {
		values = append(values, slug)
	}
	return normalizeList(values)
}

func containsTopicKeyword(searchText, keyword string) bool {
	keyword = strings.TrimSpace(strings.ToLower(keyword))
	if keyword == "" {
		return false
	}
	return strings.Contains(searchText, keyword)
}

func (s *Store) AddMembership(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent, membership models.TopicMembership) (models.Topic, bool, error) {
	if topic.Mode == models.TopicModeCollection {
		return topic, false, ErrAggregateOnly
	}
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(topic.ID)
	defer unlock()
	latest, err := s.Get(ctx, topic.ID)
	if err != nil {
		return models.Topic{}, false, err
	}
	thought, content, err = markdown.ReadThoughtUnderNamespace(s.rootPath, thought.ID)
	if err != nil {
		return models.Topic{}, false, err
	}

	topic = latest
	if topic.Mode == models.TopicModeCollection {
		return topic, false, ErrAggregateOnly
	}
	if contains(topic.Members, thought.ID) {
		_, membershipChanged, err := s.upsertMembership(topic, thought.ID, membership, time.Now().UTC())
		if err != nil {
			return models.Topic{}, false, err
		}
		currentThought, currentContent, err := markdown.ReadThoughtUnderNamespace(s.rootPath, thought.ID)
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

func (s *Store) PreviewMembership(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent, membership models.TopicMembership) (string, string, string, error) {
	if topic.Mode == models.TopicModeCollection {
		return "", "", "", ErrAggregateOnly
	}
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(topic.ID)
	defer unlock()
	latest, err := s.Get(ctx, topic.ID)
	if err != nil {
		return "", "", "", err
	}
	topic = latest
	if strings.TrimSpace(thought.ID) == "" {
		return "", "", "", errors.New("thought id is required")
	}
	document, err := s.ReadDocument(ctx, topic.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			document = initialDocument(topic)
		} else {
			return "", "", "", err
		}
	}
	now := time.Now().UTC()
	membership = normalizeMembership(topic, thought.ID, membership, now)
	proposed := s.weaveDocument(ctx, topic, document, thought, content, membership)
	members := appendMissing(append([]string{}, topic.Members...), thought.ID)
	sort.Strings(members)
	proposed = updateTopicDocumentMembers(proposed, members)
	sourceRel := sourceRelativePath(s.rootPath, topic, thought)
	if !strings.Contains(proposed, sourceRel) {
		return "", "", "", errors.New("topic weave document missing required source link")
	}
	return document, proposed, sourceRel, nil
}

func (s *Store) SaveWeaveProposal(ctx context.Context, topic models.Topic, proposal models.TopicWeaveProposal) (models.TopicWeaveProposal, error) {
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(topic.ID)
	defer unlock()
	_ = ctx
	now := time.Now().UTC()
	proposal = normalizeWeaveProposal(topic, proposal, now)
	if strings.TrimSpace(proposal.ThoughtID) == "" {
		return models.TopicWeaveProposal{}, errors.New("thought id is required")
	}
	if strings.TrimSpace(proposal.ProposedDocument) == "" {
		return models.TopicWeaveProposal{}, errors.New("proposed document is required")
	}
	if strings.TrimSpace(proposal.SourceLink) != "" && !strings.Contains(proposal.ProposedDocument, proposal.SourceLink) {
		return models.TopicWeaveProposal{}, errors.New("topic weave proposal missing required source link")
	}
	if err := s.writeWeaveProposal(topic, proposal); err != nil {
		return models.TopicWeaveProposal{}, err
	}
	return proposal, nil
}

func (s *Store) ListWeaveProposals(ctx context.Context, topicID string) ([]models.TopicWeaveProposal, error) {
	topic, err := s.Get(ctx, topicID)
	if err != nil {
		return nil, err
	}
	dir, err := s.weaveProposalDir(topic)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []models.TopicWeaveProposal{}, nil
	}
	if err != nil {
		return nil, err
	}
	proposals := []models.TopicWeaveProposal{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		proposalID := strings.TrimSuffix(entry.Name(), ".yaml")
		proposal, err := s.readWeaveProposal(topic, proposalID)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	sort.Slice(proposals, func(left, right int) bool {
		return proposals[left].CreatedAt.After(proposals[right].CreatedAt)
	})
	return proposals, nil
}

func (s *Store) GetWeaveProposal(ctx context.Context, topicID string, proposalID string) (models.TopicWeaveProposal, error) {
	topic, err := s.Get(ctx, topicID)
	if err != nil {
		return models.TopicWeaveProposal{}, err
	}
	return s.readWeaveProposal(topic, proposalID)
}

func (s *Store) MarkWeaveProposalAccepted(ctx context.Context, topic models.Topic, proposalID string, document string) (models.TopicWeaveProposal, error) {
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(topic.ID)
	defer unlock()
	_ = ctx
	proposal, err := s.readWeaveProposal(topic, proposalID)
	if err != nil {
		return models.TopicWeaveProposal{}, err
	}
	now := time.Now().UTC()
	proposal.Status = "accepted"
	proposal.AcceptedDocument = strings.TrimSpace(document)
	proposal.UpdatedAt = now
	proposal.AcceptedAt = &now
	if err := s.writeWeaveProposal(topic, proposal); err != nil {
		return models.TopicWeaveProposal{}, err
	}
	return proposal, nil
}

func (s *Store) ApplyMembershipPatch(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent, membership models.TopicMembership, patch models.TopicDocumentPatch) (models.Topic, bool, string, error) {
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(topic.ID)
	defer unlock()
	latest, getErr := s.Get(ctx, topic.ID)
	if getErr != nil {
		return models.Topic{}, false, "", getErr
	}
	topic = latest
	currentDocument, err := s.ReadDocument(ctx, topic.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return models.Topic{}, false, "", err
	}
	if errors.Is(err, os.ErrNotExist) {
		currentDocument = initialDocument(topic)
	}
	document, err := applyDocumentPatch(currentDocument, patch)
	if err != nil {
		return models.Topic{}, false, "", err
	}
	updatedTopic, changed, err := s.applyMembershipDocumentLocked(ctx, topic, thought, content, membership, document)
	return updatedTopic, changed, document, err
}

func (s *Store) ApplyMembershipDocument(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent, membership models.TopicMembership, document string, expectedDocument ...string) (models.Topic, bool, error) {
	if topic.Mode == models.TopicModeCollection {
		return topic, false, ErrAggregateOnly
	}
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(topic.ID)
	defer unlock()
	latest, err := s.Get(ctx, topic.ID)
	if err != nil {
		return models.Topic{}, false, err
	}
	if latest.Mode == models.TopicModeCollection {
		return models.Topic{}, false, ErrAggregateOnly
	}
	if !latest.UpdatedAt.Equal(topic.UpdatedAt) {
		return models.Topic{}, false, errors.New("topic changed; refresh the proposal")
	}
	if len(expectedDocument) > 0 {
		current, err := s.ReadDocument(ctx, topic.ID)
		if err != nil {
			return models.Topic{}, false, err
		}
		if current != expectedDocument[0] {
			return models.Topic{}, false, errors.New("topic changed; refresh the proposal")
		}
	}

	return s.applyMembershipDocumentLocked(ctx, latest, thought, content, membership, document)
}

func (s *Store) applyMembershipDocumentLocked(ctx context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent, membership models.TopicMembership, document string) (models.Topic, bool, error) {
	if strings.TrimSpace(thought.ID) == "" {
		return models.Topic{}, false, errors.New("thought id is required")
	}
	document = strings.TrimSpace(document)
	if document == "" {
		return models.Topic{}, false, errors.New("topic document is required")
	}
	sourceRel := sourceRelativePath(s.rootPath, topic, thought)
	if !strings.Contains(document, sourceRel) {
		return models.Topic{}, false, errors.New("topic weave document missing required source link")
	}
	currentDocument, err := s.ReadDocument(ctx, topic.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return models.Topic{}, false, err
	}
	if errors.Is(err, os.ErrNotExist) {
		currentDocument = initialDocument(topic)
	}

	now := time.Now().UTC()
	memberChanged := false
	if !contains(topic.Members, thought.ID) {
		topic.Members = append(topic.Members, thought.ID)
		sort.Strings(topic.Members)
		memberChanged = true
	}
	document = updateTopicDocumentMembers(document, topic.Members)
	topic.MemberCount = len(topic.Members)
	topic.WordCount = countWords(document)
	topic.LastActiveAt = &now
	topic.UpdatedAt = now
	if err := s.writeDocument(topic, document); err != nil {
		return models.Topic{}, false, err
	}
	membership = normalizeMembership(topic, thought.ID, membership, now)
	_, membershipChanged, err := s.upsertMembership(topic, thought.ID, membership, now)
	if err != nil {
		return models.Topic{}, false, err
	}
	if err := s.writeTopic(topic); err != nil {
		return models.Topic{}, false, err
	}
	linkChanged, err := s.updateThoughtTopicLink(topic, thought, content, true)
	if err != nil {
		return models.Topic{}, false, err
	}
	documentChanged := strings.TrimSpace(currentDocument) != strings.TrimSpace(document)
	return topic, memberChanged || documentChanged || membershipChanged || linkChanged, nil
}

func (s *Store) Refresh(ctx context.Context, id string) (models.Topic, int, []string, error) {
	return s.RefreshWithMatcher(ctx, id, func(_ context.Context, topic models.Topic, thought models.Thought, content models.ThoughtContent) (models.TopicMembership, bool) {
		return s.MatchThought(topic, thought, content)
	})
}

func (s *Store) RefreshWithMatcher(ctx context.Context, id string, matcher MatchFunc) (models.Topic, int, []string, error) {
	if t, e := s.Get(ctx, id); e == nil && t.Mode == models.TopicModeCollection {
		v, e := s.RebuildCatalog(ctx, id)
		return v.Topic, 0, nil, e
	}
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(id)
	defer unlock()
	topic, err := s.Get(ctx, id)
	if err != nil {
		return models.Topic{}, 0, nil, err
	}
	if matcher == nil {
		matcher = func(_ context.Context, t models.Topic, thought models.Thought, c models.ThoughtContent) (models.TopicMembership, bool) {
			return s.MatchThought(t, thought, c)
		}
	}
	previous := append([]string{}, topic.Members...)
	document, err := s.ReadDocument(ctx, id)
	if os.IsNotExist(err) {
		document = initialDocument(topic)
	} else if err != nil {
		return models.Topic{}, 0, nil, err
	}
	found := map[string]bool{}
	changedPaths := []string{}
	count := 0
	ids, err := markdown.ListThoughtIDs(s.rootPath)
	if err != nil {
		return models.Topic{}, 0, nil, err
	}
	err = func() error {
		for _, thoughtID := range ids {
			thought, content, e := markdown.ReadThoughtUnderNamespace(s.rootPath, thoughtID)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			existing := contains(previous, thought.ID)
			membership, matches := matcher(ctx, topic, thought, content)
			if !existing && (!matches || !topic.AutoWeave) {
				continue
			}
			found[thought.ID] = true
			if !existing {
				topic.Members = appendMissing(topic.Members, thought.ID)
				document = s.weaveDocument(ctx, topic, document, thought, content, membership)
				if _, _, e = s.upsertMembership(topic, thought.ID, membership, time.Now().UTC()); e != nil {
					return e
				}
				count++
			}
			changed, e := s.updateThoughtTopicLink(topic, thought, content, true)
			if changed {
				changedPaths = append(changedPaths, thought.Path)
			}
			if e != nil {
				return e
			}
		}
		return nil
	}()
	if err != nil {
		return models.Topic{}, 0, nil, err
	}
	for _, old := range previous {
		if found[old] {
			continue
		}
		topic.Members = removeValue(topic.Members, old)
		changedPaths = append(changedPaths, filepath.ToSlash(filepath.Join("topics", topic.Slug, "memberships", old+".yaml")))
		document += "\n> Source deleted; review retained content: " + old + "\n"
	}
	sort.Strings(topic.Members)
	if !sameStringSet(previous, topic.Members) {
		document = updateTopicDocumentMembers(document, topic.Members)
	}
	topic.MemberCount = len(topic.Members)
	topic.WordCount = countWords(document)
	topic.UpdatedAt = time.Now().UTC()
	if err = s.writeDocument(topic, document); err != nil {
		return models.Topic{}, 0, nil, err
	}
	if err = s.removeStaleMemberships(topic, topic.Members); err != nil {
		return models.Topic{}, 0, nil, err
	}
	if err = s.writeTopic(topic); err != nil {
		return models.Topic{}, 0, nil, err
	}
	return topic, count, changedPaths, nil
}

// RemoveThought preserves authored content and marks its provenance unavailable.
func (s *Store) RemoveThought(ctx context.Context, id, thoughtID string) (models.Topic, bool, error) {
	if t, e := s.Get(ctx, id); e == nil && t.Mode == models.TopicModeCollection {
		_, e = s.RebuildCatalog(ctx, id)
		return t, false, e
	}
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(id)
	defer unlock()
	topic, err := s.Get(ctx, id)
	if err != nil {
		return topic, false, err
	}
	if !contains(topic.Members, thoughtID) {
		return topic, false, nil
	}
	document, err := s.ReadDocument(ctx, id)
	if err != nil {
		return topic, false, err
	}
	topic.Members = removeValue(topic.Members, thoughtID)
	topic.MemberCount = len(topic.Members)
	topic.UpdatedAt = time.Now().UTC()
	document = updateTopicDocumentMembers(document, topic.Members) + "\n> Source deleted; review retained content: " + thoughtID + "\n"
	if err = s.writeDocument(topic, document); err != nil {
		return topic, false, err
	}
	path, err := s.membershipPath(topic, thoughtID)
	if err != nil {
		return topic, false, err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return topic, false, err
	}
	return topic, true, s.writeTopic(topic)
}

func (s *Store) writeTopic(topic models.Topic) error {
	path, err := s.topicPath(topic.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rel, err := filepath.Rel(s.rootPath, path)
	if err != nil {
		return err
	}
	return objectlocation.WriteJSON(s.rootPath, filepath.ToSlash(rel), topic, false)
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

func (s *Store) writeWeaveProposal(topic models.Topic, proposal models.TopicWeaveProposal) error {
	path, err := s.weaveProposalPath(topic, proposal.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(proposal)
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

func (s *Store) readWeaveProposal(topic models.Topic, proposalID string) (models.TopicWeaveProposal, error) {
	path, err := s.weaveProposalPath(topic, proposalID)
	if err != nil {
		return models.TopicWeaveProposal{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return models.TopicWeaveProposal{}, err
	}
	var proposal models.TopicWeaveProposal
	if err := yaml.Unmarshal(raw, &proposal); err != nil {
		return models.TopicWeaveProposal{}, err
	}
	return normalizeWeaveProposal(topic, proposal, time.Now().UTC()), nil
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

// candidatesPath resolves the per-topic session-candidates file.
// Sessions are stored in a single YAML rather than per-session files
// because the candidate list is a read-only projection that is
// rewritten wholesale on every refresh — the volume is tiny (one
// entry per unarchived session) and there is no per-session history
// to preserve.
func (s *Store) candidatesPath(topic models.Topic) (string, error) {
	slug := Slugify(firstNonEmpty(topic.Slug, topic.ID))
	if slug == "" {
		return "", errors.New("topic id is required")
	}
	path := filepath.Join(s.rootPath, "topics", slug, "candidates.yaml")
	if err := workspace.EnsureInside(s.rootPath, path); err != nil {
		return "", err
	}
	return path, nil
}

// ListSessionCandidates returns the scratchpad sessions currently
// matching this topic, in the order written (most-recent match first
// by convention of the writer). A missing file is not an error and
// returns an empty slice; the topic has simply never been refreshed
// while any session was uncommitted.
func (s *Store) ListSessionCandidates(ctx context.Context, id string) ([]models.TopicSessionCandidate, error) {
	_ = ctx
	topic, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	path, err := s.candidatesPath(topic)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []models.TopicSessionCandidate{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return []models.TopicSessionCandidate{}, nil
	}
	var candidates []models.TopicSessionCandidate
	if err := yaml.Unmarshal(raw, &candidates); err != nil {
		return nil, err
	}
	return candidates, nil
}

// SaveSessionCandidates overwrites the candidates file for the given
// topic. Callers should pass the full desired list — the file is not
// a log, it is a snapshot. Empty input is allowed and writes an
// empty (but valid) YAML so subsequent reads do not see a "missing
// file" sentinel.
func (s *Store) SaveSessionCandidates(ctx context.Context, id string, candidates []models.TopicSessionCandidate) error {
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(id)
	defer unlock()
	return s.saveSessionCandidatesLocked(ctx, id, candidates)
}

// UpdateSessionCandidates applies an entire candidate projection transaction.
func (s *Store) UpdateSessionCandidates(ctx context.Context, id string, update func([]models.TopicSessionCandidate) ([]models.TopicSessionCandidate, error)) error {
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	unlock := s.lock(id)
	defer unlock()
	current, err := s.ListSessionCandidates(ctx, id)
	if err != nil {
		return err
	}
	next, err := update(current)
	if err != nil {
		return err
	}
	return s.saveSessionCandidatesLocked(ctx, id, next)
}

func (s *Store) saveSessionCandidatesLocked(ctx context.Context, id string, candidates []models.TopicSessionCandidate) error {
	_ = ctx
	topic, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if candidates == nil {
		candidates = []models.TopicSessionCandidate{}
	}
	path, err := s.candidatesPath(topic)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(candidates)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func (s *Store) weaveProposalDir(topic models.Topic) (string, error) {
	slug := Slugify(firstNonEmpty(topic.Slug, topic.ID))
	if slug == "" {
		return "", errors.New("topic id is required")
	}
	path := filepath.Join(s.rootPath, "topics", slug, "approvals")
	if err := workspace.EnsureInside(s.rootPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) weaveProposalPath(topic models.Topic, proposalID string) (string, error) {
	proposalID = strings.TrimSpace(proposalID)
	if proposalID == "" {
		return "", errors.New("proposal id is required")
	}
	if strings.ContainsAny(proposalID, `/\`) {
		return "", fmt.Errorf("invalid proposal id %q", proposalID)
	}
	dir, err := s.weaveProposalDir(topic)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, proposalID+".yaml")
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

func normalizeRule(rule models.TopicRule) (models.TopicRule, error) {
	if rule.Semantic.Threshold < 0 || rule.Semantic.Threshold > 1 {
		return models.TopicRule{}, errors.New("semantic threshold must be between 0 and 1")
	}
	rule.Keywords.Any = normalizeList(rule.Keywords.Any)
	rule.Keywords.All = normalizeList(rule.Keywords.All)
	rule.Keywords.Exclude = normalizeList(rule.Keywords.Exclude)
	rule.Tags.Any = normalizeList(rule.Tags.Any)
	rule.ManualInclude = normalizeList(rule.ManualInclude)
	rule.ManualExclude = normalizeList(rule.ManualExclude)
	return rule, nil
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

func normalizeWeaveProposal(topic models.Topic, proposal models.TopicWeaveProposal, now time.Time) models.TopicWeaveProposal {
	if proposal.ID == "" {
		proposal.ID = models.NewJobID("topic-weave-proposal", now)
	}
	if topic.ID != "" {
		proposal.TopicID = topic.ID
	}
	if proposal.Status == "" {
		proposal.Status = "pending"
	}
	if proposal.CreatedAt.IsZero() {
		proposal.CreatedAt = now
	}
	if proposal.UpdatedAt.IsZero() {
		proposal.UpdatedAt = proposal.CreatedAt
	}
	return proposal
}

func applyDocumentPatch(currentDocument string, patch models.TopicDocumentPatch) (string, error) {
	if len(patch.Hunks) == 0 {
		return "", errors.New("patch hunks are required")
	}
	if patch.BaseHash != "" && models.ContentHash(strings.TrimRight(currentDocument, "\n")) != patch.BaseHash {
		return "", errors.New("patch base document mismatch")
	}
	currentLines := splitPatchDocumentLines(currentDocument)
	result := []string{}
	cursor := 0
	for _, hunk := range patch.Hunks {
		baseCount, proposedCount := patchHunkCounts(hunk)
		if baseCount != hunk.BaseCount || proposedCount != hunk.ProposedCount {
			return "", errors.New("patch hunk count mismatch")
		}
		baseStart := hunk.BaseStart
		if baseStart <= 0 {
			baseStart = 1
		}
		start := baseStart - 1
		if start < cursor || start > len(currentLines) {
			return "", fmt.Errorf("patch hunk base_start %d is out of range", hunk.BaseStart)
		}
		result = append(result, currentLines[cursor:start]...)
		cursor = start
		for _, line := range hunk.Lines {
			switch line.Op {
			case "context":
				if cursor >= len(currentLines) || currentLines[cursor] != line.Text {
					return "", errors.New("patch context mismatch")
				}
				result = append(result, currentLines[cursor])
				cursor++
			case "remove":
				if cursor >= len(currentLines) || currentLines[cursor] != line.Text {
					return "", errors.New("patch remove mismatch")
				}
				cursor++
			case "add":
				result = append(result, line.Text)
			default:
				return "", fmt.Errorf("unsupported patch operation %q", line.Op)
			}
		}
	}
	result = append(result, currentLines[cursor:]...)
	document := strings.Join(result, "\n")
	if document != "" {
		document += "\n"
	}
	if patch.ProposedHash != "" && models.ContentHash(strings.TrimRight(document, "\n")) != patch.ProposedHash {
		return "", errors.New("patch proposed document hash mismatch")
	}
	return document, nil
}

func patchHunkCounts(hunk models.TopicDocumentPatchHunk) (int, int) {
	baseCount := 0
	proposedCount := 0
	for _, line := range hunk.Lines {
		if line.Op != "add" {
			baseCount++
		}
		if line.Op != "remove" {
			proposedCount++
		}
	}
	return baseCount, proposedCount
}

func splitPatchDocumentLines(document string) []string {
	document = strings.TrimRight(document, "\n")
	if document == "" {
		return []string{}
	}
	return strings.Split(document, "\n")
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
	changed := false
	err := markdown.UpdateThoughtUnderNamespace(s.rootPath, thought.ID, func(current *models.Thought, body *models.ThoughtContent) error {
		next := append([]string{}, current.TopicIDs...)
		if include {
			next = appendMissing(next, topic.ID)
		} else {
			next = removeValue(next, topic.ID)
		}
		sort.Strings(next)
		links := setTopicLink(s.rootPath, topic, *current, body.Links, include)
		status := models.TopicStatusUnmatched
		if len(next) > 0 {
			status = models.TopicStatusMatched
		}
		changed = !sameStringSet(current.TopicIDs, next) || strings.TrimSpace(body.Links) != strings.TrimSpace(links) || current.TopicStatus != status
		current.TopicIDs = next
		current.TopicStatus = status
		body.Links = links
		return nil
	})
	return changed, err
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

// topicSlugFromName derives a path-safe, unique-friendly topic slug from a
// display name. Prefer a readable Slugify result (including CJK letters);
// fall back to a stable hash-based id when the name has no path-safe runes
// (emoji / pure punctuation).
func topicSlugFromName(name string) string {
	if slug := Slugify(name); slug != "" {
		return slug
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(name)))
	return "topic-" + hex.EncodeToString(sum[:])[:8]
}

// Slugify converts a display string into a path-safe topic slug.
//
// Rules:
//   - keep Unicode letters and numbers (Latin, CJK, Hangul, …)
//   - keep existing '-'
//   - map '_' and whitespace to '-'
//   - drop other punctuation / symbols / path-hostile runes (/, \, ., …)
//   - lower-case Latin letters; CJK is unchanged
//
// Consecutive dashes are preserved so that previously generated ASCII
// slugs (e.g. "foo--bar" from "foo  bar") remain resolvable when the
// directory name is re-passed through Slugify in topicPath/Get.
//
// Empty result means the input had no path-safe content — callers that need
// a guaranteed non-empty id should use topicSlugFromName.
func Slugify(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '_' || r == ' ' || r == '\t' || r == '\n' || r == '\r':
			b.WriteByte('-')
		case r == '-':
			b.WriteByte('-')
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			// Drop path-hostile / decorative runes (/, \, ., :, emoji, …).
		}
	}
	return strings.Trim(b.String(), "-")
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
