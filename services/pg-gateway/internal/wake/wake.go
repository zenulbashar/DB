// Package wake triggers scale-to-zero wake-on-connect: when a client hits a
// suspended endpoint the gateway asks the control plane to resume the branch
// (ADR-014). The trigger is a single authenticated POST to the control-plane's
// privileged internal endpoint; it is COALESCED per branch so a connection
// storm produces one wake, not one per connection (SECURITY_MODEL §2).
package wake

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

// Waker asks the control plane to wake (resume) a suspended branch. Wake
// returns nil once the wake has been REQUESTED (the branch is flipped to
// resuming) — it does not wait for the compute to finish resuming; the caller
// polls the route table for readiness.
type Waker interface {
	Wake(ctx context.Context, branchID string) error
}

// HTTPWaker calls POST {baseURL}/internal/branches/{branchID}/wake with a
// bearer token, coalescing concurrent wakes for the same branch.
type HTTPWaker struct {
	baseURL string
	token   string
	client  *http.Client
	// timeout bounds the POST itself, INDEPENDENTLY of any caller's context:
	// singleflight shares the first caller's result with all coalesced callers,
	// so if the POST rode the first caller's context a client that gave up would
	// cancel the wake for everyone waiting. The request below uses a fresh
	// background context with this timeout instead.
	timeout time.Duration
	group   singleflight.Group
}

// NewHTTP builds an HTTPWaker. baseURL is the control-plane API root (e.g.
// http://control-plane.nimbusdb-system.svc:8080); token is NDB_GATEWAY_TOKEN.
func NewHTTP(baseURL, token string, timeout time.Duration) *HTTPWaker {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPWaker{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client:  &http.Client{Timeout: timeout},
		timeout: timeout,
	}
}

func (h *HTTPWaker) Wake(_ context.Context, branchID string) error {
	// Coalesce per branch. The shared call uses a background context (see the
	// timeout field doc) so one caller's cancellation cannot abort the wake for
	// the others sharing the result.
	_, err, _ := h.group.Do(branchID, func() (any, error) {
		return nil, h.post(branchID)
	})
	return err
}

func (h *HTTPWaker) post(branchID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	endpoint := h.baseURL + "/internal/branches/" + url.PathEscape(branchID) + "/wake"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("wake request: %w", err)
	}
	defer resp.Body.Close()
	// 2xx = resuming (or an idempotent no-op on an already-resuming/ready
	// branch). Anything else is a real failure worth surfacing.
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("wake: control plane returned %d", resp.StatusCode)
	}
	return nil
}
