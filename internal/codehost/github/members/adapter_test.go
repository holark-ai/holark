package members

import (
	"context"
	"errors"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/githubidentity/storeadapter"
	"path/filepath"
	"testing"
	"time"

	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	"github.com/holark-ai/holark/internal/githubidentity"
)

type fakeClient struct {
	current       githubapi.AuthenticatedUser
	collaborators []githubapi.Collaborator
	currentErr    error
	membersErr    error
}

func (client fakeClient) CurrentUser(context.Context) (githubapi.AuthenticatedUser, error) {
	return client.current, client.currentErr
}
func (client fakeClient) ListCollaborators(context.Context, githubapi.Repository) ([]githubapi.Collaborator, error) {
	return client.collaborators, client.membersErr
}

func TestSourceReturnsOnlyCompleteClassifiedSnapshots(t *testing.T) {
	syncFailure := &githubapi.Error{Code: githubapi.ErrorCodeSyncFailed, Err: errors.New("denied")}
	for _, test := range []struct {
		name       string
		client     fakeClient
		want       error
		wantLength int
	}{
		{name: "collaborator failure", client: fakeClient{current: githubapi.AuthenticatedUser{NodeID: "U_me"}, membersErr: syncFailure}, want: githubidentity.ErrGitHubFailed},
		{name: "success", client: fakeClient{current: githubapi.AuthenticatedUser{NodeID: "U_me"}, collaborators: []githubapi.Collaborator{{NodeID: "U_me", Login: "me", AvatarURL: "avatar", HTMLURL: "profile", Permission: "custom"}}}, wantLength: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := New(test.client).ProjectMembers(t.Context(), "https://github.com/owner/repo.git")
			if test.want != nil {
				if !errors.Is(err, test.want) || len(snapshot.Members) != 0 {
					t.Fatalf("snapshot = %#v, error = %v", snapshot, err)
				}
				return
			}
			if err != nil || snapshot.AuthenticatedNodeID != "" || len(snapshot.Members) != test.wantLength || snapshot.Members[0].ProfileURL != "profile" || snapshot.Members[0].Permission != "custom" {
				t.Fatalf("snapshot = %#v, error = %v", snapshot, err)
			}
		})
	}
}

func TestAuthenticatedIdentityDoesNotRequireCollaboratorPermission(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`create table repositories(id text primary key);insert into repositories values('repo')`); err != nil {
		t.Fatal(err)
	}
	store, err := storeadapter.New(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	source := New(fakeClient{current: githubapi.AuthenticatedUser{NodeID: "me", Login: "alice", AvatarURL: "avatar"}, membersErr: errors.New("collaborators forbidden")})
	service := githubidentity.NewService(store, source)
	if err = service.SyncAuthenticatedUser(t.Context(), "repo"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SyncProjectMembers(t.Context(), "repo", "https://github.com/owner/repo"); err == nil {
		t.Fatal("expected collaborator failure")
	}
	// Other observation and collaborator workflows cannot clear or impersonate me.
	if _, err = service.ObserveProjectMember(t.Context(), "repo", githubidentity.SourceMember{NodeID: "other", Login: "bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SyncProjectMembers(t.Context(), "repo", []githubidentity.StoredMember{{Member: githubidentity.Member{ID: "collaborator", Login: "bob", Permission: "write", IsMe: true}, NodeID: "other"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	members, err := service.ListProjectMembers(t.Context(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, member := range members {
		if member.IsMe {
			if member.Login != "alice" {
				t.Fatal(member)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("authenticated identity lost")
	}
}
