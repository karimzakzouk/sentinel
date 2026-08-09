// Package codeintel provides lightweight code intelligence for the agent loop.
// It extracts symbols (functions, classes, methods, variables) from source
// files, builds a call graph, and provides semantic search via the existing
// HNSW vector index.
//
// Go files are parsed with go/ast (built-in, zero deps). Other languages
// use regex-based extraction — less precise than tree-sitter but CGO-free
// and handles the 7 most common languages.
package codeintel
