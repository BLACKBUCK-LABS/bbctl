package shell

import (
	"context"
	"fmt"
	"os"
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
}

type cacheEntry struct {
	completions []string
	at          time.Time
}

// RemoteCompleter fetches tab completions from the backend.
type RemoteCompleter struct {
	instanceID string
	accountID  string
	client     Completer
	currentDir *string // pointer so updates in the shell loop are visible here

	mu    sync.Mutex
	cache map[string]cacheEntry
}

func NewRemoteCompleter(instanceID, accountID string, client Completer, currentDir *string) *RemoteCompleter {
	return &RemoteCompleter{
		instanceID: instanceID,
		accountID:  accountID,
		client:     client,
		currentDir: currentDir,
		cache:      make(map[string]cacheEntry),
	}
}

// Do implements readline.AutoCompleter.
func (rc *RemoteCompleter) Do(line []rune, pos int) (newLine [][]rune, length int) {
	fmt.Fprintf(os.Stderr, "DEBUG completer called: line=%q pos=%d\n", string(line[:pos]), pos)
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
		return toRuneSlices(completions, partial), utf8.RuneCountInString(partial)
	}
	rc.mu.Unlock()

	fmt.Fprintf(os.Stderr, "DEBUG calling backend: instanceID=%s accountID=%s partial=%q currentDir=%q\n",
		rc.instanceID, rc.accountID, partial, *rc.currentDir)

	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()

	results, err := rc.client.Complete(ctx, CompleteRequest{
		InstanceID: rc.instanceID,
		AccountID:  rc.accountID,
		Partial:    partial,
		CurrentDir: *rc.currentDir,
	})
	fmt.Fprintf(os.Stderr, "DEBUG complete results: err=%v results=%v\n", err, results)
	if err != nil || len(results) == 0 {
		return nil, 0
	}

	rc.mu.Lock()
	rc.cache[cacheKey] = cacheEntry{completions: results, at: time.Now()}
	rc.mu.Unlock()

	slices := toRuneSlices(results, partial)
	fmt.Fprintf(os.Stderr, "DEBUG rune slices: len=%d first=%q\n",
		len(slices), func() string {
			if len(slices) > 0 {
				return string(slices[0])
			}
			return ""
		}())

	return slices, utf8.RuneCountInString(partial)
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
