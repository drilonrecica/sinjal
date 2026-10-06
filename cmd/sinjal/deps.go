package main

// Temporary: records the approved dependency versions (docs/40_DEPENDENCIES.md)
// in go.mod until real imports exist. Delete once chi is imported for real.
import (
	_ "github.com/go-chi/chi/v5"
)
