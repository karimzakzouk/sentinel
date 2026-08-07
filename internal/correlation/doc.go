// Package correlation detects cascading failures across services.
//
// If service B deploys at T=0 and services A, C, D start failing within
// T+window, the correlator emits ONE CorrelationEvent naming B as the likely
// cause — instead of N independent investigations racing to fix symptoms.
//
// Algorithm:
//
//  1. Every incident is registered with RegisterIncident(service, ns, ts).
//  2. The correlator keeps a sliding window of incidents per service+namespace.
//  3. On each new incident, it counts how many distinct service+namespace keys
//     have incidents in the window.
//  4. If count >= threshold AND the cooldown has elapsed, a CorrelationEvent
//     is emitted. The handler is called synchronously (under no lock) so the
//     caller can run it in a goroutine if it is slow.
//  5. The "likely cause" is the most-recently-registered deployment that
//     preceded the cascade by at most 2*window.
//
// The caller is expected to feed deployment events via RegisterDeployment so
// the likely-cause attribution has data to work from; without deployments,
// LikelyCause will be the empty string and the caller falls back to its own
// heuristics.
package correlation
