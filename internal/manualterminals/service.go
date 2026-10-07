package manualterminals

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/terminals"
)

type CreateRequest struct {
	SessionID       string
	Title           string
	CWD             string
	MinimumTabOrder int
}

type Service struct {
	mu    sync.Mutex
	store Store
	now   func() time.Time
	id    func(string) (string, error)
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("manual terminal store is required")
	}
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }, id: randomID}, nil
}

func (service *Service) List(ctx context.Context, sessionID string) ([]Terminal, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	result, err := service.store.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(left, right int) bool { return tabLess(result[left], result[right]) })
	return result, nil
}

func (service *Service) ListAll(ctx context.Context, sessionID string) ([]Terminal, error) {
	return service.List(ctx, sessionID)
}

func (service *Service) Get(ctx context.Context, sessionID, recordID string) (Terminal, bool, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.getLocked(ctx, sessionID, recordID)
}

func (service *Service) Create(ctx context.Context, request CreateRequest) (Terminal, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if strings.TrimSpace(request.SessionID) == "" || strings.TrimSpace(request.CWD) == "" {
		return Terminal{}, errors.New("manual terminal session and working directory are required")
	}
	all, err := service.store.ListBySession(ctx, request.SessionID)
	if err != nil {
		return Terminal{}, err
	}
	if len(all) >= MaximumPerSession {
		return Terminal{}, ErrLimit
	}
	maximumOrder := request.MinimumTabOrder
	for _, current := range all {
		maximumOrder = max(maximumOrder, current.TabOrder)
	}
	recordID, err := service.id("manual")
	if err != nil {
		return Terminal{}, err
	}
	terminalID, err := terminals.NewID()
	if err != nil {
		return Terminal{}, err
	}
	now := service.now()
	record := Record{
		ID: recordID, TerminalID: terminalID, SessionID: request.SessionID,
		Title: normalizeTitle(request.Title, defaultTitle(len(all)+1)), CWD: request.CWD,
		TabOrder: maximumOrder + 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := service.store.Insert(ctx, record); err != nil {
		return Terminal{}, err
	}
	return record, nil
}

func (service *Service) UpdateTitle(ctx context.Context, sessionID, recordID, title string) (Terminal, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	record, ok, err := service.getLocked(ctx, sessionID, recordID)
	if err != nil {
		return Terminal{}, err
	}
	if !ok {
		return Terminal{}, ErrMissing
	}
	record.Title = normalizeTitle(title, record.Title)
	record.UpdatedAt = service.now()
	if err := service.store.Update(ctx, record); err != nil {
		return Terminal{}, err
	}
	return record, nil
}

func (service *Service) Delete(ctx context.Context, sessionID, recordID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	_, ok, err := service.getLocked(ctx, sessionID, recordID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return service.store.Delete(ctx, recordID)
}

func (service *Service) HasVisible(ctx context.Context, sessionID string) (bool, error) {
	all, err := service.List(ctx, sessionID)
	return len(all) > 0, err
}

func (service *Service) ActiveTerminalIDs(ctx context.Context, sessionID string) ([]terminals.TerminalID, error) {
	all, err := service.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result := make([]terminals.TerminalID, 0, len(all))
	for _, record := range all {
		if record.TerminalID.Valid() {
			result = append(result, record.TerminalID)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result, nil
}

func (service *Service) ApplyTabOrders(ctx context.Context, sessionID string, orders map[string]int, persist bool) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	all, err := service.store.ListBySession(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, terminal := range all {
		order, exists := orders[terminal.ID]
		if !exists || terminal.TabOrder == order {
			continue
		}
		terminal.TabOrder = order
		terminal.UpdatedAt = service.now()
		if persist {
			if err := service.store.Update(ctx, terminal); err != nil {
				return err
			}
		}
	}
	return nil
}

func (service *Service) MaximumTabOrder(ctx context.Context, sessionID string) (int, error) {
	all, err := service.List(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	maximum := 0
	for _, record := range all {
		maximum = max(maximum, record.TabOrder)
	}
	return maximum, nil
}

func (service *Service) getLocked(ctx context.Context, sessionID, recordID string) (Record, bool, error) {
	all, err := service.store.ListBySession(ctx, sessionID)
	if err != nil {
		return Record{}, false, err
	}
	for _, terminal := range all {
		if terminal.ID == recordID {
			return terminal, true, nil
		}
	}
	return Record{}, false, nil
}

func tabLess(left, right Terminal) bool {
	if left.TabOrder > 0 && right.TabOrder > 0 && left.TabOrder != right.TabOrder {
		return left.TabOrder < right.TabOrder
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID < right.ID
}

func defaultTitle(index int) string {
	if index <= 0 {
		index = 1
	}
	return "Shell " + strconv.Itoa(index)
}

func normalizeTitle(title, fallback string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = fallback
	}
	if len(title) > 80 {
		title = title[:80]
	}
	return title
}

func randomID(prefix string) (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(data), nil
}
