package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/holark-ai/holark/internal/issues"
	"github.com/holark-ai/holark/internal/issues/comments"
)

func TestCreatePublishesBeforeProjectingAndResolvesCanonicalIdentities(t *testing.T) {
	events := []string{}
	transport := &fakeTransport{events: &events, created: remote(42, "creator", "b", "a", "b")}
	projection := newFakeProjection(&events)
	identities := &fakeIdentities{events: &events}
	service := testService(transport, projection, identities)

	created, err := service.Create(t.Context(), "project", "Title", "Body")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"github_create", "resolve", "store"}) {
		t.Fatalf("events = %#v", events)
	}
	if identities.projectID != "project" || !reflect.DeepEqual(identities.nodeIDs, []string{"creator", "b", "a", "b"}) {
		t.Fatalf("resolution project=%q nodeIDs=%#v", identities.projectID, identities.nodeIDs)
	}
	if created.IssuerHolarkID != "member-creator" ||
		!reflect.DeepEqual(created.AssigneeHolarkIDs, []string{"member-b", "member-a"}) {
		t.Fatalf("created = %#v", created)
	}
}

func TestRejectedMutationLeavesProjectionUnchangedAndPersistenceFailureIsPending(t *testing.T) {
	projection := newFakeProjection(nil)
	projection.issues["stored"] = issue(7, "Stored")
	transport := &fakeTransport{updateErr: errors.New("rejected")}
	service := testService(transport, projection, &fakeIdentities{})

	if _, err := service.Close(t.Context(), "stored"); err == nil {
		t.Fatal("expected rejection")
	}
	if projection.storeCalls != 0 || projection.issues["stored"].Title != "Stored" {
		t.Fatalf("projection changed after rejection: %#v", projection)
	}

	transport.updateErr = nil
	transport.updated = remote(7, "ignored")
	projection.storeErr = errors.New("database unavailable")
	_, err := service.Close(t.Context(), "stored")
	var pending *ProjectionPendingError
	if !errors.As(err, &pending) || pending.GitHubIssueNumber != 7 ||
		pending.GitHubIssueURL != "https://github.com/o/r/issues/7" || pending.Operation != MutationClose {
		t.Fatalf("pending error = %#v (%v)", pending, err)
	}
}

func TestAcceptedMutationsBecomeOperationSpecificProjectionPendingErrors(t *testing.T) {
	acceptedCause := errors.New("response could not be decoded")
	accepted := fmt.Errorf("accepted mutation: %w: %w", ErrMutationAccepted, acceptedCause)

	for _, test := range []struct {
		name      string
		operation MutationOperation
		invoke    func(*Service) error
	}{
		{name: "create", operation: MutationCreate, invoke: func(service *Service) error {
			_, err := service.Create(t.Context(), "project", "Title", "Body")
			return err
		}},
		{name: "update", operation: MutationUpdate, invoke: func(service *Service) error {
			title := "Updated"
			_, err := service.Update(t.Context(), "stored", &title, nil)
			return err
		}},
		{name: "close", operation: MutationClose, invoke: func(service *Service) error {
			_, err := service.Close(t.Context(), "stored")
			return err
		}},
		{name: "reopen", operation: MutationReopen, invoke: func(service *Service) error {
			_, err := service.Reopen(t.Context(), "stored")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection := newFakeProjection(nil)
			projection.issues["stored"] = issue(7, "Stored")
			transport := &fakeTransport{
				created: remote(42, "creator"), createErr: accepted,
				updated: remote(7, "creator"), updateErr: accepted,
			}
			identities := &fakeIdentities{}
			err := test.invoke(testService(transport, projection, identities))

			var pending *ProjectionPendingError
			if !errors.As(err, &pending) {
				t.Fatalf("error = %v, want ProjectionPendingError", err)
			}
			if pending.Operation != test.operation {
				t.Fatalf("operation = %q, want %q", pending.Operation, test.operation)
			}
			wantNumber := 7
			if test.operation == MutationCreate {
				wantNumber = 42
			}
			if pending.GitHubIssueNumber != wantNumber || pending.GitHubIssueURL != fmt.Sprintf("https://github.com/o/r/issues/%d", wantNumber) {
				t.Fatalf("pending reference = %q #%d", pending.GitHubIssueURL, pending.GitHubIssueNumber)
			}
			if !errors.Is(err, ErrMutationAccepted) || !errors.Is(err, acceptedCause) {
				t.Fatalf("error = %v, want accepted marker and original cause", err)
			}
			if projection.storeCalls != 0 || identities.calls != 0 {
				t.Fatalf("post-transport work continued: stores=%d identity resolutions=%d", projection.storeCalls, identities.calls)
			}
		})
	}
}

func TestProjectionFailuresRecordMutationOperation(t *testing.T) {
	identityFailure := errors.New("identity unavailable")
	projectionFailure := errors.New("projection unavailable")

	projection := newFakeProjection(nil)
	service := testService(&fakeTransport{created: remote(42, "creator")}, projection, &fakeIdentities{err: identityFailure})
	_, err := service.Create(t.Context(), "project", "Title", "Body")
	var pending *ProjectionPendingError
	if !errors.As(err, &pending) || pending.Operation != MutationCreate || !errors.Is(err, identityFailure) {
		t.Fatalf("create identity error = %#v (%v)", pending, err)
	}

	projection = newFakeProjection(nil)
	projection.issues["stored"] = issue(7, "Stored")
	projection.storeErr = projectionFailure
	for _, test := range []struct {
		operation MutationOperation
		invoke    func(*Service) error
	}{
		{operation: MutationUpdate, invoke: func(service *Service) error {
			title := "Updated"
			_, err := service.Update(t.Context(), "stored", &title, nil)
			return err
		}},
		{operation: MutationClose, invoke: func(service *Service) error {
			_, err := service.Close(t.Context(), "stored")
			return err
		}},
		{operation: MutationReopen, invoke: func(service *Service) error {
			_, err := service.Reopen(t.Context(), "stored")
			return err
		}},
	} {
		projection.storeCalls = 0
		err := test.invoke(testService(&fakeTransport{updated: remote(7, "creator")}, projection, &fakeIdentities{}))
		pending = nil
		if !errors.As(err, &pending) || pending.Operation != test.operation || !errors.Is(err, projectionFailure) {
			t.Fatalf("%s projection error = %#v (%v)", test.operation, pending, err)
		}
	}
}

func TestProjectionPendingUsesRemoteReferenceWithoutReadingProjectionMetadata(t *testing.T) {
	for _, syncData := range []json.RawMessage{nil, json.RawMessage(`{"github":`)} {
		t.Run(string(syncData), func(t *testing.T) {
			created := remote(42, "creator")
			created.Reference = RemoteReference{URL: "https://github.com/canonical/repository/issues/91", Number: 91}
			created.Issue.SyncData = syncData
			projection := newFakeProjection(nil)
			projection.storeErr = errors.New("database unavailable")
			service := testService(&fakeTransport{created: created}, projection, &fakeIdentities{})

			_, err := service.Create(t.Context(), "project", "Title", "Body")
			var pending *ProjectionPendingError
			if !errors.As(err, &pending) {
				t.Fatalf("error = %v, want ProjectionPendingError", err)
			}
			if pending.GitHubIssueURL != created.Reference.URL || pending.GitHubIssueNumber != created.Reference.Number {
				t.Fatalf("pending reference = %q #%d, want %q #%d", pending.GitHubIssueURL, pending.GitHubIssueNumber, created.Reference.URL, created.Reference.Number)
			}
			if pending.Operation != MutationCreate {
				t.Fatalf("pending operation = %q, want %q", pending.Operation, MutationCreate)
			}
		})
	}
}

func TestTitleBodyAndStateUpdatesPreserveStoredIdentities(t *testing.T) {
	projection := newFakeProjection(nil)
	stored := issue(7, "Stored")
	stored.IssuerHolarkID = "issuer"
	stored.AssigneeHolarkIDs = []string{"one", "two"}
	projection.issues["stored"] = stored
	transport := &fakeTransport{updated: remote(7, "remote-creator", "remote-assignee")}
	identities := &fakeIdentities{}
	service := testService(transport, projection, identities)
	title := "New title"

	updated, err := service.Update(t.Context(), "stored", &title, nil)
	if err != nil {
		t.Fatal(err)
	}
	if identities.calls != 0 || updated.IssuerHolarkID != "issuer" ||
		!reflect.DeepEqual(updated.AssigneeHolarkIDs, []string{"one", "two"}) {
		t.Fatalf("updated = %#v identity calls=%d", updated, identities.calls)
	}
}

func TestAssigneeMutationFailuresDoNotChangeProjection(t *testing.T) {
	projection := newFakeProjection(nil)
	stored := issue(7, "Stored")
	projection.issues["stored"] = stored
	transport := &fakeTransport{assigneeRemote: remote(7, "creator", "node-a")}
	identities := &fakeIdentities{loginErr: errors.New("cross-project member")}
	service := testService(transport, projection, identities)

	if _, err := service.AddAssignee(t.Context(), "stored", "member-a"); !errors.Is(err, ErrProjectMemberUnresolved) {
		t.Fatalf("identity error=%v", err)
	}
	if transport.assigneeCalls != 0 || projection.storeCalls != 0 {
		t.Fatalf("unresolved member mutated provider=%d projection=%d", transport.assigneeCalls, projection.storeCalls)
	}

	identities.loginErr = nil
	transport.assigneeErr = errors.New("GitHub rejected mutation")
	if _, err := service.AddAssignee(t.Context(), "stored", "member-a"); err == nil {
		t.Fatal("expected provider rejection")
	}
	if projection.storeCalls != 0 || projection.issues["stored"].Title != "Stored" {
		t.Fatalf("projection changed after rejection: %#v", projection)
	}

	closed := stored
	closed.Status = issues.IssueClosed
	projection.issues["stored"] = closed
	transport.assigneeErr = nil
	if _, err := service.RemoveAssignee(t.Context(), "stored", "member-a"); !errors.Is(err, ErrIssueAssigneesReadOnly) {
		t.Fatalf("closed error=%v", err)
	}
	providerless := stored
	providerless.SyncProvider = ""
	projection.issues["stored"] = providerless
	if _, err := service.AddAssignee(t.Context(), "stored", "member-a"); !errors.Is(err, ErrProviderIdentityRequired) {
		t.Fatalf("providerless error=%v", err)
	}
}

func TestMissingIssueAssigneeMutationsReleaseIssueLocks(t *testing.T) {
	service := testService(&fakeTransport{}, newFakeProjection(nil), &fakeIdentities{})

	for index := range 100 {
		id := fmt.Sprintf("missing-%d", index)
		if _, err := service.AddAssignee(t.Context(), id, "member-a"); !errors.Is(err, issues.ErrIssueNotFound) {
			t.Fatalf("AddAssignee(%q) error = %v, want issue not found", id, err)
		}
	}

	service.lockMu.Lock()
	defer service.lockMu.Unlock()
	if len(service.issueLocks) != 0 {
		t.Fatalf("retained issue locks = %d, want 0", len(service.issueLocks))
	}
}

func TestAssigneePostProviderFailuresAreOperationSpecificProjectionPending(t *testing.T) {
	for _, test := range []struct {
		operation MutationOperation
		invoke    func(*Service) error
	}{
		{MutationReplaceAssignees, func(service *Service) error {
			_, err := service.ReplaceAssignees(t.Context(), "stored", []string{"member-a"})
			return err
		}},
		{MutationAddAssignee, func(service *Service) error {
			_, err := service.AddAssignee(t.Context(), "stored", "member-a")
			return err
		}},
		{MutationRemoveAssignee, func(service *Service) error {
			_, err := service.RemoveAssignee(t.Context(), "stored", "member-a")
			return err
		}},
	} {
		t.Run(string(test.operation), func(t *testing.T) {
			projection := newFakeProjection(nil)
			projection.issues["stored"] = issue(7, "Stored")
			identities := &fakeIdentities{err: errors.New("canonical node is not a project member")}
			service := testService(&fakeTransport{assigneeRemote: remote(7, "creator", "node-a")}, projection, identities)

			err := test.invoke(service)
			var pending *ProjectionPendingError
			if !errors.As(err, &pending) || pending.Operation != test.operation || pending.GitHubIssueNumber != 7 {
				t.Fatalf("pending=%#v error=%v", pending, err)
			}
			if projection.storeCalls != 0 {
				t.Fatalf("projection stored after canonical identity failure")
			}
		})
	}

	projection := newFakeProjection(nil)
	projection.issues["stored"] = issue(7, "Stored")
	projection.storeErr = errors.New("database unavailable")
	_, err := testService(
		&fakeTransport{assigneeRemote: remote(7, "creator", "node-a")},
		projection,
		&fakeIdentities{},
	).AddAssignee(t.Context(), "stored", "member-a")
	var pending *ProjectionPendingError
	if !errors.As(err, &pending) || pending.Operation != MutationAddAssignee {
		t.Fatalf("persistence pending=%#v error=%v", pending, err)
	}
}

func TestSameIssueCommandsAndAssigneeMutationsAreSerialized(t *testing.T) {
	projection := newFakeProjection(nil)
	projection.issues["stored"] = issue(7, "Stored")
	transport := &serialTransport{
		updateEntered: make(chan struct{}),
		releaseUpdate: make(chan struct{}),
		addEntered:    make(chan struct{}),
		remote:        remote(7, "creator", "node-a"),
	}
	service := New(func(slug string) (Project, bool) {
		return Project{ID: "project", RepositoryURL: "https://github.com/o/r"}, slug == "project"
	}, transport, projection, &fakeIdentities{})

	updateDone := make(chan error, 1)
	go func() {
		title := "Updated"
		_, err := service.Update(t.Context(), "stored", &title, nil)
		updateDone <- err
	}()
	<-transport.updateEntered

	addDone := make(chan error, 1)
	go func() {
		_, err := service.AddAssignee(t.Context(), "stored", "member-a")
		addDone <- err
	}()
	select {
	case <-transport.addEntered:
		t.Fatal("assignee mutation entered provider while update was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(transport.releaseUpdate)
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-transport.addEntered:
	case <-time.After(time.Second):
		t.Fatal("assignee mutation did not proceed after update completed")
	}
	if err := <-addDone; err != nil {
		t.Fatal(err)
	}
}

func TestSyncAndOutboundMutationAreSerialized(t *testing.T) {
	projection := newFakeProjection(nil)
	projection.issues["stored"] = issue(7, "Stored")
	stale := remote(7, "creator")
	stale.Issue.Title = "Stale snapshot"
	canonical := remote(7, "creator")
	canonical.Issue.Title = "Updated canonical"
	transport := &serialTransport{
		snapshotEntered: make(chan struct{}),
		releaseSnapshot: make(chan struct{}),
		updateEntered:   make(chan struct{}),
		remote:          canonical,
		snapshot:        []RemoteIssue{stale},
	}
	service := New(func(slug string) (Project, bool) {
		return Project{ID: "project", RepositoryURL: "https://github.com/o/r"}, slug == "project"
	}, transport, projection, &fakeIdentities{})

	syncDone := make(chan error, 1)
	go func() {
		_, err := service.Sync(t.Context(), "project")
		syncDone <- err
	}()
	<-transport.snapshotEntered

	updateDone := make(chan error, 1)
	go func() {
		title := "Updated"
		_, err := service.Update(t.Context(), "stored", &title, nil)
		updateDone <- err
	}()
	select {
	case <-transport.updateEntered:
		t.Fatal("mutation entered provider while authoritative sync was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(transport.releaseSnapshot)
	if err := <-syncDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-transport.updateEntered:
	case <-time.After(time.Second):
		t.Fatal("mutation did not proceed after sync completed")
	}
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	if got := projection.issues["stored"].Title; got != "Updated canonical" {
		t.Fatalf("final title = %q, want updated canonical projection", got)
	}
}

func TestSyncIsIdentityAwareAuthoritativeAndLeavesLocalIssues(t *testing.T) {
	projection := newFakeProjection(nil)
	kept := issue(1, "Old")
	projection.issues["kept"] = kept
	projection.issues["stale"] = issue(2, "Stale")
	projection.issues["local"] = issues.Issue{ID: "local", RepositoryID: "project", Title: "Local", Status: issues.IssueOpen}
	transport := &fakeTransport{snapshot: []RemoteIssue{
		remote(1, "creator-a", "b", "a"),
		remote(3, "creator-c"),
	}}
	identities := &fakeIdentities{}
	service := testService(transport, projection, identities)

	result, err := service.Sync(t.Context(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Updated != 1 || result.Exported != 0 {
		t.Fatalf("result = %#v", result)
	}
	if _, ok := projection.issues["stale"]; ok {
		t.Fatalf("stale issue remains: %#v", projection.issues)
	}
	if _, ok := projection.issues["local"]; !ok {
		t.Fatalf("local issue was deleted: %#v", projection.issues)
	}
	if !reflect.DeepEqual(identities.nodeIDs, []string{"creator-a", "b", "a", "creator-c"}) {
		t.Fatalf("flattened identities = %#v", identities.nodeIDs)
	}
	stored := projection.byExternal("github:o/r#1")
	if stored.IssuerHolarkID != "member-creator-a" ||
		!reflect.DeepEqual(stored.AssigneeHolarkIDs, []string{"member-b", "member-a"}) {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestSyncFailureSafetyAndEmptySnapshotDeletion(t *testing.T) {
	projection := newFakeProjection(nil)
	projection.issues["one"] = issue(1, "One")
	projection.issues["stale"] = issue(2, "Stale")
	transport := &fakeTransport{snapshot: []RemoteIssue{remote(1, "creator"), remote(3, "creator")}}
	identities := &fakeIdentities{err: errors.New("unknown member")}
	service := testService(transport, projection, identities)
	if _, err := service.Sync(t.Context(), "project"); err == nil {
		t.Fatal("expected identity failure")
	}
	if projection.storeCalls != 0 || projection.deleteCalls != 0 || len(projection.issues) != 2 {
		t.Fatalf("projection changed after identity failure: %#v", projection)
	}

	identities.err = nil
	projection.failStoreCall = 2
	if _, err := service.Sync(t.Context(), "project"); err == nil {
		t.Fatal("expected upsert failure")
	}
	if projection.deleteCalls != 0 {
		t.Fatalf("stale deletion ran after upsert failure")
	}
	if _, ok := projection.issues["stale"]; !ok {
		t.Fatal("stale issue deleted after partial upsert")
	}

	projection.failStoreCall = 0
	projection.storeCalls = 0
	transport.snapshot = []RemoteIssue{}
	if _, err := service.Sync(t.Context(), "project"); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range projection.issues {
		if candidate.SyncProvider == string(issues.IssueSyncProviderGitHub) {
			t.Fatalf("GitHub projection remains after empty snapshot: %#v", projection.issues)
		}
	}
}

func TestListAndGetReadProjectionWithoutTransport(t *testing.T) {
	projection := newFakeProjection(nil)
	projection.issues["one"] = issue(1, "One")
	transport := &fakeTransport{}
	service := testService(transport, projection, &fakeIdentities{})
	if _, err := service.List(t.Context(), "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if transport.snapshotCalls != 0 || transport.createCalls != 0 || transport.updateCalls != 0 {
		t.Fatalf("transport calls = %#v", transport)
	}
}

func TestSnapshotReturnsTaskDataFromProjectionWithoutExternalOperations(t *testing.T) {
	projection := newFakeProjection(nil)
	projection.issues["one"] = issues.Issue{
		ID: "one", RepositoryID: "project", Title: "Fix the bug", Body: "Reproduction details",
		Status: issues.IssueOpen,
	}
	transport := &fakeTransport{}
	identities := &fakeIdentities{}
	service := testService(transport, projection, identities)

	snapshot, err := service.Snapshot(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Discussion: comments.BuildContext(comments.Discussion{}), IssueID: "one", RepositoryID: "project", Title: "Fix the bug", Body: "Reproduction details"}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("snapshot = %#v, want %#v", snapshot, want)
	}
	if transport.snapshotCalls != 0 || transport.createCalls != 0 || transport.updateCalls != 0 || identities.calls != 0 {
		t.Fatalf("snapshot performed external operations: transport=%#v identities=%#v", transport, identities)
	}
	if _, err := service.Snapshot(t.Context(), "missing"); !errors.Is(err, issues.ErrIssueNotFound) {
		t.Fatalf("missing snapshot error = %v", err)
	}
}

func testService(transport *fakeTransport, projection *fakeProjection, identities *fakeIdentities) *Service {
	service := New(func(slug string) (Project, bool) {
		if slug != "project" {
			return Project{}, false
		}
		return Project{ID: "project", RepositoryURL: "https://github.com/o/r", DefaultBranch: "main"}, true
	}, transport, projection, identities)
	service.now = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }
	return service
}

func remote(number int, creator string, assignees ...string) RemoteIssue {
	return RemoteIssue{
		Reference: RemoteReference{URL: fmt.Sprintf("https://github.com/o/r/issues/%d", number), Number: number},
		Issue: issues.Issue{
			RepositoryID: "project", Title: fmt.Sprintf("Issue %d", number), Status: issues.IssueOpen,
			SyncProvider: "github", SyncExternalID: fmt.Sprintf("github:o/r#%d", number),
			SyncData: json.RawMessage(fmt.Sprintf(`{"github":{"number":%d,"url":"https://github.com/o/r/issues/%d"}}`, number, number)),
			Labels:   []issues.Label{}, AssigneeHolarkIDs: []string{}, LinkedPullRequestIDs: []string{},
			CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
		},
		CreatorGitHubNodeID: creator, AssigneeGitHubNodeIDs: assignees,
	}
}

func issue(number int, title string) issues.Issue {
	candidate := remote(number, "creator").Issue
	candidate.ID = map[int]string{1: "kept", 2: "stale", 7: "stored"}[number]
	candidate.Title = title
	candidate.IssuerHolarkID = "old-issuer"
	return candidate
}

type fakeTransport struct {
	events         *[]string
	snapshot       []RemoteIssue
	created        RemoteIssue
	updated        RemoteIssue
	snapshotErr    error
	createErr      error
	updateErr      error
	snapshotCalls  int
	createCalls    int
	updateCalls    int
	assigneeRemote RemoteIssue
	assigneeErr    error
	assigneeCalls  int
}

func (transport *fakeTransport) Snapshot(context.Context, string, string, time.Time) ([]RemoteIssue, error) {
	transport.snapshotCalls++
	return append([]RemoteIssue(nil), transport.snapshot...), transport.snapshotErr
}

func (transport *fakeTransport) Create(context.Context, string, string, string, string, time.Time) (RemoteIssue, error) {
	transport.createCalls++
	if transport.events != nil {
		*transport.events = append(*transport.events, "github_create")
	}
	return transport.created, transport.createErr
}

func (transport *fakeTransport) Update(context.Context, issues.Issue, string, UpdateRequest, time.Time) (RemoteIssue, error) {
	transport.updateCalls++
	return transport.updated, transport.updateErr
}

func (transport *fakeTransport) ReplaceAssignees(_ context.Context, _ issues.Issue, _ string, logins []string, _ time.Time) (RemoteIssue, error) {
	transport.recordAssignee("github_replace_assignees", logins)
	return transport.assigneeRemote, transport.assigneeErr
}

func (transport *fakeTransport) AddAssignee(_ context.Context, _ issues.Issue, _ string, login string, _ time.Time) (RemoteIssue, error) {
	transport.recordAssignee("github_add_assignee", []string{login})
	return transport.assigneeRemote, transport.assigneeErr
}

func (transport *fakeTransport) RemoveAssignee(_ context.Context, _ issues.Issue, _ string, login string, _ time.Time) (RemoteIssue, error) {
	transport.recordAssignee("github_remove_assignee", []string{login})
	return transport.assigneeRemote, transport.assigneeErr
}

func (transport *fakeTransport) recordAssignee(event string, logins []string) {
	transport.assigneeCalls++
	if transport.events != nil {
		*transport.events = append(*transport.events, event)
	}
}

type fakeIdentities struct {
	events    *[]string
	projectID string
	nodeIDs   []string
	err       error
	calls     int
	loginErr  error
}

func (identities *fakeIdentities) ResolveProjectMemberIDs(_ context.Context, projectID string, nodeIDs []string) ([]string, error) {
	identities.calls++
	identities.projectID = projectID
	identities.nodeIDs = append([]string(nil), nodeIDs...)
	if identities.events != nil {
		*identities.events = append(*identities.events, "resolve")
	}
	if identities.err != nil {
		return nil, identities.err
	}
	result := make([]string, len(nodeIDs))
	for index := range nodeIDs {
		result[index] = "member-" + nodeIDs[index]
	}
	return result, nil
}

func (identities *fakeIdentities) ResolveProjectMemberLogins(_ context.Context, _ string, ids []string) ([]string, error) {
	if identities.events != nil {
		*identities.events = append(*identities.events, "resolve_logins")
	}
	if identities.loginErr != nil {
		return nil, identities.loginErr
	}
	result := make([]string, len(ids))
	for index := range ids {
		result[index] = "login-" + ids[index]
	}
	return result, nil
}

type fakeProjection struct {
	mu            sync.RWMutex
	events        *[]string
	issues        map[string]issues.Issue
	storeErr      error
	failStoreCall int
	storeCalls    int
	deleteCalls   int
	nextID        int
}

func newFakeProjection(events *[]string) *fakeProjection {
	return &fakeProjection{events: events, issues: map[string]issues.Issue{}, nextID: 1}
}

func (projection *fakeProjection) Get(_ context.Context, id string) (issues.Issue, error) {
	projection.mu.RLock()
	defer projection.mu.RUnlock()
	issue, ok := projection.issues[id]
	if !ok {
		return issues.Issue{}, issues.ErrIssueNotFound
	}
	return cloneIssue(issue), nil
}

func (projection *fakeProjection) List(_ context.Context, projectID string) ([]issues.Issue, error) {
	projection.mu.RLock()
	defer projection.mu.RUnlock()
	result := []issues.Issue{}
	for _, issue := range projection.issues {
		if issue.RepositoryID == projectID {
			result = append(result, cloneIssue(issue))
		}
	}
	return result, nil
}

func (projection *fakeProjection) StoreSynced(_ context.Context, _ string, incoming issues.Issue) (issues.Issue, bool, error) {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	projection.storeCalls++
	if projection.events != nil {
		*projection.events = append(*projection.events, "store")
	}
	if projection.storeErr != nil {
		return issues.Issue{}, false, projection.storeErr
	}
	if projection.failStoreCall != 0 && projection.storeCalls == projection.failStoreCall {
		return issues.Issue{}, false, errors.New("upsert failed")
	}
	for id, existing := range projection.issues {
		if existing.SyncProvider == incoming.SyncProvider && existing.SyncExternalID == incoming.SyncExternalID {
			incoming.ID = id
			incoming.CreatedAt = existing.CreatedAt
			incoming.LinkedPullRequestIDs = append([]string(nil), existing.LinkedPullRequestIDs...)
			incoming.AssigneeHolarkIDs = deduplicate(incoming.AssigneeHolarkIDs)
			projection.issues[id] = cloneIssue(incoming)
			return cloneIssue(incoming), false, nil
		}
	}
	if incoming.ID == "" {
		incoming.ID = fmt.Sprintf("new-%d", projection.nextID)
		projection.nextID++
	}
	incoming.AssigneeHolarkIDs = deduplicate(incoming.AssigneeHolarkIDs)
	projection.issues[incoming.ID] = cloneIssue(incoming)
	return cloneIssue(incoming), true, nil
}

func (projection *fakeProjection) DeleteSyncedAbsent(_ context.Context, projectID, provider string, present []string) (int, error) {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	projection.deleteCalls++
	keep := map[string]struct{}{}
	for _, value := range present {
		keep[value] = struct{}{}
	}
	deleted := 0
	for id, issue := range projection.issues {
		if issue.RepositoryID != projectID || issue.SyncProvider != provider {
			continue
		}
		if _, ok := keep[issue.SyncExternalID]; ok {
			continue
		}
		delete(projection.issues, id)
		deleted++
	}
	return deleted, nil
}

func (projection *fakeProjection) byExternal(externalID string) issues.Issue {
	projection.mu.RLock()
	defer projection.mu.RUnlock()
	for _, issue := range projection.issues {
		if issue.SyncExternalID == externalID {
			return issue
		}
	}
	return issues.Issue{}
}

func cloneIssue(issue issues.Issue) issues.Issue {
	issue.AssigneeHolarkIDs = append([]string(nil), issue.AssigneeHolarkIDs...)
	issue.LinkedPullRequestIDs = append([]string(nil), issue.LinkedPullRequestIDs...)
	return issue
}

func deduplicate(values []string) []string {
	result := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

type serialTransport struct {
	snapshotEntered chan struct{}
	releaseSnapshot chan struct{}
	updateEntered   chan struct{}
	releaseUpdate   chan struct{}
	addEntered      chan struct{}
	remote          RemoteIssue
	snapshot        []RemoteIssue
}

func (transport *serialTransport) Snapshot(context.Context, string, string, time.Time) ([]RemoteIssue, error) {
	close(transport.snapshotEntered)
	<-transport.releaseSnapshot
	return append([]RemoteIssue(nil), transport.snapshot...), nil
}

func (*serialTransport) Create(context.Context, string, string, string, string, time.Time) (RemoteIssue, error) {
	panic("unexpected create")
}

func (transport *serialTransport) Update(context.Context, issues.Issue, string, UpdateRequest, time.Time) (RemoteIssue, error) {
	close(transport.updateEntered)
	if transport.releaseUpdate != nil {
		<-transport.releaseUpdate
	}
	return transport.remote, nil
}

func (*serialTransport) ReplaceAssignees(context.Context, issues.Issue, string, []string, time.Time) (RemoteIssue, error) {
	panic("unexpected replace")
}

func (transport *serialTransport) AddAssignee(context.Context, issues.Issue, string, string, time.Time) (RemoteIssue, error) {
	close(transport.addEntered)
	return transport.remote, nil
}

func (*serialTransport) RemoveAssignee(context.Context, issues.Issue, string, string, time.Time) (RemoteIssue, error) {
	panic("unexpected remove")
}
