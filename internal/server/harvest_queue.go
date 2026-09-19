package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	memctx "central-memory/internal/context"
	"central-memory/internal/extract"
	"central-memory/internal/store"
)

// harvestSQSNotifier is optional; nil means DB-poll only.
type harvestSQSNotifier interface {
	SendHarvestJob(ctx context.Context, jobID, projectID string) error
}

// StartHarvestWorker claims queued jobs one-at-a-time and runs OpenRouter.
func (s *Server) StartHarvestWorker(ctx context.Context) {
	if s == nil || s.Harvest == nil {
		return
	}
	go s.runHarvestWorker(ctx)
}

func (s *Server) runHarvestWorker(ctx context.Context) {
	lg := s.Log
	if lg == nil {
		lg = log.Default()
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var mu sync.Mutex
	busy := false

	runOnce := func() {
		mu.Lock()
		if busy {
			mu.Unlock()
			return
		}
		busy = true
		mu.Unlock()
		defer func() {
			mu.Lock()
			busy = false
			mu.Unlock()
		}()

		job, err := s.Harvest.ClaimNextHarvestJob(ctx)
		if err != nil {
			lg.Printf("harvest worker: claim: %v", err)
			return
		}
		if job == nil {
			return
		}
		s.processHarvestJob(ctx, job)
	}

	runOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

func (s *Server) processHarvestJob(ctx context.Context, job *store.HarvestJob) {
	if job == nil {
		return
	}
	svc := s.resolveExtractor()
	if !svc.HasAPIKey() {
		_ = s.Harvest.FinishHarvestJob(ctx, job.ID, store.HarvestFailed, "", "OPENROUTER_API_KEY unset — refusing heuristic scrap", 0)
		return
	}

	turns := make([]extract.Turn, 0, len(job.Turns))
	for _, t := range job.Turns {
		turns = append(turns, extract.Turn{
			Speaker:   t.Speaker,
			Content:   t.Content,
			Timestamp: t.Timestamp,
			SessionID: t.SessionID,
		})
	}
	sessionID := extract.SessionIDFromTurns(turns)
	existing := s.harvestExisting(ctx, job.ProjectID)
	jobCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	saved := 0
	provider := extract.ProviderOpenRouter

	if sessionID != "" {
		result, err := svc.ExtractSessionCompress(jobCtx, job.ProjectID, sessionID, turns, existing)
		if err != nil {
			s.finishOrRetryHarvest(ctx, job, extract.ProviderOpenRouter, err.Error())
			return
		}
		if result.LLMError != "" {
			s.finishOrRetryHarvest(ctx, job, result.Provider, result.LLMError)
			return
		}
		provider = result.Provider
		if strings.TrimSpace(result.Summary) != "" {
			if _, err := s.upsertSessionSummary(ctx, job, sessionID, result.Summary); err != nil {
				if s.Log != nil {
					s.Log.Printf("harvest worker: session summary job=%s: %v", job.ID, err)
				}
			} else {
				saved++
			}
		}
		for _, p := range result.Decisions {
			if _, err := s.persistHarvestMemory(ctx, job, p); err != nil {
				if s.Log != nil {
					s.Log.Printf("harvest worker: persist job=%s key=%q: %v", job.ID, p.Key, err)
				}
				continue
			}
			saved++
		}
	} else {
		result, err := svc.ExtractLLMOnly(jobCtx, job.ProjectID, turns, existing)
		if err != nil {
			s.finishOrRetryHarvest(ctx, job, extract.ProviderOpenRouter, err.Error())
			return
		}
		if result.LLMError != "" {
			s.finishOrRetryHarvest(ctx, job, result.Provider, result.LLMError)
			return
		}
		provider = result.Provider
		for _, p := range result.Proposals {
			if _, err := s.persistHarvestMemory(ctx, job, p); err != nil {
				if s.Log != nil {
					s.Log.Printf("harvest worker: persist job=%s key=%q: %v", job.ID, p.Key, err)
				}
				continue
			}
			saved++
		}
	}

	_ = s.Harvest.FinishHarvestJob(ctx, job.ID, store.HarvestDone, provider, "", saved)
	if s.Log != nil {
		s.Log.Printf("harvest worker: done job=%s provider=%s memories=%d session=%s", job.ID, provider, saved, sessionID)
	}
}

func (s *Server) findMemoryByKey(ctx context.Context, projectID, key string) *store.MemoryItem {
	if s.Store == nil || key == "" {
		return nil
	}
	items, err := s.Store.SearchMemory(ctx, projectID, key, nil, 80)
	if err != nil {
		return nil
	}
	for _, it := range items {
		if it != nil && it.Key == key {
			return it
		}
	}
	return nil
}

func (s *Server) upsertSessionSummary(ctx context.Context, job *store.HarvestJob, sessionID, summary string) (*store.MemoryItem, error) {
	content := extract.ClampMemoryContent(summary)
	if err := store.ValidateMemoryContent(content); err != nil {
		return nil, err
	}
	key := "session/" + strings.TrimSpace(sessionID)
	src := "harvest:openrouter:session-compress"
	if job.Source != "" {
		src = src + ":" + job.Source
	}

	if existing := s.findMemoryByKey(ctx, job.ProjectID, key); existing != nil {
		if es, ok := s.memoryEditStore(); ok {
			patch := store.MemoryPatch{Content: &content}
			scope := "episode_summary"
			level := "project"
			patch.Scope = &scope
			patch.Level = &level
			item, err := es.UpdateMemory(ctx, existing.ID, patch, "harvest")
			if err != nil {
				return nil, err
			}
			s.notifyProjectActivity(job.ProjectID, "MEMORY_CONFIRMED", "/app/memory")
			s.publishMemoryLifecycle(item, "MEMORY_CONFIRMED", "confirmed")
			return item, nil
		}
	}

	item := &store.MemoryItem{
		ProjectID:  job.ProjectID,
		Key:        key,
		Content:    content,
		Level:      "project",
		Scope:      "episode_summary",
		Confidence: 0.9,
		Status:     store.StatusConfirmed,
		Source:     src,
		Tags:       []string{"session-compress", "session:" + sessionID},
	}
	item.Embedding = s.embedText(ctx, memctx.EmbedTextForItem(item.Key, item.Content))
	if err := s.Store.CreateMemoryItem(ctx, item); err != nil {
		return nil, err
	}
	s.notifyProjectActivity(job.ProjectID, "MEMORY_CONFIRMED", "/app/memory")
	s.publishMemoryLifecycle(item, "MEMORY_CONFIRMED", "confirmed")
	return item, nil
}

func (s *Server) finishOrRetryHarvest(ctx context.Context, job *store.HarvestJob, provider, errMsg string) {
	if job == nil {
		return
	}
	attempts := job.AttemptCount
	if store.HarvestErrorTransient(errMsg) && attempts < store.HarvestMaxAttempts {
		delay := store.HarvestRetryDelay(attempts + 1)
		if err := s.Harvest.RequeueHarvestJob(ctx, job.ID, provider, errMsg, delay); err != nil {
			_ = s.Harvest.FinishHarvestJob(ctx, job.ID, store.HarvestFailed, provider, errMsg, 0)
			return
		}
		if s.Log != nil {
			s.Log.Printf("harvest worker: retry job=%s attempt=%d delay=%s: %s", job.ID, attempts+1, delay, errMsg)
		}
		return
	}
	_ = s.Harvest.FinishHarvestJob(ctx, job.ID, store.HarvestFailed, provider, errMsg, 0)
	if s.Log != nil {
		s.Log.Printf("harvest worker: failed job=%s: %s", job.ID, errMsg)
	}
}

func (s *Server) harvestExisting(ctx context.Context, projectID string) []extract.Existing {
	if s.Store == nil {
		return nil
	}
	items, err := s.Store.SearchMemory(ctx, projectID, "", nil, 30)
	if err != nil || len(items) == 0 {
		return nil
	}
	out := make([]extract.Existing, 0, len(items))
	for _, it := range items {
		if it == nil {
			continue
		}
		out = append(out, extract.Existing{Level: it.Level, Scope: it.Scope, Content: it.Content})
	}
	return out
}

func (s *Server) persistHarvestMemory(ctx context.Context, job *store.HarvestJob, p extract.Proposal) (*store.MemoryItem, error) {
	content := strings.TrimSpace(p.Content)
	if err := store.ValidateMemoryContent(content); err != nil {
		return nil, err
	}
	level := extract.NormalizeLevel(p.Level, content)
	// Harvested chat is durable project memory — session is almost always wrong.
	if level == "session" {
		level = "project"
	}
	switch level {
	case "organization", "project", "personal":
	default:
		level = "project"
	}
	scope := extract.NormalizeScope(p.Scope, content)
	key := strings.TrimSpace(p.Key)
	if key == "" {
		key = extract.KeyFromContent(content)
	}
	conf := float32(p.Confidence)
	if conf <= 0 {
		conf = 0.85
	}
	src := "harvest:openrouter"
	if job.Source != "" {
		src = src + ":" + job.Source
	}
	item := &store.MemoryItem{
		ProjectID:  job.ProjectID,
		Key:        key,
		Content:    content,
		Level:      level,
		Scope:      scope,
		Confidence: conf,
		Status:     store.StatusConfirmed,
		Source:     src,
	}
	item.Embedding = s.embedText(ctx, memctx.EmbedTextForItem(item.Key, item.Content))
	if err := s.Store.CreateMemoryItem(ctx, item); err != nil {
		return nil, err
	}
	s.notifyProjectActivity(job.ProjectID, "MEMORY_CONFIRMED", "/app/memory")
	s.publishMemoryLifecycle(item, "MEMORY_CONFIRMED", "confirmed")
	return item, nil
}

type harvestEnqueueRequest struct {
	ProjectID string              `json:"project_id"`
	Turns     []store.HarvestTurn `json:"turns"`
	Source    string              `json:"source,omitempty"`
	// Existing is ignored — older daemons still send it; DisallowUnknownFields
	// would 400 and break harvest until every desktop is upgraded.
	Existing json.RawMessage `json:"existing,omitempty"`
}

func (s *Server) handleMemoryHarvestEnqueue(w http.ResponseWriter, r *http.Request) {
	if s.Harvest == nil {
		writeError(w, http.StatusServiceUnavailable, "harvest queue not configured")
		return
	}
	var req harvestEnqueueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if !s.authorizePermission(w, r, projectID, store.PermMemoryWrite) {
		return
	}
	if len(req.Turns) == 0 {
		writeError(w, http.StatusBadRequest, "turns required")
		return
	}
	if !s.eventAllowed("memory-harvest:" + projectID) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	job, created, err := s.Harvest.EnqueueHarvestJob(r.Context(), projectID, req.Source, req.Turns)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if created && s.HarvestSQS != nil {
		if err := s.HarvestSQS.SendHarvestJob(r.Context(), job.ID, projectID); err != nil && s.Log != nil {
			s.Log.Printf("harvest: sqs notify job=%s: %v (db worker will still pick it up)", job.ID, err)
		}
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	msg := "Raw turns accepted — visible now; OpenRouter will process this job next."
	if !created {
		msg = "Duplicate batch — already queued or processed (no duplicate update)."
	}
	writeJSON(w, status, map[string]any{
		"job":     job,
		"created": created,
		"queued":  created && job.Status == store.HarvestQueued,
		"message": msg,
	})
}

func (s *Server) handleMemoryHarvestList(w http.ResponseWriter, r *http.Request) {
	if s.Harvest == nil {
		writeError(w, http.StatusServiceUnavailable, "harvest queue not configured")
		return
	}
	if jobID := strings.TrimSpace(r.URL.Query().Get("job_id")); jobID != "" {
		s.handleMemoryHarvestGetByID(w, r, jobID)
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if !s.authorizePermission(w, r, projectID, store.PermMemoryRead) {
		return
	}
	full := strings.EqualFold(r.URL.Query().Get("full"), "1") ||
		strings.EqualFold(r.URL.Query().Get("full"), "true")
	limit := 40
	if full {
		limit = 20 // full turn payloads are large
	}
	var items []*store.HarvestJob
	var err error
	if full {
		items, err = s.Harvest.ListHarvestJobsOpt(r.Context(), projectID, limit, true)
	} else {
		items, err = s.Harvest.ListHarvestJobs(r.Context(), projectID, limit)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []*store.HarvestJob{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "full": full})
}

func (s *Server) handleMemoryHarvestGetByID(w http.ResponseWriter, r *http.Request, id string) {
	job, err := s.Harvest.GetHarvestJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "harvest job not found")
		return
	}
	if !s.authorizePermission(w, r, job.ProjectID, store.PermMemoryRead) {
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleMemoryHarvestGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	s.handleMemoryHarvestGetByID(w, r, id)
}

// handleMemoryExtract enqueues onto the harvest queue when available.
func (s *Server) handleMemoryExtract(w http.ResponseWriter, r *http.Request) {
	if s.Harvest != nil {
		s.handleMemoryHarvestEnqueue(w, r)
		return
	}
	s.handleMemoryExtractSync(w, r)
}

func (s *Server) handleMemoryExtractSync(w http.ResponseWriter, r *http.Request) {
	var req extractRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if !s.authorizePermission(w, r, projectID, store.PermMemoryWrite) {
		return
	}
	turns := extract.CapTurns(req.Turns)
	if len(turns) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"items": []*store.MemoryItem{}, "count": 0, "provider": extract.ProviderHeuristic,
		})
		return
	}
	totalChars := 0
	for _, t := range turns {
		totalChars += len(t.Content)
	}
	ownerType, ownerID := s.billingOwnerForProject(r.Context(), projectID)
	if !s.enforcePlanDimension(w, r, ownerType, ownerID, "memories") {
		return
	}
	if !s.eventAllowed("memory-extract:" + projectID) {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	if ok, reason := s.quotaAllowed(totalChars); !ok {
		writeError(w, http.StatusTooManyRequests, "quota exceeded: "+reason)
		return
	}
	existing := req.Existing
	if len(existing) == 0 {
		existing = s.extractExistingHints(r, projectID)
	}
	svc := s.resolveExtractor()
	result, err := svc.Extract(r.Context(), projectID, turns, existing)
	if err != nil {
		writeError(w, http.StatusBadGateway, "extraction failed: "+err.Error())
		return
	}
	srcHint := strings.TrimSpace(req.Source)
	created := make([]*store.MemoryItem, 0, len(result.Proposals))
	for _, p := range result.Proposals {
		item, cerr := s.persistExtractProposal(r, projectID, p, srcHint, result.Provider)
		if cerr != nil {
			continue
		}
		created = append(created, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": created, "count": len(created), "provider": result.Provider, "llm_error": result.LLMError,
	})
}

type sqsHarvestNotifier struct {
	url  string
	send func(ctx context.Context, queueURL, body string) error
}

func newSQSHarvestNotifierFromEnv() harvestSQSNotifier {
	url := strings.TrimSpace(os.Getenv("HARVEST_QUEUE_URL"))
	if url == "" {
		return nil
	}
	n := &sqsHarvestNotifier{url: url}
	n.send = sendSQSMessage
	return n
}

// NewSQSHarvestNotifierFromEnv is the exported wiring helper for cmd/server.
func NewSQSHarvestNotifierFromEnv() harvestSQSNotifier {
	return newSQSHarvestNotifierFromEnv()
}

func (n *sqsHarvestNotifier) SendHarvestJob(ctx context.Context, jobID, projectID string) error {
	if n == nil {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"job_id": jobID, "project_id": projectID})
	return n.send(ctx, n.url, string(body))
}
