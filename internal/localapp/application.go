// Package localapp composes Holark's repository, durable Holon state, local
// PTY runtime, and HTTP adapters without owning a listener or authentication.
package localapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/holark-ai/holark/internal/agentsessions"
	"github.com/holark-ai/holark/internal/agentsettings"
	agentsettingshttp "github.com/holark-ai/holark/internal/agentsettings/httpapi"
	agentsettingssqlite "github.com/holark-ai/holark/internal/agentsettings/sqliteadapter"
	"github.com/holark-ai/holark/internal/attachments"
	attachmentshttp "github.com/holark-ai/holark/internal/attachments/httpapi"
	githubapi "github.com/holark-ai/holark/internal/codehost/github/api"
	githubattachments "github.com/holark-ai/holark/internal/codehost/github/attachments"
	githubissues "github.com/holark-ai/holark/internal/codehost/github/issues"
	githubmembers "github.com/holark-ai/holark/internal/codehost/github/members"
	githubprcomments "github.com/holark-ai/holark/internal/codehost/github/pullrequestcomments"
	githubprmetadata "github.com/holark-ai/holark/internal/codehost/github/pullrequestmetadata"
	githubprparticipants "github.com/holark-ai/holark/internal/codehost/github/pullrequestparticipants"
	githubprreviews "github.com/holark-ai/holark/internal/codehost/github/pullrequestreviews"
	githubpullrequests "github.com/holark-ai/holark/internal/codehost/github/pullrequests"
	"github.com/holark-ai/holark/internal/database"
	"github.com/holark-ai/holark/internal/githubbinding"
	"github.com/holark-ai/holark/internal/githubidentity"
	githubidentityhttp "github.com/holark-ai/holark/internal/githubidentity/httpapi"
	githubidentitystore "github.com/holark-ai/holark/internal/githubidentity/storeadapter"
	"github.com/holark-ai/holark/internal/harness"
	"github.com/holark-ai/holark/internal/harness/cliprobe"
	"github.com/holark-ai/holark/internal/harness/codex"
	harnesshttp "github.com/holark-ai/holark/internal/harness/httpapi"
	"github.com/holark-ai/holark/internal/holons"
	holonshttp "github.com/holark-ai/holark/internal/holons/httpapi"
	holonssqlite "github.com/holark-ai/holark/internal/holons/sqliteadapter"
	"github.com/holark-ai/holark/internal/ide"
	ideholons "github.com/holark-ai/holark/internal/ide/holonadapter"
	idehttp "github.com/holark-ai/holark/internal/ide/httpapi"
	idelocal "github.com/holark-ai/holark/internal/ide/localadapter"
	idesqlite "github.com/holark-ai/holark/internal/ide/sqliteadapter"
	"github.com/holark-ai/holark/internal/issues"
	issueholons "github.com/holark-ai/holark/internal/issues/holonworkflow"
	issueholonshttp "github.com/holark-ai/holark/internal/issues/holonworkflow/httpapi"
	issueshttp "github.com/holark-ai/holark/internal/issues/httpapi"
	issuesstore "github.com/holark-ai/holark/internal/issues/storeadapter"
	issueworkflow "github.com/holark-ai/holark/internal/issues/workflow"
	"github.com/holark-ai/holark/internal/prompt_templates"
	prompttemplateshttp "github.com/holark-ai/holark/internal/prompt_templates/http_api"
	prompttemplatessqlite "github.com/holark-ai/holark/internal/prompt_templates/sqlite_adapter"
	"github.com/holark-ai/holark/internal/pullrequestcomments"
	pullrequestcommentshttp "github.com/holark-ai/holark/internal/pullrequestcomments/httpapi"
	pullrequestcommentssqlite "github.com/holark-ai/holark/internal/pullrequestcomments/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestlifecycle"
	pullrequestshttp "github.com/holark-ai/holark/internal/pullrequestlifecycle/httpapi"
	pullrequestssqlite "github.com/holark-ai/holark/internal/pullrequestlifecycle/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestmerge"
	"github.com/holark-ai/holark/internal/pullrequestmetadata"
	metadatagit "github.com/holark-ai/holark/internal/pullrequestmetadata/gitadapter"
	pullrequestmetadatahttp "github.com/holark-ai/holark/internal/pullrequestmetadata/httpapi"
	pullrequestmetadatasqlite "github.com/holark-ai/holark/internal/pullrequestmetadata/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestparticipants"
	pullrequestparticipantshttp "github.com/holark-ai/holark/internal/pullrequestparticipants/httpapi"
	pullrequestparticipantssqlite "github.com/holark-ai/holark/internal/pullrequestparticipants/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestreviews"
	pullrequestreviewshttp "github.com/holark-ai/holark/internal/pullrequestreviews/httpapi"
	pullrequestreviewssqlite "github.com/holark-ai/holark/internal/pullrequestreviews/sqliteadapter"
	pullrequesttrackingsqlite "github.com/holark-ai/holark/internal/pullrequesttracking/sqliteadapter"
	"github.com/holark-ai/holark/internal/pullrequestwork"
	pullrequestworkhttp "github.com/holark-ai/holark/internal/pullrequestwork/httpapi"
	pullrequestworksqlite "github.com/holark-ai/holark/internal/pullrequestwork/sqliteadapter"
	"github.com/holark-ai/holark/internal/repository"
	"github.com/holark-ai/holark/internal/repository/gitadapter"
	repositorysqlite "github.com/holark-ai/holark/internal/repository/sqliteadapter"
	"github.com/holark-ai/holark/internal/repositorybrowser"
	"github.com/holark-ai/holark/internal/repositoryhttp"
	"github.com/holark-ai/holark/internal/sessions/branchnaming"
	"github.com/holark-ai/holark/internal/sessionterminals"
	"github.com/holark-ai/holark/internal/terminalenv"
	"github.com/holark-ai/holark/internal/terminalhost"
	"github.com/holark-ai/holark/internal/terminals"
	terminalshttp "github.com/holark-ai/holark/internal/terminals/httpapi"
	"github.com/holark-ai/holark/internal/terminals/localadapter"
	"github.com/holark-ai/holark/internal/workitems"
	workitemshttp "github.com/holark-ai/holark/internal/workitems/httpapi"
	workitemssqlite "github.com/holark-ai/holark/internal/workitems/sqliteadapter"
	"github.com/holark-ai/holark/internal/workspace"
)

type Options struct {
	HarnessDiscovery            cliprobe.Discovery
	RepositoryPath              string
	HomeDirectory               string
	Terminal                    terminalhost.Options
	TerminalContext             terminalenv.Context
	IDEExecutable               string
	IDEReadinessTimeout         time.Duration
	IDEShutdownTimeout          time.Duration
	ConfirmGitHubRepository     bool
	PullRequestBranchPublisher  pullrequestlifecycle.BranchPublisher
	PullRequestGitHubTransport  pullrequestlifecycle.GitHubTransport
	PullRequestMergeProvider    pullrequestmerge.GitHubProvider
	PullRequestMetadataAgents   pullrequestmetadata.AgentSessions
	PullRequestMetadataProvider pullrequestmetadata.Provider
	PullRequestRepositoryURL    string
	// GitHubRepositoryURL separates provider identity from the Git transport URL.
	// A pointer to an empty string disables GitHub binding for a local remote.
	GitHubRepositoryURL     *string
	PullRequestWorkLauncher pullrequestwork.Launcher
}

type Application struct {
	manualStartups        *manualStartupJobs
	recoveryDone          chan struct{}
	commentPublisherDone  chan struct{}
	Handler               http.Handler
	Codex                 *codex.Runtime
	agentSettingsDatabase *sql.DB
	agents                *agentsessions.Service
	Holons                *holons.Service
	Database              *sql.DB
	Manager               *terminalhost.Manager
	IDEs                  *ide.Service
	RepositoryID          string
	PullRequestWork       *pullrequestwork.Service
	prRefresh             *pullrequestlifecycle.RefreshCoordinator
	dispatcherDone        chan struct{}
	cancel                context.CancelFunc
	done                  chan struct{}
	lock                  *repositoryInstanceLock
}
type localIssueRepository struct{ *repository.Service }

func (r localIssueRepository) DefaultBranch() string { return r.Descriptor().DefaultBranch }

type localPullRequestRepository struct {
	*repository.Service
	scheduler *pullrequestlifecycle.RefreshCoordinator
}

func (r localPullRequestRepository) Refresh(ctx context.Context, _ repositorybrowser.Repository) error {
	return withPullRequestGit(ctx, r.scheduler, r.Descriptor().ID, func(ctx context.Context) error { _, err := r.Service.Refresh(ctx); return err })
}
func (r localPullRequestRepository) ResolveRef(ctx context.Context, _ repositorybrowser.Repository, ref string) (string, error) {
	return r.Resolve(ctx, ref)
}

type localPullRequestMergeRepository struct {
	*repository.Service
	scheduler *pullrequestlifecycle.RefreshCoordinator
}

func (r localPullRequestMergeRepository) Refresh(ctx context.Context) error {
	return withPullRequestGit(ctx, r.scheduler, r.Descriptor().ID, func(ctx context.Context) error { _, err := r.Service.Refresh(ctx); return err })
}

type localPullRequestHolons struct{ service *holons.Service }

func (h localPullRequestHolons) PullRequestSource(ctx context.Context, id string) (pullrequestlifecycle.PullRequestSource, error) {
	v, err := h.service.Get(ctx, id)
	if err != nil {
		return pullrequestlifecycle.PullRequestSource{}, err
	}
	i, err := h.service.InspectWorkspace(ctx, id)
	if err != nil {
		return pullrequestlifecycle.PullRequestSource{}, err
	}
	return pullrequestlifecycle.PullRequestSource{HolonID: v.ID, BaseBranch: v.BaseBranch, BaseCommit: v.BaseCommit, HeadBranch: i.Branch, HeadCommit: i.HeadCommit, PullRequestID: v.PullRequestID, HasChanges: i.HasChanges, Dirty: i.Dirty}, nil
}
func (h localPullRequestHolons) PullRequestReference(ctx context.Context, id string) (string, error) {
	v, err := h.service.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return v.PullRequestID, nil
}

type localPullRequestPreparation struct{ service *pullrequestmetadata.Service }

func (p localPullRequestPreparation) RequestOpen(ctx context.Context, id, operationID string) (bool, error) {
	handled, err := p.service.RequestOpen(ctx, id, operationID)
	if errors.Is(err, pullrequestmetadata.ErrAgentTurnActive) {
		return handled, pullrequestlifecycle.Error{Code: "metadata_agent_busy", Message: "A metadata agent turn is already running."}
	}
	return handled, err
}

func (p localPullRequestPreparation) ClearPreparation(ctx context.Context, id string) error {
	return p.service.ClearPreparation(ctx, id)
}

func New(ctx context.Context, options Options) (*Application, error) {
	home := options.HomeDirectory
	applicationLogger := log.Default()
	worktreesDirectory := filepath.Join(home, "worktrees")
	gitRepository, err := gitadapter.OpenWithWorktrees(ctx, options.RepositoryPath, worktreesDirectory)
	if err != nil {
		return nil, err
	}
	repositoryContext, stopRepository := context.WithCancel(ctx)
	repositories := repository.NewServiceWithContext(repositoryContext, gitRepository)
	opened := false
	defer func() {
		if !opened {
			stopRepository()
		}
	}()
	descriptor := repositories.Descriptor()
	repositoryKey := repositoryDatabaseKey(descriptor.ID)
	instanceLock, err := acquireRepositoryInstanceLock(home, repositoryKey)
	if err != nil {
		return nil, err
	}
	db, err := database.Open(filepath.Join(home, "state", "repositories", repositoryKey+".sqlite"))
	if err != nil {
		_ = instanceLock.Close()
		return nil, err
	}
	processRegistry, err := terminalhost.OpenProcessRegistry(filepath.Join(home, "runtimes", "processes", repositoryKey))
	if err != nil {
		return nil, errors.Join(err, db.Close(), instanceLock.Close())
	}
	sharedDB, err := database.Open(filepath.Join(home, "state", "agent-settings.sqlite"))
	if err != nil {
		return nil, errors.Join(err, db.Close(), instanceLock.Close())
	}
	commandStore, err := agentsettingssqlite.New(ctx, sharedDB)
	if err != nil {
		return nil, errors.Join(err, sharedDB.Close(), db.Close(), instanceLock.Close())
	}
	launchCommands := agentsettings.NewLaunchCommands(commandStore, cliprobe.Invalidate)
	codexManager := codex.NewRuntime(codex.ManagerOptions{TerminalContext: options.TerminalContext, ProcessRegistry: processRegistry, Discovery: options.HarnessDiscovery}, launchCommands)
	var manager *terminalhost.Manager
	var ideService *ide.Service
	fail := func(e error) (*Application, error) {
		_ = codexManager.Close()
		if ideService != nil {
			_ = ideService.Shutdown()
		}
		if manager != nil {
			manager.Close()
		}
		return nil, errors.Join(e, sharedDB.Close(), db.Close(), instanceLock.Close())
	}
	if err = repositorysqlite.Bind(ctx, db, descriptor); err != nil {
		return fail(err)
	}
	registry := harness.RegistryWithProbeOptions(codexManager, harness.ProbeOptions{Discovery: options.HarnessDiscovery, Commands: launchCommands})
	agentSettingsStore, err := agentsettingssqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	agentSettings := agentsettings.New(agentSettingsStore, registry)
	promptTemplateStore, err := prompttemplatessqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	promptTemplateService := prompttemplates.New(promptTemplateStore)
	bindingStore, err := githubbinding.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	githubRepositoryURL := descriptor.RepositoryURL
	if options.GitHubRepositoryURL != nil {
		githubRepositoryURL = *options.GitHubRepositoryURL
	}
	binding, err := bindingStore.Bind(ctx, descriptor.ID, githubRepositoryURL, options.ConfirmGitHubRepository)
	if err != nil {
		return fail(err)
	}
	store, err := holonssqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	service := holons.NewServiceWithRepository(store, holonRepositoryCoordinator{repositories: repositories})
	ideStore, err := idesqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	ideWorkspaces := ideholons.New(service)
	ideRuntime := idelocal.New(idelocal.Options{ConfiguredExecutable: options.IDEExecutable, DataDirectory: filepath.Join(home, "ide"), ReadinessTimeout: options.IDEReadinessTimeout, ShutdownTimeout: options.IDEShutdownTimeout})
	ideService = ide.New(ideStore, ideWorkspaces, ideRuntime)
	ideCoordinator := &localIDEService{Service: ideService, holons: service}
	if err = ideService.Recover(ctx); err != nil {
		return fail(err)
	}
	if err = settleInterruptedManualPreparations(ctx, service); err != nil {
		return fail(err)
	}
	if err = clearStaleTerminalBindings(ctx, service); err != nil {
		return fail(err)
	}
	interrupted, err := service.List(ctx)
	if err != nil {
		return fail(err)
	}
	var restoringSessions []string
	for _, h := range interrupted {
		for _, a := range h.AgentSessions {
			if a.ClosedAt == nil && a.Status == string(holons.StatusRestoring) {
				restoringSessions = append(restoringSessions, h.ID)
				break
			}
		}
	}
	options.Terminal.ProcessRegistry = processRegistry
	manager, err = terminalhost.NewManager(options.Terminal)
	if err != nil {
		return fail(err)
	}
	products := &localTerminalProducts{holons: service}
	gateway := localadapter.New(manager, options.TerminalContext)
	coordinator, err := sessionterminals.New(gateway, products)
	if err != nil {
		return fail(err)
	}
	workspaceManager, err := workspace.NewManager(worktreesDirectory)
	if err != nil {
		return fail(err)
	}
	agentState := &localAgentState{holons: service, templates: promptTemplateService}
	agentService, err := agentsessions.New(registry, agentState, localAgentLauncher{manager: manager, terminalContext: options.TerminalContext}, localSkillInstaller{manager: workspaceManager}, localAgentNamer{harnesses: agentSettings, namer: branchnaming.NewDispatcherWithCommands(launchCommands), templates: promptTemplateService}, filepath.Join(home, "runtimes", "agents"))
	if err != nil {
		return fail(err)
	}
	gateway.InputDelivered = agentService.ObserveTerminalInput
	agentState.agents = agentService
	commitAgents := &commitAgentCoordinator{holons: service, agents: agentService, harnesses: agentSettings, templates: promptTemplateService}
	forkAgents := &forkAgentCoordinator{holons: service, agents: agentService}
	terminalService := &terminalHolonService{naming: agentService, commitAgents: commitAgents, forkAgents: forkAgents, Service: service, terminals: coordinator, agents: agentService, ides: ideCoordinator, templates: promptTemplateService, harnesses: agentSettings}
	commitAgents.pinPullRequests = terminalService.pinHolonPullRequests
	forkAgents.pinPullRequests = terminalService.pinHolonPullRequests
	agentState.commitCloser = terminalService
	memberStore, err := githubidentitystore.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	memberService := githubidentity.NewService(memberStore, githubmembers.New(nil))
	issueStore, err := issuesstore.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	issueProjection := issues.NewService(issueStore)
	project := issueworkflow.Project{ID: descriptor.ID, RepositoryURL: binding.RepositoryURL, DefaultBranch: descriptor.DefaultBranch}
	projects := func(id string) (issueworkflow.Project, bool) { return project, id == descriptor.ID }
	issueService := issueworkflow.New(projects, githubissues.New(nil), issueProjection, memberService)
	issueService.WithComments(issueStore)
	pullRequestStore, err := pullrequestssqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	pullRequestPanelPins, err := pullrequesttrackingsqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	terminalService.panelPins = pullRequestPanelPins
	terminalService.pullRequestCatalog = pullRequestStore
	terminalService.repositoryID = descriptor.ID
	terminalService.logger = applicationLogger
	pullRequestProvider := githubpullrequests.New(githubapi.NewCLIClient())
	pullRequestRepositoryURL := binding.RepositoryURL
	if options.PullRequestRepositoryURL != "" {
		pullRequestRepositoryURL = options.PullRequestRepositoryURL
	}
	pullRequestTransport := pullrequestlifecycle.GitHubTransport(pullRequestProvider)
	if options.PullRequestGitHubTransport != nil {
		pullRequestTransport = options.PullRequestGitHubTransport
	}
	pullRequestProject := pullrequestlifecycle.Project{ID: descriptor.ID, RepositoryURL: pullRequestRepositoryURL, DefaultBranch: descriptor.DefaultBranch, GitHubBacked: pullRequestRepositoryURL != ""}
	refreshScheduler := pullrequestlifecycle.NewRefreshCoordinator()
	pullRequestLifecycle := pullrequestlifecycle.New(pullRequestStore, pullrequestlifecycle.Options{
		OnCreationRecovered: func(ctx context.Context, pr pullrequestlifecycle.PullRequest) {
			pinPullRequestToPanel(ctx, pullRequestPanelPins, applicationLogger, pr.ID, "creation recovery")
		},
		Refresh:    refreshScheduler,
		Projects:   pullrequestlifecycle.ProjectLookupFunc(func(id string) (pullrequestlifecycle.Project, bool) { return pullRequestProject, id == descriptor.ID }),
		Repository: localPullRequestRepository{Service: repositories, scheduler: refreshScheduler}, GitHubTransport: pullRequestTransport, GitHubCodec: pullRequestProvider,
	})
	pullRequestTargets := localPullRequestTargets{catalog: pullRequestStore, provider: pullRequestProvider, repositoryURL: binding.RepositoryURL}
	participantStore, err := pullrequestparticipantssqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	participantService := pullrequestparticipants.NewService(participantStore, pullRequestTargets, memberService, githubprparticipants.New(githubapi.NewCLIClient()))
	browsingStore, err := workitemssqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	pullRequestLifecycle.SetBrowsingSync(participantService, browsingStore)
	issueService.WithSyncRecorder(browsingStore)
	participantService.SetRefreshRequester(func(ctx context.Context, id string) {
		refreshScheduler.Trigger(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: descriptor.ID, PullRequestID: id, Section: "participants"}, func(ctx context.Context) error { _, err := participantService.Reconcile(ctx, id); return err })
	})
	personalSync := &personalWorkSync{repositoryID: descriptor.ID, members: memberService, issues: issueService, lifecycle: pullRequestLifecycle, scheduler: refreshScheduler, state: browsingStore}
	commentStore, err := pullrequestcommentssqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	workStore, err := pullrequestworksqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	commentService := pullrequestcomments.NewService(commentStore, pullRequestTargets,
		pullrequestcomments.WithProviderGateway(githubprcomments.New(githubapi.NewCLIClient()), localCommentAuthors{members: memberService}),
		pullrequestcomments.WithWorkerReferenceReader(workStore),
		pullrequestcomments.WithRefreshScheduler(pullrequestcomments.RefreshSchedulerFunc(func(ctx context.Context, repositoryID, pullRequestID string, work func(context.Context) error) error {
			return refreshScheduler.Do(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: repositoryID, PullRequestID: pullRequestID, Section: "comments"}, work)
		})),
	)
	manualReviewStore, err := pullrequestreviewssqlite.New(ctx, db)
	if err != nil {
		return fail(err)
	}
	manualReviewService := pullrequestreviews.New(manualReviewStore, pullRequestTargets, githubprreviews.New(githubapi.NewCLIClient()))

	rebaseManager, err := repositorybrowser.NewManager(filepath.Join(home, "cache", "repositories"))
	if err != nil {
		return fail(err)
	}
	var directRebase pullrequestwork.PinnedRebaser
	if descriptor.RepositoryURL != "" {
		directRebase = localRebaser{scheduler: refreshScheduler, actions: pullRequestStore, manager: rebaseManager, repository: repositorybrowser.Repository{ID: descriptor.ID, RepositoryURL: descriptor.RepositoryURL, DefaultBranch: descriptor.DefaultBranch, GitDirectory: descriptor.Root}}
	}
	workCatalog := localWorkCatalog{store: pullRequestStore, sync: pullRequestLifecycle}
	var rebasePreview pullrequestwork.PinnedRebasePreviewer
	if local, ok := directRebase.(localRebaser); ok {
		rebasePreview = local
	}
	rebaseCoordinator := pullrequestwork.NewRebaseCoordinator(workCatalog, directRebase, rebasePreview)
	var workLauncher pullrequestwork.Launcher = localWorkLauncher{holons: terminalService, templates: promptTemplateService, comments: commentService, harnesses: agentSettings, works: workStore}
	if options.PullRequestWorkLauncher != nil {
		workLauncher = options.PullRequestWorkLauncher
	}
	workService := pullrequestwork.New(workStore, workCatalog, workLauncher, localReviewComments{comments: commentService}, rebaseCoordinator)
	workService.SetReviewChanges(localReviewChanges{changes: metadatagit.Changes{Path: descriptor.Root}})
	agentState.work = workService
	agentState.workCompletion = localPullRequestWorkCompletionCoordinator{reviewMu: &terminalService.recoveryMu, holons: service, agents: agentService, work: workService, logger: applicationLogger}
	terminalService.work = workService
	bindWorkFinalization(service, workService)
	commitAgents.work = workService
	service.SetRebaseAutoCloseEligibility(func(ctx context.Context, h holons.Holon) (bool, error) {
		return autoCommitEligible(ctx, h, workService)
	})
	workService.SetPublisher(localWorkPublisher{sync: pullRequestLifecycle, holons: terminalService, actions: pullRequestStore})
	workService.SetCompletionCommitter(pullrequestworksqlite.NewCompletionCommitter(workStore, pullRequestStore))
	workService.SetContinuePublicationCommitter(pullrequestworksqlite.NewContinuePublicationCommitter(workStore, pullRequestStore))
	rebaseAgents := &rebaseAgentCoordinator{closer: terminalService, reservations: pullrequestssqlite.NewHolonPublicationCommitter(pullRequestStore, store), sync: pullRequestLifecycle, holons: service, agents: agentService, harnesses: agentSettings, catalog: pullRequestStore, repositoryID: descriptor.ID, templates: promptTemplateService}
	rebaseAgents.pinPullRequests = terminalService.pinHolonPullRequests
	terminalService.rebaseAgents = rebaseAgents
	agentState.rebaseAgents = rebaseAgents
	products.rebaseAgents = rebaseAgents
	service.SetPublicationTargets(rebaseAgents)
	terminalService.actions = pullRequestStore
	addressCompletion := &addressCompletionCoordinator{holons: service, work: workService, rebase: rebaseAgents, agents: agentService}
	agentState.addressCompletion = addressCompletion
	terminalService.workCompletion = &localPullRequestWorkCompletionCoordinator{reviewMu: &terminalService.recoveryMu, holons: service, agents: agentService, work: workService, address: addressCompletion, logger: applicationLogger}
	agentState.workCompletion = terminalService.workCompletion
	terminalService.addressCompletion = addressCompletion
	products.addressCompletion = addressCompletion
	publisher := pullrequestlifecycle.NewHolonPublisher(descriptor.ID, pullRequestStore, service, pullrequestssqlite.NewHolonPublicationCommitter(pullRequestStore, store), rebaseAgents)
	terminalService.publication = publisher
	workService.SetComments(localWorkComments{comments: commentService})
	workService.SetWorkerRuntime(localWorkRuntime{holons: terminalService})
	if err = addressCompletion.Recover(ctx); err != nil {
		return fail(err)
	}
	if err = workService.Recover(ctx, restoringSessions...); err != nil {
		return fail(err)
	}
	metadataStore, err := pullrequestmetadatasqlite.New(ctx, db, pullRequestStore)
	if err != nil {
		return fail(err)
	}
	metadataChanges := metadatagit.Changes{Path: descriptor.Root}
	metadataAgents := pullrequestmetadata.AgentSessions(localMetadataHolons{holons: terminalService, harnesses: agentSettings, changes: metadataChanges})
	if options.PullRequestMetadataAgents != nil {
		metadataAgents = options.PullRequestMetadataAgents
	}
	metadataProvider := pullrequestmetadata.Provider(githubprmetadata.New(githubapi.NewCLIClient()))
	if options.PullRequestMetadataProvider != nil {
		metadataProvider = options.PullRequestMetadataProvider
	}
	metadataService := pullrequestmetadata.New(metadataStore, pullrequestmetadata.Options{CommitMetadata: metadataChanges, Changes: metadataChanges, Provider: metadataProvider, Projection: pullRequestStore, AgentSessions: metadataAgents, Templates: promptTemplateService, Lifecycle: pullrequestmetadata.LifecycleFunc(func(ctx context.Context, id string, target pullrequestmetadata.PreparationTarget) error {
		pr, ok := pullRequestStore.GetPullRequest(id)
		if !ok {
			return pullrequestmetadata.ErrPullRequestNotFound
		}
		_, e := pullRequestLifecycle.Transition(ctx, pr, pullrequestlifecycle.Status(target))
		return e
	})})
	agentState.metadata = metadataService
	pullRequestLifecycle.SetPublicationReadiness(metadataService)
	pullRequestLifecycle.SetOpeningPreparation(localPullRequestPreparation{metadataService})
	runContext, cancel := context.WithCancel(context.Background())
	app := &Application{recoveryDone: make(chan struct{}), Codex: codexManager, agentSettingsDatabase: sharedDB, agents: agentService, Holons: service, Database: db, Manager: manager, IDEs: ideService, RepositoryID: descriptor.ID, PullRequestWork: workService, cancel: func() { stopRepository(); cancel() }, done: make(chan struct{}), lock: instanceLock}
	terminalService.startups = &manualStartupJobs{ctx: runContext, prepare: repositories.PrepareBranch}
	app.manualStartups = terminalService.startups
	app.prRefresh = refreshScheduler
	app.commentPublisherDone = make(chan struct{})
	go func() {
		defer close(app.commentPublisherDone)
		commentService.RunPublisher(runContext, pullrequestcomments.RefreshSchedulerFunc(func(ctx context.Context, repositoryID, pullRequestID string, work func(context.Context) error) error {
			return refreshScheduler.Do(ctx, pullrequestlifecycle.RefreshKey{RepositoryID: repositoryID, PullRequestID: pullRequestID, Section: "comments-publication"}, work)
		}))
	}()
	go func() {
		defer close(app.done)
		var acknowledged []terminals.TerminalID
		for runContext.Err() == nil {
			completions, completionErr := manager.Completions(runContext, acknowledged, time.Second)
			if completionErr != nil {
				return
			}
			for _, completion := range completions {
				cleanup, stop := context.WithTimeout(runContext, 5*time.Second)
				_ = agentService.StopTerminal(cleanup, string(completion.TerminalID))
				stop()
			}
			acknowledged, _ = coordinator.ApplyCompletions(runContext, localTerminalHost, completions)
			recoverMetadataArtifacts(runContext, service, metadataService)
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/agent-capabilities", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		capabilities := agentService.Capabilities(r.Context())
		defaults, currentErr := agentSettings.DefaultsWithCapabilities(r.Context(), capabilities)
		if currentErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "agent_settings_unavailable", "message": "Agent settings are unavailable."})
			return
		}
		current := defaults[agentsettings.WorkflowDefault]
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": capabilities, "default_harness": current.HarnessType, "default_harness_explicit": current.Explicit, "harness_defaults": defaults})
	})
	repositoryHandler := repositoryhttp.New(repositories)
	mux.Handle("/api/v1/repository", repositoryHandler)
	mux.Handle("/api/v1/repository/", repositoryHandler)
	holonsHandler := holonshttp.New(terminalService, holonshttp.Options{RepositoryID: descriptor.ID, RuntimeID: localTerminalHost})
	terminalHandler := terminalshttp.New(coordinator, nil)
	ideHandler := idehttp.New(ideCoordinator)
	register := func(pattern string, handler http.HandlerFunc) { mux.HandleFunc(pattern, handler) }
	prompttemplateshttp.RegisterRoutes(register, promptTemplateService)
	agentsettingshttp.RegisterRoutes(register, agentSettings)
	harnesshttp.RegisterModels(register, registry, descriptor.Root)
	agentsettingshttp.RegisterCommandRoutes(register, launchCommands)
	workitemshttp.RegisterRoutes(register, workitems.New(browsingStore, descriptor.ID), personalSync)
	issueshttp.RegisterRoutes(register, issueshttp.Options{Workflow: issueService, RepositoryID: descriptor.ID})
	attachmentshttp.RegisterRoutes(register, attachments.New(binding.RepositoryURL, githubattachments.New(githubapi.NewCLIClient(), nil)))
	memberProject := githubidentityhttp.Project{ID: descriptor.ID, RepositoryURL: binding.RepositoryURL}
	githubidentityhttp.RegisterRoutes(register, githubidentityhttp.Options{Service: memberService, Project: &memberProject})
	register("GET /api/v1/github-profile", githubidentityhttp.NewProfileHandler(memberService))
	issueHolonWorkflow := issueholons.New(issueService, localIssueRepository{repositories}, terminalService, promptTemplateService)
	mux.Handle("POST /api/v1/issues/{id}/start-agent", issueholonshttp.New(issueHolonWorkflow))
	pullRequestHolons := localPullRequestHolons{service}
	pullRequestBranchPublisher := pullrequestlifecycle.BranchPublisher(repositories)
	if options.PullRequestBranchPublisher != nil {
		pullRequestBranchPublisher = options.PullRequestBranchPublisher
	}
	pullRequestCreator := pinningPullRequestCreator{
		creator: pullrequestlifecycle.NewCreationCoordinator(pullRequestStore, pullRequestHolons, pullRequestBranchPublisher, pullRequestLifecycle, localPullRequestRepository{Service: repositories, scheduler: refreshScheduler}),
		pins:    pullRequestPanelPins, logger: applicationLogger,
	}
	mergeProvider := pullrequestmerge.GitHubProvider(pullRequestProvider)
	if options.PullRequestMergeProvider != nil {
		mergeProvider = options.PullRequestMergeProvider
	}
	pullRequestMerger := pullrequestmerge.New(pullrequestmerge.Options{Sync: pullRequestLifecycle, RepositoryURL: binding.RepositoryURL, Store: pullRequestStore, Repository: localPullRequestMergeRepository{Service: repositories, scheduler: refreshScheduler}, GitHub: mergeProvider, Comments: commentService})
	pullRequestStore.SetActionCompletionHook(func(id string) { pullRequestLifecycle.RequestPullRequestRefresh(runContext, id) })
	pullRequestStore.SetPublicationCompletionHook(func(id string) { pullRequestLifecycle.RequestPublicationRefresh(runContext, id) })
	retirement := &localPullRequestRetirement{catalog: pullRequestStore, holons: service, links: localPullRequestHolonLinks{catalog: pullRequestStore, holons: service}, metadata: metadataService, work: workService}
	pullRequestLifecycle.SetRetirement(retirement)
	app.dispatcherDone = make(chan struct{})
	go func() {
		defer close(app.dispatcherDone)
		workService.RunDispatcher(runContext)
	}()
	app.prRefresh = refreshScheduler
	pullrequestshttp.RegisterRoutes(register, pullrequestshttp.Options{RepositoryID: descriptor.ID, Catalog: pullRequestStore, Lifecycle: pullRequestLifecycle, PublicationReadiness: metadataService, Merge: pullRequestMerger, Repository: repositories, Holons: pullRequestHolons, HolonLinks: localPullRequestHolonLinks{catalog: pullRequestStore, holons: service}, Creator: pullRequestCreator, ContinuePublisher: workService, Participants: participantService, Comments: commentService, PanelPins: pullRequestPanelPins, Retirement: retirement, RetirementScheduler: pullRequestLifecycle})
	pullrequestcommentshttp.RegisterRoutes(register, commentService, pullRequestTargets)
	pullrequestreviewshttp.RegisterRoutes(register, manualReviewService)
	pullrequestparticipantshttp.RegisterRoutes(register, participantService)
	pullrequestmetadatahttp.RegisterRoutes(register, metadataService)
	pullrequestworkhttp.RegisterRoutes(register, workService)
	mux.Handle("GET /api/v1/pull-requests/{id}/holon-activity", holonsHandler)
	mux.Handle("/api/v1/holons", holonsHandler)
	mux.Handle("/api/v1/holons/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/ides") {
			ideHandler.ServeHTTP(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/attach") {
			terminalHandler.ServeHTTP(w, r)
			return
		}
		holonsHandler.ServeHTTP(w, r)
	}))
	app.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
			r = r.WithContext(pullrequestlifecycle.WithRequestID(r.Context(), requestID))
		}
		mux.ServeHTTP(w, r)
	})
	go func() {
		defer close(app.recoveryDone)
		if err := processRegistry.Recover(); err != nil {
			log.Printf("recover previous processes: %v", err)
		}
		terminalService.restoreAgents(runContext)
	}()
	if pullRequestProject.GitHubBacked {
		go func() {
			if err := refreshScheduler.Do(runContext, pullrequestlifecycle.RefreshKey{RepositoryID: descriptor.ID, Section: "recovery"}, pullRequestLifecycle.RecoverOperations); err != nil && runContext.Err() == nil {
				log.Printf("Pull request operation startup recovery: %v", err)
			}
			pullRequestLifecycle.RunBasic(runContext, descriptor.ID)
		}()
	}
	go recoverMetadataArtifacts(runContext, service, metadataService)
	go func() {
		refreshContext, stop := context.WithTimeout(runContext, 30*time.Second)
		defer stop()
		_, _ = repositories.Refresh(refreshContext)
	}()
	if binding.RepositoryURL != "" {
		go personalSync.run(runContext, "identity", 5*time.Minute)
		go personalSync.run(runContext, "issues", openIssueRefreshInterval)
		go func() {
			syncContext, stop := context.WithTimeout(runContext, 30*time.Second)
			defer stop()
			if _, e := memberService.SyncProjectMembers(syncContext, descriptor.ID, binding.RepositoryURL); e != nil {
				log.Printf("GitHub member startup sync: %v", e)
			}
		}()
	}
	opened = true
	return app, nil
}

func (app *Application) Close() error {
	if app == nil {
		return nil
	}
	app.cancel()
	if app.manualStartups != nil {
		app.manualStartups.Close()
	}
	if app.dispatcherDone != nil {
		<-app.dispatcherDone
	}
	if app.prRefresh != nil {
		app.prRefresh.Close()
	}
	if app.commentPublisherDone != nil {
		<-app.commentPublisherDone
	}
	if app.recoveryDone != nil {
		<-app.recoveryDone
	}
	app.agents.Close()
	codexErr := app.Codex.Close()
	ideErr := app.IDEs.Shutdown()
	app.Manager.Close()
	<-app.done
	databaseErr := app.Database.Close()
	if app.agentSettingsDatabase != nil {
		databaseErr = errors.Join(databaseErr, app.agentSettingsDatabase.Close())
	}
	return errors.Join(codexErr, ideErr, databaseErr, app.lock.Close())
}
