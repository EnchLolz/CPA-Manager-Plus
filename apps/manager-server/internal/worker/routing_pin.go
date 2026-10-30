package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
)

// RoutingPin is a manual "use this credential first" override. The priority
// worker keeps ordering every other credential; only the pinned one moves.
type RoutingPin struct {
	AuthIndex  string `json:"auth_index"`
	Name       string `json:"name"`
	PinnedAtMS int64  `json:"pinned_at_ms"`
	// UntilMS ends the pin automatically. Zero keeps it until it is cleared.
	UntilMS int64 `json:"until_ms,omitempty"`
}

var (
	ErrRoutingUnavailable = errors.New("priority routing is off for this provider")
	ErrRoutingPinTarget   = errors.New("credential is not an enabled account of this provider")
)

// Each worker owns its pin. Requests are handed to the worker goroutine so pin
// state, quota caches, and CPA writes are never touched concurrently.
type routingPinRequest struct {
	authIndex string
	reply     chan error
}

var routingPinners struct {
	mu       sync.RWMutex
	channels map[string]chan routingPinRequest
}

func registerRoutingPinner(provider string) chan routingPinRequest {
	routingPinners.mu.Lock()
	defer routingPinners.mu.Unlock()
	if routingPinners.channels == nil {
		routingPinners.channels = map[string]chan routingPinRequest{}
	}
	ch := make(chan routingPinRequest)
	routingPinners.channels[provider] = ch
	return ch
}

// PinRouting puts authIndex first for provider, or clears the pin when authIndex
// is empty. It returns once the worker has written the new order to CPA.
func PinRouting(ctx context.Context, provider, authIndex string) error {
	routingPinners.mu.RLock()
	ch := routingPinners.channels[strings.ToLower(provider)]
	routingPinners.mu.RUnlock()
	if ch == nil {
		return ErrRoutingUnavailable
	}
	req := routingPinRequest{authIndex: authIndex, reply: make(chan error, 1)}
	select {
	case ch <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// serveRoutingPins answers pin requests until the next poll is due. It reports
// false once the worker is shutting down. A nil pins channel never fires.
func serveRoutingPins(ctx context.Context, tick <-chan time.Time, pins chan routingPinRequest, setPin func(context.Context, string) error) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-tick:
			return true
		case req := <-pins:
			req.reply <- setPin(ctx, req.authIndex)
		}
	}
}

func routingPinPath(dataDir, provider string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, provider+"-routing-pin.json")
}

func loadRoutingPin(path string) *RoutingPin {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	var pin RoutingPin
	if err != nil || json.Unmarshal(raw, &pin) != nil || pin.AuthIndex == "" {
		log.Print("[routing-pin] could not load pin state")
		return nil
	}
	return &pin
}

func saveRoutingPin(path string, pin *RoutingPin) {
	if path == "" {
		return
	}
	var err error
	if pin == nil {
		if err = os.Remove(path); os.IsNotExist(err) {
			err = nil
		}
	} else {
		raw, _ := json.Marshal(pin)
		if err = os.WriteFile(path+".tmp", raw, 0600); err == nil {
			err = os.Rename(path+".tmp", path)
		}
	}
	if err != nil {
		log.Print("[routing-pin] could not save pin state")
	}
}

func pinnedAuthIndex(pin *RoutingPin) string {
	if pin == nil {
		return ""
	}
	return pin.AuthIndex
}

func copyRoutingPin(pin *RoutingPin) *RoutingPin {
	if pin == nil {
		return nil
	}
	value := *pin
	return &value
}

func enabledCredential(files []cpaauthfiles.File, provider, authIndex string) (cpaauthfiles.File, bool) {
	for _, file := range files {
		if strings.EqualFold(file.Provider, provider) && !file.Disabled && file.AuthIndex != "" && file.AuthIndex == authIndex {
			return file, true
		}
	}
	return cpaauthfiles.File{}, false
}

// pinFirst moves the pinned item to the front and keeps everything else in order.
func pinFirst[T any](items []T, pinned func(T) bool) []T {
	for i, item := range items {
		if pinned(item) {
			out := append([]T{item}, items[:i]...)
			return append(out, items[i+1:]...)
		}
	}
	return items
}
