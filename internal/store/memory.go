package store

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
)

var ErrNotFound = errors.New("order not found")

type Memory struct {
	mu             sync.RWMutex
	orders         map[string]domain.Order
	events         []domain.Event
	nextSequence   uint64
	retention      int
	evictedThrough map[string]uint64
	subscribers    map[uint64]subscriber
	nextSubscriber uint64
}

type subscriber struct {
	tenantID string
	events   chan domain.Event
}

func NewMemory(retention int) *Memory {
	if retention < 1 {
		retention = 500
	}
	return &Memory{
		orders:         make(map[string]domain.Order),
		retention:      retention,
		evictedThrough: make(map[string]uint64),
		subscribers:    make(map[uint64]subscriber),
	}
}

func (m *Memory) Save(order domain.Order, eventType string) domain.Order {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextSequence++
	order.Sequence = m.nextSequence
	order.UpdatedAt = time.Now().UTC()
	if order.CreatedAt.IsZero() {
		order.CreatedAt = order.UpdatedAt
	}
	m.orders[order.ID] = order
	event := domain.Event{
		Sequence: m.nextSequence,
		Type:     eventType,
		OrderID:  order.ID,
		TenantID: order.TenantID,
		Status:   order.Status,
		At:       order.UpdatedAt,
	}
	m.events = append(m.events, event)
	if len(m.events) > m.retention {
		evicted := m.events[:len(m.events)-m.retention]
		for _, old := range evicted {
			m.evictedThrough[old.TenantID] = max(m.evictedThrough[old.TenantID], old.Sequence)
		}
		m.events = append([]domain.Event(nil), m.events[len(m.events)-m.retention:]...)
	}
	for id, subscriber := range m.subscribers {
		if subscriber.tenantID != "" && subscriber.tenantID != event.TenantID {
			continue
		}
		select {
		case subscriber.events <- event:
		default:
			delete(m.subscribers, id)
			close(subscriber.events)
		}
	}
	return order
}

func (m *Memory) Get(tenantID, orderID string) (domain.Order, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	order, ok := m.orders[orderID]
	if !ok || order.TenantID != tenantID {
		return domain.Order{}, ErrNotFound
	}
	return order, nil
}

func (m *Memory) EventsSince(tenantID string, sequence uint64) []domain.Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Event, 0)
	for _, event := range m.events {
		if event.TenantID == tenantID && event.Sequence > sequence {
			result = append(result, event)
		}
	}
	return result
}

func (m *Memory) RecentEvents(limit int) []domain.Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 || limit > len(m.events) {
		limit = len(m.events)
	}
	start := len(m.events) - limit
	result := append([]domain.Event(nil), m.events[start:]...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func (m *Memory) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.orders)
}

func (m *Memory) List(tenantID string) []domain.Order {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.Order, 0)
	for _, order := range m.orders {
		if order.TenantID == tenantID {
			result = append(result, order)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return result
}

func (m *Memory) Subscribe(buffer int) (<-chan domain.Event, func()) {
	_, events, cancel, _ := m.SubscribeSince("", ^uint64(0), buffer)
	return events, cancel
}

// SubscribeSince atomically captures retained events and registers a tenant-scoped
// live subscriber so events cannot be lost between replay and subscription.
func (m *Memory) SubscribeSince(tenantID string, sequence uint64, buffer int) ([]domain.Event, <-chan domain.Event, func(), bool) {
	if buffer < 1 {
		buffer = 32
	}
	m.mu.Lock()
	expired := sequence > 0 && sequence <= m.evictedThrough[tenantID]
	replay := make([]domain.Event, 0)
	for _, event := range m.events {
		if event.TenantID == tenantID && event.Sequence > sequence {
			replay = append(replay, event)
		}
	}
	m.nextSubscriber++
	id := m.nextSubscriber
	ch := make(chan domain.Event, buffer)
	m.subscribers[id] = subscriber{tenantID: tenantID, events: ch}
	m.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			m.mu.Lock()
			if current, ok := m.subscribers[id]; ok {
				delete(m.subscribers, id)
				close(current.events)
			}
			m.mu.Unlock()
		})
	}
	return replay, ch, cancel, expired
}

func (m *Memory) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, subscriber := range m.subscribers {
		delete(m.subscribers, id)
		close(subscriber.events)
	}
	m.orders = make(map[string]domain.Order)
	m.events = nil
	m.evictedThrough = make(map[string]uint64)
	m.nextSequence = 0
}
