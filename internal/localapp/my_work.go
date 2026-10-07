package localapp

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/holark-ai/holark/internal/githubidentity"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
)

const openIssueRefreshInterval = 30 * time.Second

// personalWorkSync schedules independent sections without nesting scheduler waits.
type personalWorkSync struct {
	repositoryID string
	members      *githubidentity.Service
	issues       *issueworkflow.Service
	lifecycle    *pullrequestlifecycle.Coordinator
	scheduler    interface {
		Do(context.Context, pullrequestlifecycle.RefreshKey, func(context.Context) error) error
	}
	state pullrequestlifecycle.SyncRecorder
}

func (s *personalWorkSync) section(name string) func(context.Context) error {
	return func(ctx context.Context) error {
		var err error
		switch name {
		case "identity":
			err = s.members.SyncAuthenticatedUser(ctx, s.repositoryID)
		case "issues":
			_, err = s.issues.Sync(ctx, s.repositoryID)
		case "issues_open":
			_, err = s.issues.SyncOpen(ctx, s.repositoryID)
		}
		if name == "issues" || name == "issues_open" {
			return err
		}
		return errors.Join(err, s.state.RecordSync(context.WithoutCancel(ctx), s.repositoryID, name, err))
	}
}
func (s *personalWorkSync) Sync(ctx context.Context) error {
	var pending sync.WaitGroup
	var failures [3]error
	for i, name := range []string{"identity", "issues"} {
		pending.Add(1)
		go func() {
			defer pending.Done()
			failures[i] = s.scheduler.Do(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: s.repositoryID, Section: name}, s.section(name))
		}()
	}
	_, failures[2] = s.lifecycle.SyncActive(ctx, s.repositoryID)
	pending.Wait()
	return errors.Join(failures[:]...)
}
func (s *personalWorkSync) run(ctx context.Context, name string, interval time.Duration) {
	delay := interval
	section := name
	for {
		err := s.scheduler.Do(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: s.repositoryID, Section: section}, s.section(section))
		// Retry startup history until it succeeds. Use a separate key so an open
		// refresh cannot replace a pending explicit full import in the scheduler.
		if name == "issues" && err == nil {
			section = "issues_open"
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("%s synchronization: %v", name, err)
			delay = min(delay*2, 15*time.Minute)
			var retry interface{ RetryAfter() time.Duration }
			if errors.As(err, &retry) && retry.RetryAfter() > delay {
				delay = retry.RetryAfter()
			}
		} else {
			delay = interval
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
