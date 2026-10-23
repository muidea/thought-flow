package objectlocation

// WorkspaceMetrics contains process counters only; persistent operations remain
// the recovery authority. No metric label contains source bodies or evidence.
func (r *Registry) AddMetric(name string, delta float64) {
	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()
	if r.metrics == nil {
		r.metrics = map[string]float64{}
	}
	r.metrics[name] += delta
}
func (r *Registry) SetMetric(name string, value float64) {
	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()
	if r.metrics == nil {
		r.metrics = map[string]float64{}
	}
	r.metrics[name] = value
}
func (r *Registry) Metrics() map[string]float64 {
	r.metricsMu.Lock()
	defer r.metricsMu.Unlock()
	result := map[string]float64{}
	for _, key := range []string{"migration_failures_total", "migration_duration_seconds", "location_conflicts_total", "catalog_generation", "projection_lag_seconds", "index_rebuild_failures_total", "policy_filtered_total", "epoch_retries_total", "resources_missing_total"} {
		result["thoughtflow_workspace_"+key] = r.metrics[key]
	}
	for key, value := range r.metrics {
		result["thoughtflow_workspace_"+key] = value
	}
	return result
}
