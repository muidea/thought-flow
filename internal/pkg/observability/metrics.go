package observability

import "sync"

type RuntimeCounters struct {
	AIRequestTotal   uint64 `json:"ai_request_total"`
	SearchQueryTotal uint64 `json:"search_query_total"`
	TopicWeaveTotal  uint64 `json:"topic_weave_total"`
}

var counters = struct {
	mu    sync.RWMutex
	value RuntimeCounters
}{}

func IncrementAIRequest() {
	counters.mu.Lock()
	defer counters.mu.Unlock()
	counters.value.AIRequestTotal++
}

func IncrementSearchQuery() {
	counters.mu.Lock()
	defer counters.mu.Unlock()
	counters.value.SearchQueryTotal++
}

func IncrementTopicWeave() {
	AddTopicWeave(1)
}

func AddTopicWeave(count uint64) {
	if count == 0 {
		return
	}
	counters.mu.Lock()
	defer counters.mu.Unlock()
	counters.value.TopicWeaveTotal += count
}

func Snapshot() RuntimeCounters {
	counters.mu.RLock()
	defer counters.mu.RUnlock()
	return counters.value
}

func ResetForTest() {
	counters.mu.Lock()
	defer counters.mu.Unlock()
	counters.value = RuntimeCounters{}
}
