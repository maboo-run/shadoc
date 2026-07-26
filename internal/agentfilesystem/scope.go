package agentfilesystem

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
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
	TotalFiles      int               `json:"totalFiles"`
	IncludedFiles   int               `json:"includedFiles"`
	IncludedBytes   int64             `json:"includedBytes"`
	ExcludedFiles   int               `json:"excludedFiles"`
	ExcludedBytes   int64             `json:"excludedBytes"`
	UnreadableItems int               `json:"unreadableItems"`
	AttentionItems  int               `json:"attentionItems"`
	Truncated       bool              `json:"truncated"`
	ActiveRules     []ScopeRuleImpact `json:"activeRules"`
	Suggestions     []ScopeSuggestion `json:"suggestions"`
}

type ScopeEntryType string

const (
	ScopeRegularFile ScopeEntryType = "file"
	ScopeDirectory   ScopeEntryType = "directory"
	ScopeSymlink     ScopeEntryType = "symlink"
	ScopeSpecialFile ScopeEntryType = "special"
	ScopeUnknown     ScopeEntryType = "unknown"
)

type ScopeDisposition string

const (
	ScopeIncluded   ScopeDisposition = "included"
	ScopeExcluded   ScopeDisposition = "excluded"
	ScopeUnreadable ScopeDisposition = "unreadable"
	ScopeAttention  ScopeDisposition = "attention"
)

const (
	ScopeReasonExclusionRule   = "exclusion_rule"
	ScopeReasonPermission      = "permission_denied"
	ScopeReasonUnreadable      = "unreadable"
	ScopeReasonSpecialFile     = "special_file"
	ScopeReasonMetadataFailure = "metadata_unavailable"
)

type ScopeEntry struct {
	Ordinal     int64            `json:"ordinal"`
	Path        string           `json:"path"`
	Type        ScopeEntryType   `json:"type"`
	Disposition ScopeDisposition `json:"disposition"`
	Size        int64            `json:"size,omitempty"`
	ReasonCode  string           `json:"reasonCode,omitempty"`
	RuleIndexes []int            `json:"ruleIndexes,omitempty"`
}

type ScopeVisitor func(ScopeEntry) error

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
	return ScanScope(ctx, root, exclusions, limit, nil)
}

func ScanScope(ctx context.Context, root string, exclusions []string, limit int, visit ScopeVisitor) (ScopeSummary, error) {
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
	var ordinal int64
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if current == root {
			if walkErr != nil {
				summary.UnreadableItems++
			}
			return nil
		}
		if summary.ScannedItems >= limit {
			summary.Truncated = true
			return filepath.SkipAll
		}
		summary.ScannedItems++
		relative, relativeErr := filepath.Rel(root, current)
		if relativeErr != nil {
			summary.UnreadableItems++
			return nil
		}
		relative = filepath.ToSlash(relative)
		if walkErr != nil {
			summary.UnreadableItems++
			entryType := ScopeUnknown
			if entry != nil && entry.IsDir() {
				entryType = ScopeDirectory
			} else {
				summary.TotalFiles++
			}
			ordinal++
			if err := emitScopeEntry(visit, ScopeEntry{Ordinal: ordinal, Path: relative, Type: entryType, Disposition: ScopeUnreadable, ReasonCode: scopeReadReason(walkErr)}); err != nil {
				return err
			}
			if entryType == ScopeDirectory {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(current)
		if err != nil {
			summary.UnreadableItems++
			if !entry.IsDir() {
				summary.TotalFiles++
			}
			ordinal++
			if emitErr := emitScopeEntry(visit, ScopeEntry{Ordinal: ordinal, Path: relative, Type: scopeEntryType(entry, nil), Disposition: ScopeUnreadable, ReasonCode: ScopeReasonMetadataFailure}); emitErr != nil {
				return emitErr
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		size := info.Size()
		matchedRules := make([]int, 0, 2)
		for index, rule := range exclusions {
			if matchScopePathOrAncestor(rule, relative) {
				matchedRules = append(matchedRules, index)
				if !entry.IsDir() {
					summary.ActiveRules[index].MatchedFiles++
					summary.ActiveRules[index].EstimatedBytes += size
				}
			}
		}
		if !entry.IsDir() {
			for index := range summary.Suggestions {
				if matchScopePathOrAncestor(summary.Suggestions[index].Rule, relative) {
					summary.Suggestions[index].MatchedFiles++
					summary.Suggestions[index].EstimatedBytes += size
				}
			}
		}
		entryType := scopeEntryType(entry, info)
		disposition := ScopeIncluded
		reason := ""
		if len(matchedRules) > 0 {
			disposition = ScopeExcluded
			reason = ScopeReasonExclusionRule
		}
		if entry.IsDir() {
			if readErr := checkDirectoryReadable(current); readErr != nil {
				summary.UnreadableItems++
				ordinal++
				if emitErr := emitScopeEntry(visit, ScopeEntry{
					Ordinal: ordinal, Path: relative, Type: ScopeDirectory,
					Disposition: ScopeUnreadable, ReasonCode: scopeReadReason(readErr),
				}); emitErr != nil {
					return emitErr
				}
				return filepath.SkipDir
			}
		} else if len(matchedRules) == 0 && entryType == ScopeSpecialFile {
			disposition = ScopeAttention
			reason = ScopeReasonSpecialFile
			summary.AttentionItems++
		} else if len(matchedRules) == 0 && entryType == ScopeRegularFile {
			file, openErr := os.Open(current)
			if openErr == nil {
				openErr = file.Close()
			}
			if openErr != nil {
				disposition = ScopeUnreadable
				reason = scopeReadReason(openErr)
				summary.UnreadableItems++
			}
		}
		if !entry.IsDir() {
			summary.TotalFiles++
		}
		switch {
		case entry.IsDir():
		case disposition == ScopeExcluded:
			summary.ExcludedFiles++
			summary.ExcludedBytes += size
		case disposition == ScopeIncluded:
			summary.IncludedFiles++
			summary.IncludedBytes += size
		}
		ordinal++
		if err := emitScopeEntry(visit, ScopeEntry{
			Ordinal: ordinal, Path: relative, Type: entryType, Disposition: disposition,
			Size: size, ReasonCode: reason, RuleIndexes: matchedRules,
		}); err != nil {
			return err
		}
		return nil
	})
	return summary, err
}

func checkDirectoryReadable(value string) error {
	directory, err := os.Open(value)
	if err != nil {
		return err
	}
	_, readErr := directory.Readdirnames(1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	return errors.Join(readErr, directory.Close())
}

func emitScopeEntry(visit ScopeVisitor, entry ScopeEntry) error {
	if visit == nil {
		return nil
	}
	return visit(entry)
}

func scopeEntryType(entry fs.DirEntry, info fs.FileInfo) ScopeEntryType {
	if entry != nil && entry.IsDir() {
		return ScopeDirectory
	}
	mode := fs.FileMode(0)
	if info != nil {
		mode = info.Mode()
	} else if entry != nil {
		mode = entry.Type()
	}
	switch {
	case mode&fs.ModeSymlink != 0:
		return ScopeSymlink
	case mode.IsRegular():
		return ScopeRegularFile
	case mode != 0:
		return ScopeSpecialFile
	default:
		return ScopeUnknown
	}
}

func scopeReadReason(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return ScopeReasonPermission
	}
	return ScopeReasonUnreadable
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

func SummaryMap(summary ScopeSummary) map[string]any {
	return map[string]any{
		"scannedItems": summary.ScannedItems, "totalFiles": summary.TotalFiles, "includedFiles": summary.IncludedFiles, "includedBytes": summary.IncludedBytes,
		"excludedFiles": summary.ExcludedFiles, "excludedBytes": summary.ExcludedBytes, "unreadableItems": summary.UnreadableItems,
		"attentionItems": summary.AttentionItems, "truncated": summary.Truncated, "activeRules": summary.ActiveRules, "suggestions": summary.Suggestions,
	}
}

func scopeSummaryMap(summary ScopeSummary) map[string]any {
	return SummaryMap(summary)
}
