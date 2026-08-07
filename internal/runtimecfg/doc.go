// Package runtimecfg provides hot-reloadable runtime configuration.
//
// Settings are loaded from a JSON file on disk and can be edited at runtime
// via POST /settings — changes are applied immediately to live components
// (LLM router, detection clusterer, sandbox policy) via an apply callback,
// without restarting Sentinel.
//
// The Manager is thread-safe (sync.RWMutex around the in-memory config).
// Save takes the write lock, marshals the new config to disk, swaps the
// in-memory pointer, and invokes the apply callback — all under the lock
// so concurrent Get / Save calls are serialized.
//
// The apply callback is set by main.go via SetApplyCallback. It receives
// the new RuntimeConfig and is expected to be a fast field-copy into live
// components (no I/O) — it runs under the Manager's lock. The callback is
// the only mechanism by which a POST /settings actually takes effect on
// the live system; without it, the config is persisted but inert.
//
// For K8s deployments, an optional ConfigMap poller (RunConfigMapPoller)
// re-reads a ConfigMap every 10 seconds and applies changes the same way
// a POST /settings does — useful when operators want to manage Sentinel's
// runtime config with kubectl apply rather than the API.
package runtimecfg
