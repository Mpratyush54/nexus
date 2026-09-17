package server

// vector.go — GET /memory/search vector path (issue #37).
//
// Master owns routes.go (text search via Store.SearchMemory) and the store
// package owns pgvector cosine (PostgresStore.SearchMemoryVector). This file
// is purely additive: it parses the optional ?embedding= query parameter and
// routes vector requests to the store when it supports them.
//
// ?q= is NEVER stub-embedded server-side: fake deterministic vectors would
// corrupt cosine ranking while looking authoritative. When both are present,
// embedding wins and ?q= is ignored. A store predating vector search answers
// 400, never silent text results.

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// parseEmbeddingParam parses the optional ?embedding= query parameter: a
// pgvector literal ("[0.1,0.2]") or bare comma/space-separated floats
// ("0.1,0.2"). "" (absent) returns nil, nil — the text path. Anything
// unparseable (including NaN/Inf, which ParseFloat accepts but pgvector
// rejects) is an error the handler maps to 400.
func parseEmbeddingParam(raw string) ([]float32, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "("), ")"))
	if s == "" {
		return nil, errors.New("empty embedding vector")
	}
	var parts []string
	if strings.Contains(s, ",") {
		parts = strings.Split(s, ",")
	} else {
		parts = strings.Fields(s)
	}
	vec := make([]float32, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("empty embedding component")
		}
		f, err := strconv.ParseFloat(part, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid embedding component %q", part)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("non-finite embedding component %q", part)
		}
		if len(vec) >= 4096 {
			return nil, errors.New("embedding exceeds 4096 dimensions")
		}
		vec = append(vec, float32(f))
	}
	if len(vec) == 0 {
		return nil, errors.New("empty embedding vector")
	}
	return vec, nil
}
