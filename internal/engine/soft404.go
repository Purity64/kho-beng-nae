package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Baseline captures how a target responds to paths that certainly do NOT
// exist. Many sites answer 200 with a catch-all page (SPA, custom 404) for
// every path; without this fingerprint every word in the list looks like a
// hit. We probe a few random paths up front and record the shape of the
// "not found" response so real hits can be told apart.
type Baseline struct {
	// Wildcard is true when random paths return a "found" status — the target
	// answers positively for anything, so status alone is meaningless.
	Wildcard bool
	Status   int
	// Size band of the catch-all response. A candidate whose status matches
	// and whose size falls within [MinSize, MaxSize] is treated as not-found.
	MinSize int64
	MaxSize int64
}

// randomPath returns an unguessable path segment used only for baselining.
func randomPath() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "kbn-" + hex.EncodeToString(b)
}

// Learn probes n random paths under base and builds a Baseline. base must end
// without a trailing slash; it is joined with a single "/". Transport errors
// during learning are ignored (best effort) unless every probe fails.
func Learn(ctx context.Context, doer Doer, base string, policy FoundPolicy, n int) (Baseline, error) {
	if n < 1 {
		n = 3
	}
	base = strings.TrimRight(base, "/")

	var (
		lastStatus int
		minSize    int64 = -1
		maxSize    int64
		hits       int
		ok         int
		lastErr    error
	)

	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return Baseline{}, ctx.Err()
		default:
		}

		resp, err := doer.Do(ctx, base+"/"+randomPath())
		if err != nil {
			lastErr = err
			continue
		}
		ok++
		lastStatus = resp.StatusCode
		if policy.IsFound(resp.StatusCode) {
			hits++
		}
		if minSize < 0 || resp.Size < minSize {
			minSize = resp.Size
		}
		if resp.Size > maxSize {
			maxSize = resp.Size
		}
	}

	if ok == 0 {
		return Baseline{}, lastErr
	}

	// A target is "wildcard" only if a strong majority of random paths were
	// reported as found — one flaky hit should not disable real scanning.
	wildcard := hits*2 > ok

	// Pad the size band by ~5% so minor dynamic differences (timestamps,
	// CSRF tokens) don't cause a match to be missed.
	pad := (maxSize - minSize) / 20
	if pad < 16 {
		pad = 16
	}

	return Baseline{
		Wildcard: wildcard,
		Status:   lastStatus,
		MinSize:  minSize - pad,
		MaxSize:  maxSize + pad,
	}, nil
}

// IsSoft404 reports whether a real probe result looks like the catch-all page
// rather than a genuine hit. Only meaningful when the baseline is a wildcard.
func (b Baseline) IsSoft404(resp *Response) bool {
	if !b.Wildcard || resp == nil {
		return false
	}
	if resp.StatusCode != b.Status {
		return false
	}
	return resp.Size >= b.MinSize && resp.Size <= b.MaxSize
}
