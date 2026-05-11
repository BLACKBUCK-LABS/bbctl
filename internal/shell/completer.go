package shell

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	completionTimeout = 2 * time.Second
	cacheTTL          = 30 * time.Second
)

// Completer is implemented by the HTTP client.
type Completer interface {
	Complete(ctx context.Context, req CompleteRequest) ([]string, error)
}

// CompleteRequest mirrors client.CompleteRequest to avoid a circular import.
type CompleteRequest struct {
	InstanceID string
	AccountID  string
	Partial    string
	CurrentDir string
	PrivateIP  string
}

type cacheEntry struct {
	completions []string
	at          time.Time
}

// RemoteCompleter fetches tab completions from the backend.
type RemoteCompleter struct {
	instanceID string
	accountID  string
	privateIP  string
	client     Completer
	currentDir *string // pointer so updates in the shell loop are visible here

	mu    sync.Mutex
	cache map[string]cacheEntry
}

func NewRemoteCompleter(instanceID, accountID, privateIP string, client Completer, currentDir *string) *RemoteCompleter {
	return &RemoteCompleter{
		instanceID: instanceID,
		accountID:  accountID,
		privateIP:  privateIP,
		client:     client,
		currentDir: currentDir,
		cache:      make(map[string]cacheEntry),
	}
}

// Do implements readline.AutoCompleter.
func (rc *RemoteCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	lineStr := string(line[:pos])
	lastSpace := strings.LastIndex(lineStr, " ")
	if lastSpace < 0 {
		return nil, 0
	}

	partial := lineStr[lastSpace+1:]
	if partial != "" && !looksLikeCompletablePath(partial) {
		return nil, 0
	}

	cacheKey := partial + "|" + *rc.currentDir

	rc.mu.Lock()
	if entry, ok := rc.cache[cacheKey]; ok && time.Since(entry.at) < cacheTTL {
		completions := entry.completions
		rc.mu.Unlock()
		return toRuneSlices(completions, partial), menuLength(partial)
	}
	rc.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()

	results, err := rc.client.Complete(ctx, CompleteRequest{
		InstanceID: rc.instanceID,
		AccountID:  rc.accountID,
		Partial:    partial,
		CurrentDir: *rc.currentDir,
		PrivateIP:  rc.privateIP,
	})
	if err != nil || len(results) == 0 {
		return nil, 0
	}

	rc.mu.Lock()
	rc.cache[cacheKey] = cacheEntry{completions: results, at: time.Now()}
	rc.mu.Unlock()

	return toRuneSlices(results, partial), menuLength(partial)
}

// menuLength returns how many runes readline should show as the "already typed"
// prefix in the completion menu. When partial ends with "/" the user has fully
// typed a directory — show only filenames (length=0). Otherwise show the
// partial token so the menu renders e.g. "/var/lo" + "g".
func menuLength(partial string) int {
	if strings.HasSuffix(partial, "/") {
		return 0
	}
	return utf8.RuneCountInString(partial)
}

func looksLikeCompletablePath(s string) bool {
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "~")
}

func toRuneSlices(completions []string, partial string) [][]rune {
	result := make([][]rune, 0, len(completions))
	for _, c := range completions {
		if strings.HasPrefix(c, partial) {
			result = append(result, []rune(c[len(partial):]))
		} else {
			result = append(result, []rune(c))
		}
	}
	return result
}
