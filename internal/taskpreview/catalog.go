package taskpreview

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/maboo-run/shadoc/internal/agentfilesystem"
)

var (
	ErrCatalogUnavailable = errors.New("task scope catalog is unavailable")
	ErrInvalidEntryCursor = errors.New("task scope entry cursor is invalid")
)

type EntryQuery struct {
	View   string
	Search string
	Type   string
	Parent string
	Browse bool
	Cursor string
	Limit  int
}

type EntryItem struct {
	agentfilesystem.ScopeEntry
	Coverage        string `json:"coverage,omitempty"`
	TotalFiles      int    `json:"totalFiles,omitempty"`
	IncludedFiles   int    `json:"includedFiles,omitempty"`
	ExcludedFiles   int    `json:"excludedFiles,omitempty"`
	ExcludedItems   int    `json:"excludedItems,omitempty"`
	UnreadableItems int    `json:"unreadableItems,omitempty"`
	AttentionItems  int    `json:"attentionItems,omitempty"`
}

type EntryPage struct {
	Items      []EntryItem `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
	Truncated  bool        `json:"truncated"`
}

type catalog struct {
	root     string
	key      []byte
	now      func() time.Time
	schedule func(time.Duration, func())
}

type catalogWriter struct {
	file    *os.File
	encoder *json.Encoder
	path    string
	expire  func()
}

type entryCursor struct {
	Version     int    `json:"v"`
	Offset      int64  `json:"o"`
	Fingerprint string `json:"f"`
}

var safePreviewID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

const catalogTTL = 15 * time.Minute

func newCatalog(root string, now func() time.Time) (*catalog, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrCatalogUnavailable
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	result := &catalog{
		root: root, key: key, now: now,
		schedule: func(delay time.Duration, remove func()) {
			time.AfterFunc(delay, remove)
		},
	}
	if err := result.cleanup(now().UTC()); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *catalog) begin(previewID string) (*catalogWriter, error) {
	if c == nil || !safePreviewID.MatchString(previewID) {
		return nil, ErrCatalogUnavailable
	}
	if err := c.cleanup(c.now().UTC()); err != nil {
		return nil, err
	}
	value := c.path(previewID)
	file, err := os.OpenFile(value, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(value)
		return nil, err
	}
	return &catalogWriter{
		file: file, encoder: json.NewEncoder(file), path: value,
		expire: func() {
			c.schedule(catalogTTL, func() { _ = os.Remove(value) })
		},
	}, nil
}

func (w *catalogWriter) write(entry agentfilesystem.ScopeEntry) error {
	if w == nil || w.file == nil {
		return ErrCatalogUnavailable
	}
	if !safeCatalogEntryPath(entry.Path) {
		return errors.New("scope inventory entry must use a safe relative path")
	}
	return w.encoder.Encode(entry)
}

func (w *catalogWriter) close() error {
	if w == nil || w.file == nil {
		return nil
	}
	file := w.file
	w.file = nil
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if w.expire != nil {
		w.expire()
		w.expire = nil
	}
	return nil
}

func (w *catalogWriter) abort() {
	if w == nil {
		return
	}
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	w.expire = nil
	_ = os.Remove(w.path)
}

func (c *catalog) remove(previewID string) {
	if c == nil || !safePreviewID.MatchString(previewID) {
		return
	}
	_ = os.Remove(c.path(previewID))
}

func (c *catalog) page(previewID string, input EntryQuery) (EntryPage, error) {
	query, err := normalizeEntryQuery(input)
	if err != nil {
		return EntryPage{}, err
	}
	if c == nil || !safePreviewID.MatchString(previewID) {
		return EntryPage{}, ErrCatalogUnavailable
	}
	if query.Browse {
		return c.browsePage(previewID, query)
	}
	fingerprint := entryQueryFingerprint(query)
	offset := int64(0)
	if query.Cursor != "" {
		cursor, err := c.decodeCursor(query.Cursor, fingerprint)
		if err != nil {
			return EntryPage{}, err
		}
		offset = cursor.Offset
	}
	file, err := os.Open(c.path(previewID))
	if errors.Is(err, os.ErrNotExist) {
		return EntryPage{}, ErrCatalogUnavailable
	}
	if err != nil {
		return EntryPage{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return EntryPage{}, err
	}
	if offset < 0 || offset > info.Size() {
		return EntryPage{}, ErrInvalidEntryCursor
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return EntryPage{}, err
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	page := EntryPage{Items: make([]EntryItem, 0)}
	position := offset
	for {
		lineStart := position
		line, readErr := reader.ReadBytes('\n')
		position += int64(len(line))
		if len(line) > 0 {
			var entry agentfilesystem.ScopeEntry
			if json.Unmarshal(line, &entry) != nil || !safeCatalogEntryPath(entry.Path) {
				return EntryPage{}, ErrCatalogUnavailable
			}
			if entryMatches(entry, query) {
				if len(page.Items) == query.Limit {
					page.NextCursor, err = c.encodeCursor(entryCursor{Version: 1, Offset: lineStart, Fingerprint: fingerprint})
					if err != nil {
						return EntryPage{}, err
					}
					page.Truncated = true
					break
				}
				page.Items = append(page.Items, EntryItem{ScopeEntry: entry})
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return EntryPage{}, readErr
		}
	}
	return page, nil
}

func (c *catalog) browsePage(previewID string, query EntryQuery) (EntryPage, error) {
	fingerprint := entryQueryFingerprint(query)
	offset := int64(0)
	if query.Cursor != "" {
		cursor, err := c.decodeCursor(query.Cursor, fingerprint)
		if err != nil {
			return EntryPage{}, err
		}
		offset = cursor.Offset
	}
	file, err := os.Open(c.path(previewID))
	if errors.Is(err, os.ErrNotExist) {
		return EntryPage{}, ErrCatalogUnavailable
	}
	if err != nil {
		return EntryPage{}, err
	}
	defer file.Close()

	items := make([]*EntryItem, 0, 32)
	byPath := make(map[string]*EntryItem)
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var entry agentfilesystem.ScopeEntry
			if json.Unmarshal(line, &entry) != nil || !safeCatalogEntryPath(entry.Path) {
				return EntryPage{}, ErrCatalogUnavailable
			}
			childPath, direct, ok := directChildPath(query.Parent, entry.Path)
			if ok {
				item := byPath[childPath]
				if item == nil {
					item = &EntryItem{ScopeEntry: agentfilesystem.ScopeEntry{
						Ordinal: entry.Ordinal, Path: childPath, Type: agentfilesystem.ScopeDirectory,
						Disposition: agentfilesystem.ScopeIncluded,
					}}
					byPath[childPath] = item
					items = append(items, item)
				}
				if direct {
					item.ScopeEntry = entry
				}
				accumulateBrowseEntry(item, entry)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return EntryPage{}, readErr
		}
	}

	filtered := make([]EntryItem, 0, len(items))
	for _, item := range items {
		finalizeBrowseEntry(item)
		if browseEntryMatches(*item, query) {
			filtered = append(filtered, *item)
		}
	}
	if offset < 0 || offset > int64(len(filtered)) {
		return EntryPage{}, ErrInvalidEntryCursor
	}
	end := offset + int64(query.Limit)
	if end > int64(len(filtered)) {
		end = int64(len(filtered))
	}
	page := EntryPage{Items: append([]EntryItem(nil), filtered[offset:end]...)}
	if end < int64(len(filtered)) {
		page.NextCursor, err = c.encodeCursor(entryCursor{Version: 1, Offset: end, Fingerprint: fingerprint})
		if err != nil {
			return EntryPage{}, err
		}
		page.Truncated = true
	}
	return page, nil
}

func directChildPath(parent, entryPath string) (string, bool, bool) {
	relative := entryPath
	if parent != "" {
		prefix := parent + "/"
		if !strings.HasPrefix(entryPath, prefix) {
			return "", false, false
		}
		relative = strings.TrimPrefix(entryPath, prefix)
	}
	separator := strings.IndexByte(relative, '/')
	if separator < 0 {
		return entryPath, true, true
	}
	child := relative[:separator]
	if parent == "" {
		return child, false, true
	}
	return parent + "/" + child, false, true
}

func accumulateBrowseEntry(item *EntryItem, entry agentfilesystem.ScopeEntry) {
	for _, ruleIndex := range entry.RuleIndexes {
		found := false
		for _, existing := range item.RuleIndexes {
			if existing == ruleIndex {
				found = true
				break
			}
		}
		if !found {
			item.RuleIndexes = append(item.RuleIndexes, ruleIndex)
		}
	}
	if entry.Type == agentfilesystem.ScopeDirectory {
		switch entry.Disposition {
		case agentfilesystem.ScopeExcluded:
			item.ExcludedItems++
		case agentfilesystem.ScopeUnreadable:
			item.UnreadableItems++
		case agentfilesystem.ScopeAttention:
			item.AttentionItems++
		}
		return
	}
	item.TotalFiles++
	switch entry.Disposition {
	case agentfilesystem.ScopeIncluded:
		item.IncludedFiles++
	case agentfilesystem.ScopeExcluded:
		item.ExcludedFiles++
	case agentfilesystem.ScopeUnreadable:
		item.UnreadableItems++
	case agentfilesystem.ScopeAttention:
		item.AttentionItems++
	}
}

func finalizeBrowseEntry(item *EntryItem) {
	if item.Type != agentfilesystem.ScopeDirectory {
		item.TotalFiles = 0
		item.IncludedFiles = 0
		item.ExcludedFiles = 0
		item.ExcludedItems = 0
		item.UnreadableItems = 0
		item.AttentionItems = 0
		return
	}
	switch {
	case item.Disposition == agentfilesystem.ScopeUnreadable && item.IncludedFiles == 0:
		item.Coverage = "unavailable"
		item.Disposition = agentfilesystem.ScopeUnreadable
	case item.Disposition == agentfilesystem.ScopeExcluded && item.IncludedFiles == 0 && item.UnreadableItems == 0 && item.AttentionItems == 0:
		item.Coverage = "excluded"
		item.Disposition = agentfilesystem.ScopeExcluded
	case item.ExcludedFiles == 0 && item.ExcludedItems == 0 && item.UnreadableItems == 0 && item.AttentionItems == 0:
		item.Coverage = "full"
		item.Disposition = agentfilesystem.ScopeIncluded
	case item.IncludedFiles > 0 || item.Disposition == agentfilesystem.ScopeIncluded:
		item.Coverage = "partial"
		item.Disposition = agentfilesystem.ScopeAttention
	case (item.ExcludedFiles > 0 || item.ExcludedItems > 0) && item.UnreadableItems == 0 && item.AttentionItems == 0:
		item.Coverage = "excluded"
		item.Disposition = agentfilesystem.ScopeExcluded
	default:
		item.Coverage = "unavailable"
		item.Disposition = agentfilesystem.ScopeUnreadable
	}
}

func browseEntryMatches(entry EntryItem, query EntryQuery) bool {
	if query.Type != "" && string(entry.Type) != query.Type {
		return false
	}
	if query.Search != "" && !strings.Contains(strings.ToLower(path.Base(entry.Path)), strings.ToLower(query.Search)) {
		return false
	}
	switch query.View {
	case "included":
		return entry.Disposition == agentfilesystem.ScopeIncluded || entry.IncludedFiles > 0
	case "excluded":
		return entry.Disposition == agentfilesystem.ScopeExcluded || entry.ExcludedFiles > 0 || entry.ExcludedItems > 0
	case "attention":
		return entry.Disposition == agentfilesystem.ScopeUnreadable || entry.Disposition == agentfilesystem.ScopeAttention ||
			entry.UnreadableItems > 0 || entry.AttentionItems > 0
	default:
		return true
	}
}

func normalizeEntryQuery(input EntryQuery) (EntryQuery, error) {
	query := input
	query.View = strings.TrimSpace(query.View)
	query.Search = strings.TrimSpace(query.Search)
	query.Type = strings.TrimSpace(query.Type)
	query.Parent = strings.Trim(strings.TrimSpace(query.Parent), "/")
	query.Cursor = strings.TrimSpace(query.Cursor)
	if query.View == "" {
		query.View = "all"
	}
	switch query.View {
	case "all", "included", "attention", "excluded":
	default:
		return EntryQuery{}, errors.New("invalid task scope entry view")
	}
	switch query.Type {
	case "", string(agentfilesystem.ScopeRegularFile), string(agentfilesystem.ScopeDirectory), string(agentfilesystem.ScopeSymlink), string(agentfilesystem.ScopeSpecialFile):
	default:
		return EntryQuery{}, errors.New("invalid task scope entry type")
	}
	if len(query.Search) > 256 || strings.ContainsAny(query.Search, "\x00\r\n") {
		return EntryQuery{}, errors.New("invalid task scope entry search")
	}
	if query.Parent != "" && !safeCatalogEntryPath(query.Parent) {
		return EntryQuery{}, errors.New("invalid task scope parent path")
	}
	if !query.Browse && query.Parent != "" {
		return EntryQuery{}, errors.New("task scope parent path requires directory browsing")
	}
	if query.Limit == 0 {
		query.Limit = 200
	}
	if query.Limit < 1 || query.Limit > 200 {
		return EntryQuery{}, errors.New("task scope entry limit must be between 1 and 200")
	}
	return query, nil
}

func entryMatches(entry agentfilesystem.ScopeEntry, query EntryQuery) bool {
	if query.Type != "" && string(entry.Type) != query.Type {
		return false
	}
	if query.Search != "" && !strings.Contains(strings.ToLower(entry.Path), strings.ToLower(query.Search)) {
		return false
	}
	switch query.View {
	case "included":
		return entry.Disposition == agentfilesystem.ScopeIncluded
	case "excluded":
		return entry.Disposition == agentfilesystem.ScopeExcluded
	case "attention":
		return entry.Disposition == agentfilesystem.ScopeUnreadable || entry.Disposition == agentfilesystem.ScopeAttention
	default:
		return true
	}
}

func safeCatalogEntryPath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func entryQueryFingerprint(query EntryQuery) string {
	sum := sha256.Sum256([]byte(query.View + "\x00" + strings.ToLower(query.Search) + "\x00" + query.Type + "\x00" +
		query.Parent + "\x00" + strconv.FormatBool(query.Browse) + "\x00" + strconv.Itoa(query.Limit)))
	return hex.EncodeToString(sum[:])
}

func (c *catalog) encodeCursor(cursor entryCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (c *catalog) decodeCursor(raw, fingerprint string) (entryCursor, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return entryCursor{}, ErrInvalidEntryCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return entryCursor{}, ErrInvalidEntryCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return entryCursor{}, ErrInvalidEntryCursor
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return entryCursor{}, ErrInvalidEntryCursor
	}
	var cursor entryCursor
	if json.Unmarshal(payload, &cursor) != nil || cursor.Version != 1 || cursor.Offset < 0 || cursor.Fingerprint != fingerprint {
		return entryCursor{}, ErrInvalidEntryCursor
	}
	return cursor, nil
}

func (c *catalog) cleanup(at time.Time) error {
	if c == nil {
		return nil
	}
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return err
	}
	cutoff := at.Add(-catalogTTL)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(c.root, entry.Name()))
		}
	}
	return nil
}

func (c *catalog) path(previewID string) string {
	return filepath.Join(c.root, previewID+".jsonl")
}
