package searchdb

import (
	"fmt"
	"sort"
	"strings"

	"thoughtflow/internal/pkg/models"
)

func normalizedSearchSort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "keyword", "keyword_score":
		return "keyword"
	case "semantic", "semantic_score":
		return "semantic"
	case "recency", "recent", "updated", "updated_at", "recency_score":
		return "recency"
	default:
		return "score"
	}
}

func scoreWithWeights(mode string, keyword float64, semantic float64, recency float64, useVector bool, requested models.SearchWeights) (float64, models.SearchWeights) {
	weights := searchWeights(mode, useVector, requested)
	return keyword*weights.Keyword + semantic*weights.Semantic + recency*weights.Recency, weights
}

func searchWeights(mode string, useVector bool, requested models.SearchWeights) models.SearchWeights {
	if hasCustomWeights(requested) {
		return normalizeWeights(requested)
	}
	switch mode {
	case "semantic":
		return models.SearchWeights{Semantic: 0.9, Recency: 0.1}
	case "hybrid":
		if useVector {
			return models.SearchWeights{Keyword: 0.45, Semantic: 0.45, Recency: 0.10}
		}
		return models.SearchWeights{Keyword: 1}
	default:
		return models.SearchWeights{Keyword: 1}
	}
}

func hasCustomWeights(weights models.SearchWeights) bool {
	return weights.Keyword > 0 || weights.Semantic > 0 || weights.Recency > 0
}

func normalizeWeights(weights models.SearchWeights) models.SearchWeights {
	weights.Keyword = positiveWeight(weights.Keyword)
	weights.Semantic = positiveWeight(weights.Semantic)
	weights.Recency = positiveWeight(weights.Recency)
	total := weights.Keyword + weights.Semantic + weights.Recency
	if total == 0 {
		return models.SearchWeights{Keyword: 1}
	}
	return models.SearchWeights{
		Keyword:  weights.Keyword / total,
		Semantic: weights.Semantic / total,
		Recency:  weights.Recency / total,
	}
}

func positiveWeight(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func explainSearchResult(query models.SearchQuery, mode string, sortMode string, weights models.SearchWeights, keywordSource string, semanticSource string, item models.SearchResult) *models.SearchExplain {
	if !query.Explain {
		return nil
	}
	return &models.SearchExplain{
		Mode:           mode,
		Sort:           sortMode,
		ScoreFormula:   fmt.Sprintf("score = keyword_score*%.4g + semantic_score*%.4g + recency_score*%.4g", weights.Keyword, weights.Semantic, weights.Recency),
		Weights:        weights,
		Components:     models.SearchWeights{Keyword: item.KeywordScore, Semantic: item.SemanticScore, Recency: item.RecencyScore},
		KeywordSource:  keywordSource,
		SemanticSource: semanticSource,
	}
}

func sortSearchResults(items []models.SearchResult, sortMode string) {
	sort.Slice(items, func(left, right int) bool {
		leftScore := sortValue(items[left], sortMode)
		rightScore := sortValue(items[right], sortMode)
		if leftScore == rightScore {
			if items[left].Score == items[right].Score {
				return items[left].ThoughtID > items[right].ThoughtID
			}
			return items[left].Score > items[right].Score
		}
		return leftScore > rightScore
	})
}

func sortValue(item models.SearchResult, sortMode string) float64 {
	switch sortMode {
	case "keyword":
		return item.KeywordScore
	case "semantic":
		return item.SemanticScore
	case "recency":
		return item.RecencyScore
	default:
		return item.Score
	}
}

func topSemanticThoughtIDs(scores map[string]float64, limit int) []string {
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool {
		if scores[ids[left]] == scores[ids[right]] {
			return ids[left] > ids[right]
		}
		return scores[ids[left]] > scores[ids[right]]
	})
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids
}
