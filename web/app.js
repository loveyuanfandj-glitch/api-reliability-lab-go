(function () {
  "use strict";

  var POLL_INTERVAL_MS = 2000;
  var HISTORY_LENGTH = 40;
  var chartHistory = { p50: [], p95: [], p99: [] };
  var lastSnapshot = null;
  var pollTimer = null;
  var eventCache = [];

  var elements = {
    connectionPill: document.getElementById("connectionPill"),
    connectionText: document.getElementById("connectionText"),
    systemState: document.getElementById("systemState"),
    systemStateText: document.getElementById("systemStateText"),
    systemStateDetail: document.getElementById("systemStateDetail"),
    circuitState: document.getElementById("circuitState"),
    circuitDetail: document.getElementById("circuitDetail"),
    circuitTrack: document.getElementById("circuitTrack"),
    circuitIndicator: document.getElementById("circuitIndicator"),
    throughputValue: document.getElementById("throughputValue"),
    orderCount: document.getElementById("orderCount"),
    duplicateValue: document.getElementById("duplicateValue"),
    errorValue: document.getElementById("errorValue"),
    errorDetail: document.getElementById("errorDetail"),
    p50Value: document.getElementById("p50Value"),
    p95Value: document.getElementById("p95Value"),
    p99Value: document.getElementById("p99Value"),
    chartMax: document.getElementById("chartMax"),
    chartEvent: document.getElementById("chartEvent"),
    canvas: document.getElementById("latencyChart"),
    eventTimeline: document.getElementById("eventTimeline"),
    tenantSelect: document.getElementById("tenantSelect"),
    createOrderButton: document.getElementById("createOrderButton"),
    duplicateButton: document.getElementById("duplicateButton"),
    resetButton: document.getElementById("resetButton"),
    dependencyNode: document.getElementById("dependencyNode"),
    dependencyModeLabel: document.getElementById("dependencyModeLabel"),
    toastRegion: document.getElementById("toastRegion")
  };

  function getPath(source, path) {
    return path.split(".").reduce(function (value, key) {
      return value !== undefined && value !== null ? value[key] : undefined;
    }, source);
  }

  function firstValue(source, paths, fallback) {
    for (var index = 0; index < paths.length; index += 1) {
      var value = getPath(source, paths[index]);
      if (value !== undefined && value !== null) return value;
    }
    return fallback;
  }

  function asNumber(value, fallback) {
    var number = Number(value);
    return Number.isFinite(number) ? number : fallback;
  }

  function normalizeMode(value) {
    var mode = String(value || "healthy").toLowerCase();
    if (["healthy", "slow", "flaky", "unavailable"].indexOf(mode) === -1) return "healthy";
    return mode;
  }

  function normalizeCircuit(value) {
    var state = String(value || "closed").toLowerCase().replace(/_/g, "-");
    if (state === "halfopen") state = "half-open";
    return state;
  }

  function normalizeEvent(event, index) {
    var rawType = String(firstValue(event, ["type", "kind", "name", "event"], "service_event"));
    var rawSeverity = String(firstValue(event, ["severity", "level", "status"], "info")).toLowerCase();
    var severity = "info";
    if (/error|failed|open|unavailable|reject/.test(rawSeverity + " " + rawType)) severity = "error";
    else if (/warn|slow|retry|flaky|half/.test(rawSeverity + " " + rawType)) severity = "warning";
    else if (/success|created|confirmed|closed|recover|duplicate|healthy/.test(rawSeverity + " " + rawType)) severity = "success";

    return {
      id: String(firstValue(event, ["id", "sequence", "seq"], rawType + "-" + index)),
      type: rawType,
      title: String(firstValue(event, ["title", "message", "summary"], humanize(rawType))),
      detail: String(firstValue(event, ["detail", "description"], event.order_id
        ? (event.tenant_id ? event.tenant_id + " · " : "") + "Order " + event.order_id + (event.sequence ? " · seq " + event.sequence : "")
        : "Synthetic reliability signal")),
      severity: severity,
      timestamp: firstValue(event, ["timestamp", "time", "created_at", "occurred_at", "at"], new Date().toISOString())
    };
  }

  function humanize(value) {
    return String(value || "event")
      .replace(/[_-]+/g, " ")
      .replace(/\b\w/g, function (letter) { return letter.toUpperCase(); });
  }

  function normalizeSnapshot(snapshot) {
    var mode = normalizeMode(firstValue(snapshot, [
      "dependency_mode", "dependency.mode", "inventory.mode", "fault_mode", "mode"
    ], "healthy"));
    var circuit = normalizeCircuit(firstValue(snapshot, [
      "circuit_state", "dependency.circuit_state", "circuit.state", "inventory.circuit_state", "breaker.state"
    ], "closed"));
    var rawSeries = firstValue(snapshot, ["timeseries", "time_series", "metrics.timeseries"], []);
    if (!Array.isArray(rawSeries)) rawSeries = [];
    var series = rawSeries.map(function (point) {
      return {
        timestamp: firstValue(point, ["timestamp", "at", "time"], new Date().toISOString()),
        requests: asNumber(firstValue(point, ["requests", "request_count"], 0), 0),
        errors: asNumber(firstValue(point, ["errors", "error_count"], 0), 0),
        latency: asNumber(firstValue(point, ["latency_ms", "latency", "duration_ms"], 0), 0)
      };
    });
    var requests = asNumber(firstValue(snapshot, [
      "requests_total", "metrics.requests_total", "traffic.requests_total", "total_requests", "metrics.orders_total"
    ], series.reduce(function (total, point) { return total + point.requests; }, 0)), 0);
    var errors = asNumber(firstValue(snapshot, [
      "errors_total", "metrics.errors_total", "traffic.errors_total", "failed_requests", "metrics.orders_failed"
    ], series.reduce(function (total, point) { return total + point.errors; }, 0)), 0);
    var explicitErrorRate = firstValue(snapshot, ["error_rate", "metrics.error_rate", "traffic.error_rate"], undefined);
    var errorRate = explicitErrorRate === undefined ? (requests ? errors / requests * 100 : 0) : asNumber(explicitErrorRate, 0);
    if (errorRate > 0 && errorRate <= 1) errorRate *= 100;

    var latencySource = firstValue(snapshot, ["latency_ms", "metrics.latency_ms", "latency", "metrics.latency"], {});
    var p50 = asNumber(firstValue(latencySource, ["p50", "p50_ms", "median"], firstValue(snapshot, ["p50_ms"], 0)), 0);
    var p95 = asNumber(firstValue(latencySource, ["p95", "p95_ms"], firstValue(snapshot, ["p95_ms"], 0)), 0);
    var p99 = asNumber(firstValue(latencySource, ["p99", "p99_ms"], firstValue(snapshot, ["p99_ms"], 0)), 0);
    if (series.length && !p50 && !p95 && !p99) {
      var latencySamples = series.map(function (point) { return point.latency; }).filter(function (value) { return value >= 0; });
      p50 = percentile(latencySamples, 0.5);
      p95 = percentile(latencySamples, 0.95);
      p99 = percentile(latencySamples, 0.99);
    }

    var rawEvents = firstValue(snapshot, ["events", "recent_events", "timeline"], []);
    if (!Array.isArray(rawEvents)) rawEvents = [];

    var status = String(firstValue(snapshot, ["system_status", "status", "health.status", "service.status"], "")).toLowerCase();
    if (/ok|ready|operational/.test(status)) status = "healthy";
    if (/fail|outage|down|unhealthy/.test(status)) status = "incident";
    if (["healthy", "degraded", "incident"].indexOf(status) === -1) status = "degraded";
    if (mode === "unavailable" || circuit === "open") status = "incident";
    else if (mode !== "healthy" || circuit === "half-open") status = "degraded";

    return {
      status: status,
      mode: mode,
      circuit: circuit,
      requests: requests,
      throughput: asNumber(firstValue(snapshot, [
        "throughput_rps", "requests_per_second", "metrics.throughput_rps", "traffic.rps"
      ], throughputFromSeries(series)), 0),
      orders: asNumber(firstValue(snapshot, [
        "orders_total", "metrics.orders_total", "orders.total", "total_orders"
      ], 0), 0),
      duplicates: asNumber(firstValue(snapshot, [
        "duplicates_suppressed", "metrics.duplicates_suppressed", "idempotency_hits", "duplicate_count"
      ], 0), 0),
      errorRate: Math.max(0, Math.min(errorRate, 100)),
      errors: errors,
      latency: { p50: p50, p95: p95, p99: p99 },
      series: series,
      events: rawEvents.map(normalizeEvent),
      timestamp: firstValue(snapshot, ["timestamp", "generated_at", "updated_at"], new Date().toISOString())
    };
  }

  function throughputFromSeries(series) {
    if (!series.length) return 0;
    var windowSeconds = 5;
    var cutoff = Date.now() - windowSeconds * 1000;
    var recentRequests = series.reduce(function (total, point) {
      var timestamp = new Date(point.timestamp).getTime();
      return Number.isFinite(timestamp) && timestamp >= cutoff ? total + point.requests : total;
    }, 0);
    return recentRequests / windowSeconds;
  }

  function percentile(values, quantile) {
    if (!values.length) return 0;
    var sorted = values.slice().sort(function (a, b) { return a - b; });
    var position = (sorted.length - 1) * quantile;
    var base = Math.floor(position);
    var remainder = position - base;
    return sorted[base + 1] === undefined
      ? sorted[base]
      : sorted[base] + remainder * (sorted[base + 1] - sorted[base]);
  }

  async function request(path, options) {
    var controller = new AbortController();
    var timeout = window.setTimeout(function () { controller.abort(); }, 5000);
    var config = Object.assign({ headers: {} }, options || {}, { signal: controller.signal });
    if (config.body && !config.headers["Content-Type"]) config.headers["Content-Type"] = "application/json";

    try {
      var response = await fetch(path, config);
      var contentType = response.headers.get("content-type") || "";
      var payload = contentType.indexOf("application/json") >= 0 ? await response.json() : await response.text();
      if (!response.ok) {
        var message = payload;
        if (typeof payload === "object" && payload) {
          message = payload.message;
          if (typeof payload.error === "string") message = payload.error;
          if (payload.error && typeof payload.error === "object") message = payload.error.message || payload.error.code;
        }
        throw new Error(message || "Request failed with status " + response.status);
      }
      return payload;
    } finally {
      window.clearTimeout(timeout);
    }
  }

  async function pollSnapshot() {
    window.clearTimeout(pollTimer);
    try {
      var payload = await request("/v1/demo/snapshot");
      lastSnapshot = normalizeSnapshot(payload);
      setConnection("live", "Live · " + formatTime(lastSnapshot.timestamp));
      render(lastSnapshot);
    } catch (error) {
      setConnection("offline", "Reconnecting");
      if (!lastSnapshot) renderUnavailable();
    } finally {
      pollTimer = window.setTimeout(pollSnapshot, POLL_INTERVAL_MS);
    }
  }

  function setConnection(state, text) {
    elements.connectionPill.dataset.state = state;
    elements.connectionText.textContent = text;
  }

  function render(snapshot) {
    renderSystemState(snapshot);
    renderFaultMode(snapshot.mode);
    renderMetrics(snapshot);
    updateHistory(snapshot.latency, snapshot.series);
    renderChart();
    renderEvents(snapshot.events);
  }

  function renderSystemState(snapshot) {
    elements.systemState.dataset.state = snapshot.status;
    var labels = { healthy: "OPERATIONAL", degraded: "DEGRADED", incident: "INCIDENT" };
    var details = {
      healthy: "All reliability boundaries nominal",
      degraded: "Controls are containing a fault",
      incident: "Dependency unavailable · failing fast"
    };
    elements.systemStateText.textContent = labels[snapshot.status];
    elements.systemStateDetail.textContent = details[snapshot.status];
  }

  function renderFaultMode(mode) {
    document.querySelectorAll(".mode-button").forEach(function (button) {
      var active = button.dataset.mode === mode;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    });
    elements.dependencyNode.dataset.mode = mode;
    elements.dependencyModeLabel.textContent = {
      healthy: "Healthy response",
      slow: "Injected latency",
      flaky: "Intermittent failures",
      unavailable: "Forced outage"
    }[mode];
    elements.chartEvent.dataset.active = String(mode !== "healthy");
    elements.chartEvent.querySelector("span").textContent = mode === "healthy" ? "No fault active" : humanize(mode) + " fault active";
  }

  function renderMetrics(snapshot) {
    var circuitLabels = { closed: "CLOSED", open: "OPEN", "half-open": "HALF-OPEN" };
    elements.circuitState.textContent = circuitLabels[snapshot.circuit] || snapshot.circuit.toUpperCase();
    elements.circuitDetail.textContent = humanize(snapshot.mode) + " inventory · " + (snapshot.circuit === "closed" ? "requests admitted" : snapshot.circuit === "open" ? "requests fail fast" : "recovery probe active");
    elements.circuitTrack.style.width = snapshot.circuit === "closed" ? "28%" : snapshot.circuit === "open" ? "100%" : "64%";
    elements.circuitIndicator.style.background = snapshot.circuit === "open" ? "var(--danger)" : snapshot.circuit === "half-open" ? "var(--amber)" : "var(--ink)";
    elements.throughputValue.textContent = formatDecimal(snapshot.throughput);
    elements.orderCount.textContent = formatInteger(snapshot.orders);
    elements.duplicateValue.textContent = formatInteger(snapshot.duplicates);
    elements.errorValue.textContent = snapshot.errorRate.toFixed(snapshot.errorRate < 10 ? 1 : 0);
    elements.errorDetail.textContent = snapshot.errors ? formatInteger(snapshot.errors) + " failed requests observed" : "Across recent requests";
    elements.p50Value.textContent = formatLatency(snapshot.latency.p50);
    elements.p95Value.textContent = formatLatency(snapshot.latency.p95);
    elements.p99Value.textContent = formatLatency(snapshot.latency.p99);
  }

  function updateHistory(latency, series) {
    if (series && series.length) {
      var samples = series.slice(-HISTORY_LENGTH).map(function (point) { return point.latency; });
      chartHistory = { p50: [], p95: [], p99: [] };
      samples.forEach(function (_value, index) {
        var windowSamples = samples.slice(Math.max(0, index - 6), index + 1);
        chartHistory.p50.push(percentile(windowSamples, 0.5));
        chartHistory.p95.push(percentile(windowSamples, 0.95));
        chartHistory.p99.push(percentile(windowSamples, 0.99));
      });
      return;
    }
    ["p50", "p95", "p99"].forEach(function (key) {
      var next = Math.max(0, asNumber(latency[key], 0));
      chartHistory[key].push(next);
      if (chartHistory[key].length > HISTORY_LENGTH) chartHistory[key].shift();
    });
  }

  function renderChart() {
    var canvas = elements.canvas;
    var rect = canvas.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    var ratio = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = Math.round(rect.width * ratio);
    canvas.height = Math.round(rect.height * ratio);
    var context = canvas.getContext("2d");
    context.scale(ratio, ratio);
    context.clearRect(0, 0, rect.width, rect.height);

    var values = chartHistory.p50.concat(chartHistory.p95, chartHistory.p99);
    var highest = Math.max.apply(Math, values.concat([100]));
    var chartMax = Math.ceil(highest * 1.18 / 100) * 100;
    elements.chartMax.textContent = formatInteger(chartMax) + " ms";

    drawLine(context, chartHistory.p50, rect.width, rect.height, chartMax, "#68e0ae", 1.5);
    drawLine(context, chartHistory.p95, rect.width, rect.height, chartMax, "#c7f36b", 2);
    drawLine(context, chartHistory.p99, rect.width, rect.height, chartMax, "#ff795d", 1.5);
  }

  function drawLine(context, points, width, height, max, color, lineWidth) {
    if (!points.length) return;
    var paddingX = 3;
    var paddingY = 9;
    var usableWidth = width - paddingX * 2;
    var usableHeight = height - paddingY * 2;
    var denominator = Math.max(HISTORY_LENGTH - 1, 1);

    context.beginPath();
    points.forEach(function (value, index) {
      var visualIndex = index + HISTORY_LENGTH - points.length;
      var x = paddingX + visualIndex / denominator * usableWidth;
      var y = paddingY + usableHeight - Math.min(value / max, 1) * usableHeight;
      if (index === 0) context.moveTo(x, y);
      else context.lineTo(x, y);
    });
    context.strokeStyle = color;
    context.lineWidth = lineWidth;
    context.lineJoin = "round";
    context.lineCap = "round";
    context.stroke();

    var last = points[points.length - 1];
    var dotX = paddingX + (HISTORY_LENGTH - 1) / denominator * usableWidth;
    var dotY = paddingY + usableHeight - Math.min(last / max, 1) * usableHeight;
    context.beginPath();
    context.arc(dotX, dotY, 3.2, 0, Math.PI * 2);
    context.fillStyle = color;
    context.fill();
  }

  function renderEvents(events) {
    if (events.length) {
      events.forEach(function (event) {
        var existingIndex = eventCache.findIndex(function (cached) { return cached.id === event.id; });
        if (existingIndex === -1) eventCache.push(event);
        else eventCache[existingIndex] = event;
      });
      eventCache.sort(function (a, b) { return new Date(b.timestamp) - new Date(a.timestamp); });
      eventCache = eventCache.slice(0, 16);
    }

    elements.eventTimeline.replaceChildren();
    if (!eventCache.length) {
      var empty = document.createElement("li");
      empty.className = "timeline-empty";
      empty.textContent = "Waiting for service events…";
      elements.eventTimeline.appendChild(empty);
      return;
    }

    eventCache.forEach(function (event) {
      var item = document.createElement("li");
      item.dataset.severity = event.severity;
      var row = document.createElement("div");
      row.className = "event-row";
      var title = document.createElement("strong");
      title.textContent = event.title;
      var time = document.createElement("time");
      time.dateTime = String(event.timestamp);
      time.textContent = formatTime(event.timestamp);
      var detail = document.createElement("p");
      detail.textContent = event.detail;
      row.append(title, time);
      item.append(row, detail);
      elements.eventTimeline.appendChild(item);
    });
  }

  function renderUnavailable() {
    elements.systemState.dataset.state = "observing";
    elements.systemStateText.textContent = "OFFLINE";
    elements.systemStateDetail.textContent = "Waiting for the demo service";
  }

  async function setFaultMode(mode, button) {
    setBusy(button, true);
    try {
      await request("/v1/demo/fault", {
        method: "POST",
        body: JSON.stringify({ mode: mode })
      });
      showToast(humanize(mode) + " dependency mode enabled", mode === "unavailable" ? "warning" : "success");
      await pollImmediately();
    } catch (error) {
      showToast("Could not change fault mode: " + error.message, "error");
    } finally {
      setBusy(button, false);
    }
  }

  async function createOrder(idempotencyKey) {
    return request("/v1/orders", {
      method: "POST",
      headers: {
        "X-API-Key": elements.tenantSelect.value,
        "Idempotency-Key": idempotencyKey
      },
      body: JSON.stringify({
        event_id: "event-northstar-demo",
        quantity: 2
      })
    });
  }

  async function handleCreateOrder() {
    setBusy(elements.createOrderButton, true);
    try {
      var result = await createOrder(uniqueKey("order"));
      var orderId = firstValue(result, ["id", "order_id", "order.id"], "accepted");
      showToast("Reservation " + orderId + " created for " + selectedTenantName(), "success");
      await pollImmediately();
    } catch (error) {
      showToast("Order request failed: " + error.message, "error");
    } finally {
      setBusy(elements.createOrderButton, false);
    }
  }

  async function handleDuplicateProof() {
    setBusy(elements.duplicateButton, true);
    var key = uniqueKey("duplicate-proof");
    try {
      var results = await Promise.all(Array.from({ length: 8 }, function () { return createOrder(key); }));
      var ids = results.map(function (result) {
        return String(firstValue(result, ["id", "order_id", "order.id"], "accepted"));
      });
      var uniqueIds = Array.from(new Set(ids));
      if (uniqueIds.length === 1) {
        showToast("Idempotency proven: 8 concurrent requests → 1 reservation (" + uniqueIds[0] + ")", "success");
      } else {
        showToast("Unexpected result: " + uniqueIds.length + " unique reservations returned", "warning");
      }
      await pollImmediately();
    } catch (error) {
      showToast("Duplicate proof interrupted: " + error.message, "error");
    } finally {
      setBusy(elements.duplicateButton, false);
    }
  }

  async function handleReset() {
    setBusy(elements.resetButton, true);
    try {
      await request("/v1/demo/reset", { method: "POST" });
      chartHistory = { p50: [], p95: [], p99: [] };
      eventCache = [];
      showToast("Synthetic state reset", "success");
      await pollImmediately();
    } catch (error) {
      showToast("Could not reset demo: " + error.message, "error");
    } finally {
      setBusy(elements.resetButton, false);
    }
  }

  async function pollImmediately() {
    window.clearTimeout(pollTimer);
    await pollSnapshot();
  }

  function setBusy(button, busy) {
    if (!button) return;
    button.disabled = busy;
    button.setAttribute("aria-busy", String(busy));
  }

  function showToast(message, type) {
    var toast = document.createElement("div");
    toast.className = "toast";
    toast.dataset.type = type || "success";
    toast.textContent = message;
    elements.toastRegion.appendChild(toast);
    window.setTimeout(function () { toast.remove(); }, 5200);
  }

  function formatInteger(value) {
    return new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 }).format(asNumber(value, 0));
  }

  function formatDecimal(value) {
    return asNumber(value, 0).toFixed(value < 10 ? 1 : 0);
  }

  function formatLatency(value) {
    return asNumber(value, 0) ? formatInteger(value) + "ms" : "—";
  }

  function formatTime(value) {
    var date = new Date(value);
    if (Number.isNaN(date.getTime())) return "now";
    return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
  }

  function selectedTenantName() {
    return elements.tenantSelect.options[elements.tenantSelect.selectedIndex].text;
  }

  function uniqueKey(prefix) {
    var suffix = window.crypto && typeof window.crypto.randomUUID === "function"
      ? window.crypto.randomUUID()
      : Date.now() + "-" + Math.random().toString(16).slice(2);
    return prefix + "-" + suffix;
  }

  document.querySelectorAll(".mode-button").forEach(function (button) {
    button.addEventListener("click", function () { setFaultMode(button.dataset.mode, button); });
  });
  elements.createOrderButton.addEventListener("click", handleCreateOrder);
  elements.duplicateButton.addEventListener("click", handleDuplicateProof);
  elements.resetButton.addEventListener("click", handleReset);
  window.addEventListener("resize", renderChart);
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden) pollImmediately();
  });

  pollSnapshot();
}());
