// Package events provides the in-process event broadcaster used by the API
// layer to push live updates to SSE / WebSocket clients.
//
// The interface is deliberately minimal: any component that wants to emit a
// UI-visible event (incident created, hypothesis formed, fix proposed, PR
// opened, …) calls BroadcastEvent on the singleton it was handed at startup.
// In tests, or when the API layer is disabled, the Noop implementation
// silently drops every event — letting internal callers stay unguarded.
package events
