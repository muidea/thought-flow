package documentprofile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"thoughtflow/assets/documentformats"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/workspace"
)

var ErrProfileNotFound = errors.New("document profile not found")
var ErrProfileConflict = errors.New("document profile version conflict")

type Registry struct {
	mu               sync.RWMutex
	customDir        string
	defaultProfileID string
	limits           Limits
	profiles         map[string]DocumentProfile
	latest           map[string]DocumentProfile
	issues           []models.ValidationIssue
}

type RegistryStatus struct {
	ProfileCount int                      `json:"profile_count"`
	Issues       []models.ValidationIssue `json:"issues,omitempty"`
}

func (r *Registry) Publish(raw []byte) (DocumentProfile, error) {
	profile, err := Parse(raw, r.limits)
	if err != nil {
		return DocumentProfile{}, err
	}
	if strings.HasPrefix(profile.Ref.ProfileID, "builtin.") {
		return DocumentProfile{}, errors.New("custom profile cannot use builtin namespace")
	}
	if r.customDir == "" {
		return DocumentProfile{}, errors.New("custom document format directory is not configured")
	}
	dir := filepath.Join(r.customDir, "published", profile.Ref.ProfileID)
	path := filepath.Join(dir, fmt.Sprintf("v%d.md", profile.Ref.Version))
	if err := workspace.EnsureInside(r.customDir, path); err != nil {
		return DocumentProfile{}, err
	}
	if err := rejectSymlinkComponents(r.customDir, dir); err != nil {
		return DocumentProfile{}, err
	}
	if _, err := os.Stat(path); err == nil {
		return DocumentProfile{}, fmt.Errorf("profile %s@%d already exists", profile.Ref.ProfileID, profile.Ref.Version)
	} else if !errors.Is(err, os.ErrNotExist) {
		return DocumentProfile{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return DocumentProfile{}, err
	}
	tmp, err := os.CreateTemp(dir, ".profile-*.tmp")
	if err != nil {
		return DocumentProfile{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return DocumentProfile{}, err
	}
	if err := tmp.Close(); err != nil {
		return DocumentProfile{}, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return DocumentProfile{}, err
	}
	r.Reload()
	return profile, nil
}

func NewRegistry(customDir, defaultProfileID string, limits Limits) (*Registry, error) {
	r := &Registry{
		customDir:        strings.TrimSpace(customDir),
		defaultProfileID: strings.TrimSpace(defaultProfileID),
		limits:           limits,
	}
	if r.defaultProfileID == "" {
		r.defaultProfileID = models.DocumentProfileBuiltinNote
	}
	status := r.Reload()
	if status.ProfileCount == 0 {
		return nil, errors.New("no valid document profiles are available")
	}
	return r, nil
}

func (r *Registry) Reload() RegistryStatus {
	r.mu.RLock()
	previous := make(map[string]DocumentProfile, len(r.profiles))
	for key, profile := range r.profiles {
		previous[key] = profile
	}
	r.mu.RUnlock()

	profiles := map[string]DocumentProfile{}
	latest := map[string]DocumentProfile{}
	issues := []models.ValidationIssue{}
	for _, raw := range documentformats.Builtins() {
		profile, err := Parse([]byte(raw), r.limits)
		if err != nil {
			issues = append(issues, issue("thoughtflow.profile.invalid", "", err.Error()))
			continue
		}
		insertProfile(profiles, latest, profile, &issues)
	}
	if r.customDir != "" {
		published := filepath.Join(r.customDir, "published")
		_ = filepath.WalkDir(published, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				issues = append(issues, issue("thoughtflow.profile.invalid", "", walkErr.Error()))
				return nil
			}
			if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".md" {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				issues = append(issues, issue("thoughtflow.profile.invalid", "", fmt.Sprintf("reject symbolic-link profile path %q", path)))
				return nil
			}
			if err := workspace.EnsureInside(r.customDir, path); err != nil {
				issues = append(issues, issue("thoughtflow.profile.invalid", "", fmt.Sprintf("reject profile path %q", path)))
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				issues = append(issues, issue("thoughtflow.profile.invalid", "", err.Error()))
				keepPreviousPublishedProfile(r.customDir, path, previous, profiles)
				return nil
			}
			profile, err := Parse(raw, r.limits)
			if err != nil {
				issues = append(issues, issue("thoughtflow.profile.invalid", "", fmt.Sprintf("%s: %v", path, err)))
				keepPreviousPublishedProfile(r.customDir, path, previous, profiles)
				return nil
			}
			if strings.HasPrefix(profile.Ref.ProfileID, "builtin.") {
				issues = append(issues, issue("thoughtflow.profile.invalid", "", fmt.Sprintf("custom profile %q uses reserved namespace", profile.Ref.ProfileID)))
				return nil
			}
			key := profileKey(profile.Ref.ProfileID, profile.Ref.Version)
			if prior, ok := previous[key]; ok && prior.Ref.ContentHash != profile.Ref.ContentHash {
				issues = append(issues, issue("thoughtflow.profile.conflict", "", fmt.Sprintf("published profile %s changed content hash; keeping the previous snapshot for existing thoughts", key)))
				profiles[key] = prior
				return nil
			}
			insertProfile(profiles, latest, profile, &issues)
			return nil
		})
	}
	r.mu.Lock()
	r.profiles = profiles
	r.latest = latest
	r.issues = issues
	r.mu.Unlock()
	return RegistryStatus{ProfileCount: len(profiles), Issues: append([]models.ValidationIssue(nil), issues...)}
}

func (r *Registry) ListEnabled() []DocumentProfileDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []DocumentProfileDescriptor{}
	for _, profile := range r.latest {
		if profile.Enabled && profile.AutoMatch.Enabled {
			out = append(out, profile.Descriptor())
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority == out[j].Priority {
			return out[i].ID < out[j].ID
		}
		return out[i].Priority > out[j].Priority
	})
	return out
}

func (r *Registry) ListProfiles() []DocumentProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]DocumentProfile, 0, len(r.latest))
	for _, profile := range r.latest {
		if profile.Enabled {
			out = append(out, profile)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.ProfileID < out[j].Ref.ProfileID })
	return out
}

func (r *Registry) Resolve(ref models.DocumentProfileRef) (DocumentProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.profiles[profileKey(ref.ProfileID, ref.Version)]
	if !ok {
		return DocumentProfile{}, fmt.Errorf("%w: %s@%d", ErrProfileNotFound, ref.ProfileID, ref.Version)
	}
	if ref.ContentHash != "" && ref.ContentHash != profile.Ref.ContentHash {
		return DocumentProfile{}, fmt.Errorf("%w: %s@%d", ErrProfileConflict, ref.ProfileID, ref.Version)
	}
	return profile, nil
}

func (r *Registry) ResolveLatest(profileID string) (DocumentProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.latest[strings.TrimSpace(profileID)]
	if !ok {
		return DocumentProfile{}, fmt.Errorf("%w: %s", ErrProfileNotFound, profileID)
	}
	return profile, nil
}

func (r *Registry) Default() DocumentProfile {
	profile, err := r.ResolveLatest(r.defaultProfileID)
	if err == nil {
		return profile
	}
	profile, _ = r.ResolveLatest(models.DocumentProfileBuiltinNote)
	return profile
}

func (r *Registry) ValidateFormat(raw []byte) FormatValidationResult {
	return Validate(raw, r.limits)
}

func (r *Registry) Issues() []models.ValidationIssue {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]models.ValidationIssue(nil), r.issues...)
}

func insertProfile(profiles map[string]DocumentProfile, latest map[string]DocumentProfile, profile DocumentProfile, issues *[]models.ValidationIssue) {
	key := profileKey(profile.Ref.ProfileID, profile.Ref.Version)
	if existing, ok := profiles[key]; ok && existing.Ref.ContentHash != profile.Ref.ContentHash {
		*issues = append(*issues, issue("thoughtflow.profile.conflict", "", fmt.Sprintf("profile %s has conflicting content hashes", key)))
		delete(profiles, key)
		delete(latest, profile.Ref.ProfileID)
		return
	}
	profiles[key] = profile
	current, ok := latest[profile.Ref.ProfileID]
	if !ok || profile.Ref.Version > current.Ref.Version {
		latest[profile.Ref.ProfileID] = profile
	}
}

func profileKey(id string, version int) string {
	return strings.TrimSpace(id) + "@" + strconv.Itoa(version)
}

func keepPreviousPublishedProfile(customDir, path string, previous, profiles map[string]DocumentProfile) {
	rel, err := filepath.Rel(filepath.Join(customDir, "published"), path)
	if err != nil {
		return
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 2 || !strings.HasPrefix(parts[1], "v") || strings.ToLower(filepath.Ext(parts[1])) != ".md" {
		return
	}
	version, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(parts[1], "v"), filepath.Ext(parts[1])))
	if err != nil || version <= 0 {
		return
	}
	key := profileKey(parts[0], version)
	if prior, ok := previous[key]; ok {
		profiles[key] = prior
	}
}

func rejectSymlinkComponents(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("document profile path contains symbolic link: %s", current)
		}
	}
	return nil
}
