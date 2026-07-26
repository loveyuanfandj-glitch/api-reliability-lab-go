package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/app"
	"github.com/loveyuanfandj-glitch/api-reliability-lab-go/internal/domain"
)

type ScenarioResult struct {
	Name       string         `json:"name"`
	Passed     bool           `json:"passed"`
	DurationMS int64          `json:"duration_ms"`
	Evidence   map[string]any `json:"evidence"`
	Error      string         `json:"error,omitempty"`
}

type Report struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Target      string           `json:"target"`
	Passed      bool             `json:"passed"`
	Scenarios   []ScenarioResult `json:"scenarios"`
}

type client struct {
	baseURL string
	http    *http.Client
}

func main() {
	baseURL := flag.String("base-url", "http://localhost:8080", "Northstar API base URL")
	output := flag.String("output", "", "optional JSON report path")
	flag.Parse()

	runner := &client{baseURL: strings.TrimRight(*baseURL, "/"), http: &http.Client{Timeout: 4 * time.Second}}
	if err := runner.postJSON(context.Background(), "/v1/demo/reset", nil, nil, nil); err != nil {
		fmt.Fprintf(os.Stderr, "reset demo: %v\n", err)
		os.Exit(1)
	}
	report := Report{GeneratedAt: time.Now().UTC(), Target: runner.baseURL, Passed: true}
	for _, scenario := range []struct {
		name string
		run  func(context.Context) (map[string]any, error)
	}{
		{name: "concurrent-idempotency", run: runner.idempotencyScenario},
		{name: "dependency-recovery", run: runner.recoveryScenario},
		{name: "websocket-gap-replay", run: runner.websocketScenario},
	} {
		started := time.Now()
		evidence, err := scenario.run(context.Background())
		result := ScenarioResult{Name: scenario.name, Passed: err == nil, DurationMS: time.Since(started).Milliseconds(), Evidence: evidence}
		if err != nil {
			result.Error = err.Error()
			report.Passed = false
		}
		report.Scenarios = append(report.Scenarios, result)
		state := "PASS"
		if err != nil {
			state = "FAIL"
		}
		fmt.Printf("%-28s %s (%d ms)\n", scenario.name, state, result.DurationMS)
	}

	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		panic(err)
	}
	if *output != "" {
		if err := os.WriteFile(*output, append(payload, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("report: %s\n", *output)
	} else {
		fmt.Println(string(payload))
	}
	if !report.Passed {
		os.Exit(1)
	}
}

func (c *client) idempotencyScenario(ctx context.Context) (map[string]any, error) {
	if err := c.setMode(ctx, "healthy"); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("replay-idempotency-%d", time.Now().UnixNano())
	const requestCount = 12
	var wg sync.WaitGroup
	results := make(chan app.CreateResult, requestCount)
	errs := make(chan error, requestCount)
	for range requestCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, _, err := c.createOrder(ctx, key)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			return nil, err
		}
	}
	ids := make(map[string]struct{})
	replayed := 0
	for result := range results {
		ids[result.Order.ID] = struct{}{}
		if result.Replayed {
			replayed++
		}
	}
	evidence := map[string]any{"requests": requestCount, "unique_order_ids": len(ids), "replayed_responses": replayed}
	if len(ids) != 1 || replayed != requestCount-1 {
		return evidence, errors.New("duplicate requests did not converge on one order")
	}
	return evidence, nil
}

func (c *client) recoveryScenario(ctx context.Context) (map[string]any, error) {
	if err := c.setMode(ctx, "unavailable"); err != nil {
		return nil, err
	}
	statuses := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		_, status, _ := c.createOrder(ctx, fmt.Sprintf("replay-outage-%d-%d", time.Now().UnixNano(), i))
		statuses = append(statuses, status)
	}
	var before struct {
		Dependency struct {
			CircuitState string `json:"circuit_state"`
		} `json:"dependency"`
	}
	if err := c.getJSON(ctx, "/v1/demo/snapshot", nil, &before); err != nil {
		return nil, err
	}
	if err := c.setMode(ctx, "healthy"); err != nil {
		return nil, err
	}
	time.Sleep(4200 * time.Millisecond)
	_, recoveryStatus, err := c.createOrder(ctx, fmt.Sprintf("replay-recovery-%d", time.Now().UnixNano()))
	evidence := map[string]any{"outage_statuses": statuses, "circuit_before_recovery": before.Dependency.CircuitState, "recovery_status": recoveryStatus}
	if err != nil || before.Dependency.CircuitState != "open" || (recoveryStatus != http.StatusCreated && recoveryStatus != http.StatusOK) {
		return evidence, fmt.Errorf("recovery invariant failed: %w", err)
	}
	return evidence, nil
}

func (c *client) websocketScenario(ctx context.Context) (map[string]any, error) {
	first, _, err := c.createOrder(ctx, fmt.Sprintf("replay-ws-first-%d", time.Now().UnixNano()))
	if err != nil {
		return nil, err
	}
	second, _, err := c.createOrder(ctx, fmt.Sprintf("replay-ws-second-%d", time.Now().UnixNano()))
	if err != nil {
		return nil, err
	}
	wsURL := "ws" + strings.TrimPrefix(c.baseURL, "http") + fmt.Sprintf("/v1/stream?since=%d", first.Order.Sequence)
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"X-API-Key": []string{"tenant-alpha-key"}}})
	if err != nil {
		return nil, err
	}
	defer connection.CloseNow()
	var event domain.Event
	if err := wsjson.Read(dialCtx, connection, &event); err != nil {
		return nil, err
	}
	evidence := map[string]any{"disconnected_after_sequence": first.Order.Sequence, "expected_sequence": second.Order.Sequence, "replayed_sequence": event.Sequence, "replayed_order_id": event.OrderID}
	if event.Sequence != second.Order.Sequence || event.OrderID != second.Order.ID {
		return evidence, errors.New("WebSocket replay returned an unexpected event")
	}
	return evidence, nil
}

func (c *client) createOrder(ctx context.Context, idempotencyKey string) (app.CreateResult, int, error) {
	var result app.CreateResult
	headers := http.Header{"X-API-Key": []string{"tenant-alpha-key"}, "Idempotency-Key": []string{idempotencyKey}}
	status, err := c.requestJSON(ctx, http.MethodPost, "/v1/orders", map[string]any{"event_id": "event-replay-suite", "quantity": 2}, headers, &result)
	if err != nil {
		return result, status, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return result, status, fmt.Errorf("create order returned HTTP %d", status)
	}
	return result, status, nil
}

func (c *client) setMode(ctx context.Context, mode string) error {
	return c.postJSON(ctx, "/v1/demo/fault", map[string]string{"mode": mode}, nil, nil)
}

func (c *client) getJSON(ctx context.Context, path string, headers http.Header, output any) error {
	status, err := c.requestJSON(ctx, http.MethodGet, path, nil, headers, output)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("GET %s returned HTTP %d", path, status)
	}
	return nil
}

func (c *client) postJSON(ctx context.Context, path string, input any, headers http.Header, output any) error {
	status, err := c.requestJSON(ctx, http.MethodPost, path, input, headers, output)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("POST %s returned HTTP %d", path, status)
	}
	return nil
}

func (c *client) requestJSON(ctx context.Context, method, path string, input any, headers http.Header, output any) (int, error) {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if output != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			return response.StatusCode, err
		}
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	return response.StatusCode, nil
}
