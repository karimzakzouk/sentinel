// Package errorparse holds the shared error-detection regexes used by both
// the detection layer (fast "is this log line an error?" scan) and the agents
// layer (structured extraction of error type, file, line, language).
//
// The patterns are pre-compiled at init because the detection layer runs
// them against every line of every streaming pod log — re-compiling per call
// burned measurable CPU on chatty pods. Each consumer may layer additional
// patterns on top of these; the set here is the common multi-language base.
package errorparse
