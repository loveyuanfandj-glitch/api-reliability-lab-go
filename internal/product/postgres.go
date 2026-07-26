package product

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type PostgresRepository struct {
	pool *pgxpool.Pool
}

type rowScanner interface {
	Scan(dest ...any) error
}

func OpenPostgres(ctx context.Context, databaseURL string) (*PostgresRepository, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("configure postgres pool: %w", err)
	}
	repository := &PostgresRepository{pool: pool}
	if err := repository.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return repository, nil
}

func (r *PostgresRepository) Close() {
	r.pool.Close()
}

func (r *PostgresRepository) Ping(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}

func (r *PostgresRepository) Migrate(ctx context.Context) error {
	paths, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		migration, err := migrationFiles.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", path, err)
		}
		if _, err := r.pool.Exec(ctx, string(migration)); err != nil {
			return fmt.Errorf("apply migration %s: %w", path, err)
		}
	}
	return nil
}

func (r *PostgresRepository) CreateOrder(ctx context.Context, request CreateOrderRequest) (domain.Order, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Order{}, false, fmt.Errorf("begin order transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	existing, fingerprint, found, err := findOrder(ctx, tx, request.TenantID, request.IdempotencyKey)
	if err != nil {
		return domain.Order{}, false, err
	}
	if found {
		if fingerprint != request.RequestFingerprint {
			return domain.Order{}, false, ErrIdempotencyConflict
		}
		return existing, true, nil
	}

	now := time.Now().UTC()
	order := domain.Order{
		ID:        productID("ord"),
		TenantID:  request.TenantID,
		EventID:   request.EventID,
		Quantity:  request.Quantity,
		Status:    domain.OrderConfirmed,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO product_events (event_type, order_id, tenant_id, status, occurred_at)
		VALUES ('order.confirmed', $1, $2, $3, $4)
		RETURNING sequence`, order.ID, order.TenantID, order.Status, now).Scan(&order.Sequence); err != nil {
		return domain.Order{}, false, fmt.Errorf("insert order event: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO product_orders (
			id, tenant_id, idempotency_key, request_fingerprint, source, external_id,
			event_id, quantity, status, sequence, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		order.ID, order.TenantID, request.IdempotencyKey, request.RequestFingerprint,
		request.Source, request.ExternalID, order.EventID, order.Quantity, order.Status,
		order.Sequence, order.CreatedAt, order.UpdatedAt)
	if err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			return r.resolveConcurrentCreate(ctx, request)
		}
		return domain.Order{}, false, fmt.Errorf("insert order: %w", err)
	}

	if request.WebhookURL != "" {
		payload, err := json.Marshal(struct {
			ID      string       `json:"id"`
			Type    string       `json:"type"`
			Created time.Time    `json:"created_at"`
			Data    domain.Order `json:"data"`
		}{
			ID:      productID("evt"),
			Type:    "order.confirmed",
			Created: now,
			Data:    order,
		})
		if err != nil {
			return domain.Order{}, false, fmt.Errorf("encode webhook payload: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO product_webhook_deliveries (
				id, tenant_id, event_type, payload, target_url, status, attempts,
				max_attempts, next_attempt_at, created_at, updated_at
			) VALUES ($1, $2, 'order.confirmed', $3, $4, $5, 0, $6, $7, $7, $7)`,
			productID("wh"), order.TenantID, payload, request.WebhookURL,
			DeliveryPending, request.MaxWebhookAttempts, now)
		if err != nil {
			return domain.Order{}, false, fmt.Errorf("enqueue transactional webhook: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Order{}, false, fmt.Errorf("commit order transaction: %w", err)
	}
	return order, false, nil
}

func (r *PostgresRepository) resolveConcurrentCreate(ctx context.Context, request CreateOrderRequest) (domain.Order, bool, error) {
	order, fingerprint, found, err := findOrder(ctx, r.pool, request.TenantID, request.IdempotencyKey)
	if err != nil {
		return domain.Order{}, false, err
	}
	if !found {
		return domain.Order{}, false, fmt.Errorf("idempotency conflict committed without an order")
	}
	if fingerprint != request.RequestFingerprint {
		return domain.Order{}, false, ErrIdempotencyConflict
	}
	return order, true, nil
}

func (r *PostgresRepository) GetOrder(ctx context.Context, tenantID, orderID string) (domain.Order, error) {
	order, _, err := scanOrder(r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, event_id, quantity, status, created_at, updated_at, sequence,
		       request_fingerprint
		FROM product_orders WHERE tenant_id = $1 AND id = $2`, tenantID, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, ErrNotFound
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}
	return order, nil
}

func (r *PostgresRepository) ListOrders(ctx context.Context, tenantID string, limit int) ([]domain.Order, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, event_id, quantity, status, created_at, updated_at, sequence,
		       request_fingerprint
		FROM product_orders WHERE tenant_id = $1 ORDER BY sequence DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()
	orders := make([]domain.Order, 0)
	for rows.Next() {
		order, _, err := scanOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("scan order: %w", err)
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

func (r *PostgresRepository) ClaimDeliveries(ctx context.Context, limit int, lease time.Duration) ([]WebhookDelivery, error) {
	if limit < 1 || limit > 100 {
		limit = 10
	}
	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT id FROM product_webhook_deliveries
			WHERE (status = $1 AND next_attempt_at <= NOW())
			   OR (status = $2 AND updated_at <= NOW() - ($3 * INTERVAL '1 millisecond'))
			ORDER BY next_attempt_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $4
		)
		UPDATE product_webhook_deliveries AS delivery
		SET status = $2, attempts = delivery.attempts + 1, updated_at = NOW()
		FROM candidates
		WHERE delivery.id = candidates.id
		RETURNING delivery.id, delivery.tenant_id, delivery.event_type, delivery.payload,
		          delivery.target_url, delivery.status, delivery.attempts, delivery.max_attempts,
		          delivery.next_attempt_at, delivery.last_error, delivery.created_at, delivery.updated_at`,
		DeliveryPending, DeliveryProcessing, lease.Milliseconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("claim webhook deliveries: %w", err)
	}
	defer rows.Close()
	deliveries := make([]WebhookDelivery, 0)
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("scan claimed delivery: %w", err)
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (r *PostgresRepository) MarkDeliverySucceeded(ctx context.Context, id string) error {
	command, err := r.pool.Exec(ctx, `
		UPDATE product_webhook_deliveries
		SET status = $1, last_error = '', updated_at = NOW()
		WHERE id = $2 AND status = $3`, DeliveryDelivered, id, DeliveryProcessing)
	if err != nil {
		return fmt.Errorf("complete webhook delivery: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) MarkDeliveryFailed(ctx context.Context, delivery WebhookDelivery, message string, delay time.Duration) (DeliveryStatus, error) {
	status := DeliveryPending
	if delivery.Attempts >= delivery.MaxAttempts {
		status = DeliveryDeadLetter
	}
	command, err := r.pool.Exec(ctx, `
		UPDATE product_webhook_deliveries
		SET status = $1, next_attempt_at = NOW() + ($2 * INTERVAL '1 millisecond'),
		    last_error = $3, updated_at = NOW()
		WHERE id = $4 AND status = $5`, status, delay.Milliseconds(), message, delivery.ID, DeliveryProcessing)
	if err != nil {
		return "", fmt.Errorf("fail webhook delivery: %w", err)
	}
	if command.RowsAffected() != 1 {
		return "", ErrNotFound
	}
	return status, nil
}

func (r *PostgresRepository) ListDeliveries(ctx context.Context, tenantID string, status DeliveryStatus, limit int) ([]WebhookDelivery, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, event_type, payload, target_url, status, attempts, max_attempts,
		       next_attempt_at, last_error, created_at, updated_at
		FROM product_webhook_deliveries
		WHERE tenant_id = $1 AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC LIMIT $3`, tenantID, status, limit)
	if err != nil {
		return nil, fmt.Errorf("list webhook deliveries: %w", err)
	}
	defer rows.Close()
	deliveries := make([]WebhookDelivery, 0)
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("scan webhook delivery: %w", err)
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}

func (r *PostgresRepository) ReplayDelivery(ctx context.Context, tenantID, id string) error {
	command, err := r.pool.Exec(ctx, `
		UPDATE product_webhook_deliveries
		SET status = $1, attempts = 0, next_attempt_at = NOW(), last_error = '', updated_at = NOW()
		WHERE tenant_id = $2 AND id = $3 AND status = $4`,
		DeliveryPending, tenantID, id, DeliveryDeadLetter)
	if err != nil {
		return fmt.Errorf("replay dead-letter delivery: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func findOrder(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenantID, idempotencyKey string) (domain.Order, string, bool, error) {
	order, fingerprint, err := scanOrder(querier.QueryRow(ctx, `
		SELECT id, tenant_id, event_id, quantity, status, created_at, updated_at, sequence,
		       request_fingerprint
		FROM product_orders WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, "", false, nil
	}
	if err != nil {
		return domain.Order{}, "", false, fmt.Errorf("find idempotent order: %w", err)
	}
	return order, fingerprint, true, nil
}

func scanOrder(row rowScanner) (domain.Order, string, error) {
	var order domain.Order
	var fingerprint string
	err := row.Scan(&order.ID, &order.TenantID, &order.EventID, &order.Quantity, &order.Status,
		&order.CreatedAt, &order.UpdatedAt, &order.Sequence, &fingerprint)
	return order, fingerprint, err
}

func scanDelivery(row rowScanner) (WebhookDelivery, error) {
	var delivery WebhookDelivery
	err := row.Scan(&delivery.ID, &delivery.TenantID, &delivery.EventType, &delivery.Payload,
		&delivery.TargetURL, &delivery.Status, &delivery.Attempts, &delivery.MaxAttempts,
		&delivery.NextAttemptAt, &delivery.LastError, &delivery.CreatedAt, &delivery.UpdatedAt)
	return delivery, err
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}

func productID(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(value[:])
}
