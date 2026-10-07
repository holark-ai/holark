package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	githubratelimit "github.com/holark-ai/holark/internal/codehost/github/ratelimit"
	"github.com/holark-ai/holark/internal/workitems"
)

type Syncer interface{ Sync(context.Context) error }

func RegisterRoutes(register func(string, http.HandlerFunc), service *workitems.Service, sync Syncer) {
	for _, kind := range []string{"pull-requests", "issues"} {
		register("GET /api/v1/"+kind+"/search", func(w http.ResponseWriter, r *http.Request) {
			page, size, ok := pagination(w, r)
			if !ok {
				return
			}
			parse, search := workitems.Parse, service.SearchTitle
			if kind == "issues" {
				parse, search = workitems.ParseIssues, service.SearchIssuesTitle
			}
			raw := r.URL.Query().Get("q")
			if _, err := parse(raw); err != nil {
				fail(w, 400, err.Error())
				return
			}
			result, err := search(r.Context(), raw, r.URL.Query().Get("title"), page, size)
			if err != nil {
				fail(w, 500, "Could not read cached "+kind+".")
				return
			}
			write(w, result)
		})
	}
	register("GET /api/v1/issues/search/labels", func(w http.ResponseWriter, r *http.Request) {
		labels, err := service.Labels(r.Context())
		if err != nil {
			fail(w, 500, "Could not read cached labels.")
			return
		}
		write(w, labels)
	})
	register("GET /api/v1/my-work", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, size, ok := pagination(w, r)
		if !ok {
			return
		}
		view := r.URL.Query().Get("view")
		if view == "" {
			view = "all"
		}
		switch view {
		case "all", "review_requested", "assigned_issues", "assigned_pull_requests":
		default:
			fail(w, 400, "Unknown My work view.")
			return
		}
		result, err := service.MyWork(r.Context(), view, page, size)
		if err != nil {
			fail(w, 500, "Could not read cached work.")
			return
		}
		write(w, result)
	}))
	register("POST /api/v1/my-work/sync", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := sync.Sync(r.Context()); err != nil {
			if githubratelimit.Exceeded(err) {
				fail(w, http.StatusTooManyRequests, "GitHub API rate limit exceeded. Try again after the limit resets.")
				return
			}
			fail(w, http.StatusBadGateway, "My work synchronization failed. Some sections could not be refreshed.")
			return
		}
		write(w, map[string]bool{"synced": true})
	}))
}
func pagination(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	page, size := 1, 50
	for key, target := range map[string]*int{"page": &page, "per_page": &size} {
		if raw := r.URL.Query().Get(key); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil || n < 1 || n > 1000000 {
				fail(w, 400, "Pagination must use positive integers.")
				return 0, 0, false
			}
			*target = n
		}
	}
	if size > 100 {
		size = 100
	}
	return page, size, true
}
func write(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}
