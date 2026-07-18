package localfilesystem

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
	"github.com/maboo-run/shadoc/internal/execution"
)

const (
	settingsMetadataKey = "local-filesystem.settings"
	maximumRootCount    = 32
)

type Settings struct {
	Roots []string `json:"roots"`
}

type BrowseResult struct {
	Path    string                  `json:"path"`
	Entries []agentfilesystem.Entry `json:"entries"`
}

type Storage interface {
	Metadata(context.Context, string) (string, error)
	SetMetadata(context.Context, string, string) error
}

// Service applies the same path validation and symlink boundary checks used by
// Agent browsing while loading roots from the control service configuration.
// It also implements execution.Engine so task scope previews cannot bypass a
// root change made after startup.
type Service struct {
	storage Storage
	style   string
	mu      sync.RWMutex
	roots   []string
}

func New(ctx context.Context, storage Storage, style string) (*Service, error) {
	if storage == nil || (style != "posix" && style != "windows") {
		return nil, errors.New("local filesystem storage and path style are required")
	}
	roots := agentfilesystem.DefaultRoots(style)
	encoded, err := storage.Metadata(ctx, settingsMetadataKey)
	if err == nil {
		var settings Settings
		if json.Unmarshal([]byte(encoded), &settings) != nil {
			return nil, errors.New("decode local filesystem settings")
		}
		roots, err = normalizeRoots(settings.Roots, style, false)
		if err != nil {
			return nil, fmt.Errorf("validate local filesystem settings: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load local filesystem settings: %w", err)
	}
	return &Service{storage: storage, style: style, roots: roots}, nil
}

func (s *Service) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Settings{Roots: append([]string(nil), s.roots...)}
}

func (s *Service) SaveSettings(ctx context.Context, roots []string) (Settings, error) {
	normalized, err := normalizeRoots(roots, s.style, true)
	if err != nil {
		return Settings{}, err
	}
	settings := Settings{Roots: normalized}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return Settings{}, err
	}
	if err := s.storage.SetMetadata(ctx, settingsMetadataKey, string(encoded)); err != nil {
		return Settings{}, fmt.Errorf("save local filesystem settings: %w", err)
	}
	s.mu.Lock()
	s.roots = append([]string(nil), normalized...)
	s.mu.Unlock()
	return settings, nil
}

func (s *Service) Browse(ctx context.Context, path string) (BrowseResult, error) {
	definition, _ := json.Marshal(agentfilesystem.Definition{Operation: agentfilesystem.Browse, Path: path})
	outcome, err := s.Run(ctx, execution.Assignment{Engine: agentfilesystem.Kind, Target: execution.Target{Kind: execution.Local}, Definition: definition})
	if err != nil {
		return BrowseResult{}, err
	}
	encoded, err := json.Marshal(outcome.Summary)
	if err != nil {
		return BrowseResult{}, err
	}
	var result BrowseResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return BrowseResult{}, err
	}
	return result, nil
}

func (s *Service) CreateDirectory(ctx context.Context, path string) error {
	definition, _ := json.Marshal(agentfilesystem.Definition{Operation: agentfilesystem.CreateDirectory, Path: path})
	_, err := s.Run(ctx, execution.Assignment{Engine: agentfilesystem.Kind, Target: execution.Target{Kind: execution.Local}, Definition: definition})
	return err
}

func (s *Service) Kind() execution.EngineKind { return agentfilesystem.Kind }

func (s *Service) Probe(context.Context) error { return nil }

func (s *Service) Validate(raw json.RawMessage) error {
	return agentfilesystem.New(s.style, s.Settings().Roots).Validate(raw)
}

func (s *Service) Run(ctx context.Context, assignment execution.Assignment) (execution.Outcome, error) {
	return agentfilesystem.New(s.style, s.Settings().Roots).Run(ctx, assignment)
}

func normalizeRoots(values []string, style string, requireAccessible bool) ([]string, error) {
	if len(values) == 0 || len(values) > maximumRootCount {
		return nil, errors.New("至少配置一个、最多配置 32 个允许根目录")
	}
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("允许根目录不能为空或包含控制字符")
		}
		if style == "windows" {
			value = strings.ReplaceAll(value, "/", "\\")
			if len(value) < 3 || value[1] != ':' || value[2] != '\\' {
				return nil, errors.New("允许根目录必须使用绝对路径")
			}
		} else if !filepath.IsAbs(value) {
			return nil, errors.New("允许根目录必须使用绝对路径")
		}
		value = filepath.Clean(value)
		if requireAccessible {
			resolved, err := filepath.EvalSymlinks(value)
			if err != nil {
				return nil, fmt.Errorf("允许根目录不可访问：%s", value)
			}
			info, err := os.Stat(resolved)
			if err != nil || !info.IsDir() {
				return nil, fmt.Errorf("允许根目录不是可访问目录：%s", value)
			}
			directory, err := os.Open(resolved)
			if err != nil {
				return nil, fmt.Errorf("控制服务账号无法读取允许根目录：%s", value)
			}
			_ = directory.Close()
		}
		unique[value] = struct{}{}
	}
	items := make([]string, 0, len(unique))
	for value := range unique {
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i]) < strings.ToLower(items[j]) })
	result := make([]string, 0, len(items))
	for _, candidate := range items {
		contained := false
		for _, parent := range result {
			if pathContains(parent, candidate, style == "windows") {
				contained = true
				break
			}
		}
		if !contained {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func pathContains(root, candidate string, windows bool) bool {
	if windows {
		root = strings.ToLower(strings.TrimRight(strings.ReplaceAll(root, "/", "\\"), "\\"))
		candidate = strings.ToLower(strings.TrimRight(strings.ReplaceAll(candidate, "/", "\\"), "\\"))
		return candidate == root || strings.HasPrefix(candidate, root+"\\")
	}
	root, candidate = filepath.Clean(root), filepath.Clean(candidate)
	if root == string(filepath.Separator) {
		return filepath.IsAbs(candidate)
	}
	return candidate == root || strings.HasPrefix(candidate, root+string(filepath.Separator))
}
