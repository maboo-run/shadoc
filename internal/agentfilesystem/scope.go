package agentfilesystem

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

const MaxScopeItems = 100_000

type ScopeRuleImpact struct {
	Rule           string `json:"rule"`
	MatchedFiles   int    `json:"matchedFiles"`
	EstimatedBytes int64  `json:"estimatedBytes"`
}

type ScopeSuggestion struct {
	Rule           string `json:"rule"`
	Reason         string `json:"reason"`
	MatchedFiles   int    `json:"matchedFiles"`
	EstimatedBytes int64  `json:"estimatedBytes"`
}

type ScopeSummary struct {
	ScannedItems    int               `json:"scannedItems"`
	IncludedFiles   int               `json:"includedFiles"`
	IncludedBytes   int64             `json:"includedBytes"`
	ExcludedFiles   int               `json:"excludedFiles"`
	ExcludedBytes   int64             `json:"excludedBytes"`
	UnreadableItems int               `json:"unreadableItems"`
	Truncated       bool              `json:"truncated"`
	ActiveRules     []ScopeRuleImpact `json:"activeRules"`
	Suggestions     []ScopeSuggestion `json:"suggestions"`
}

var defaultScopeSuggestions = []ScopeSuggestion{
	{Rule: "**/.cache", Reason: "应用缓存通常可重新生成"},
	{Rule: "**/node_modules", Reason: "依赖目录通常可由锁文件重新安装"},
	{Rule: "**/.DS_Store", Reason: "macOS 目录显示元数据通常不属于业务数据"},
	{Rule: "**/@eaDir", Reason: "Synology 索引缩略图通常可重新生成"},
}

func scanScope(root string, exclusions []string, limit int) (ScopeSummary, error) {
	return scanScopeContext(context.Background(), root, exclusions, limit)
}

func scanScopeContext(ctx context.Context, root string, exclusions []string, limit int) (ScopeSummary, error) {
	if limit <= 0 {
		limit = MaxScopeItems
	}
	summary := ScopeSummary{
		ActiveRules: make([]ScopeRuleImpact, len(exclusions)),
		Suggestions: append([]ScopeSuggestion(nil), defaultScopeSuggestions...),
	}
	for index, rule := range exclusions {
		summary.ActiveRules[index].Rule = rule
	}
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if current == root && walkErr == nil {
			return nil
		}
		if summary.ScannedItems >= limit {
			summary.Truncated = true
			return filepath.SkipAll
		}
		summary.ScannedItems++
		if walkErr != nil {
			summary.UnreadableItems++
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			summary.UnreadableItems++
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			summary.UnreadableItems++
			return nil
		}
		relative = filepath.ToSlash(relative)
		size := info.Size()
		excluded := false
		for index, rule := range exclusions {
			if matchScopePathOrAncestor(rule, relative) {
				excluded = true
				summary.ActiveRules[index].MatchedFiles++
				summary.ActiveRules[index].EstimatedBytes += size
			}
		}
		for index := range summary.Suggestions {
			if matchScopePathOrAncestor(summary.Suggestions[index].Rule, relative) {
				summary.Suggestions[index].MatchedFiles++
				summary.Suggestions[index].EstimatedBytes += size
			}
		}
		if excluded {
			summary.ExcludedFiles++
			summary.ExcludedBytes += size
		} else {
			summary.IncludedFiles++
			summary.IncludedBytes += size
		}
		return nil
	})
	return summary, err
}

func matchScopePathOrAncestor(pattern, value string) bool {
	for candidate := strings.Trim(value, "/"); candidate != ""; {
		if matchScopePattern(pattern, candidate) {
			return true
		}
		separator := strings.LastIndex(candidate, "/")
		if separator < 0 {
			break
		}
		candidate = candidate[:separator]
	}
	return false
}

func matchScopePattern(pattern, value string) bool {
	pattern = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(pattern)), "./")
	pattern = strings.TrimPrefix(pattern, "/")
	value = strings.Trim(filepath.ToSlash(value), "/")
	if pattern == "" || value == "" {
		return pattern == value
	}
	return matchScopeSegments(strings.Split(pattern, "/"), strings.Split(value, "/"))
}

func matchScopeSegments(pattern, value []string) bool {
	if len(pattern) == 0 {
		return len(value) == 0
	}
	if pattern[0] == "**" {
		if matchScopeSegments(pattern[1:], value) {
			return true
		}
		return len(value) > 0 && matchScopeSegments(pattern, value[1:])
	}
	if len(value) == 0 {
		return false
	}
	matched, err := path.Match(pattern[0], value[0])
	return err == nil && matched && matchScopeSegments(pattern[1:], value[1:])
}

func scopeSummaryMap(summary ScopeSummary) map[string]any {
	return map[string]any{
		"scannedItems": summary.ScannedItems, "includedFiles": summary.IncludedFiles, "includedBytes": summary.IncludedBytes,
		"excludedFiles": summary.ExcludedFiles, "excludedBytes": summary.ExcludedBytes, "unreadableItems": summary.UnreadableItems,
		"truncated": summary.Truncated, "activeRules": summary.ActiveRules, "suggestions": summary.Suggestions,
	}
}
