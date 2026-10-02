package bootstrap

import (
	"os"

	"github.com/nhirsama/yukibot/internal/adapters/database"
	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/config"
	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
	fwdstore "github.com/nhirsama/yukibot/internal/features/forwarder/infra"
	fwdrepo "github.com/nhirsama/yukibot/internal/features/forwarder/store"
	"github.com/nhirsama/yukibot/internal/features/management"
	mgmtstore "github.com/nhirsama/yukibot/internal/features/management/store"
	"github.com/nhirsama/yukibot/internal/features/summarizer"
	suminfra "github.com/nhirsama/yukibot/internal/features/summarizer/infra"
	sumstore "github.com/nhirsama/yukibot/internal/features/summarizer/store"
	"github.com/nhirsama/yukibot/internal/kernel"
	"github.com/nhirsama/yukibot/internal/observability"
)

// Build wires the process without opening PostgreSQL or starting Telegram.
// The first constructor error is returned with nothing left running.
func Build(settings config.Settings) (*kernel.Application, error) {
	app, _, err := assemble(settings)
	return app, err
}

// assemble wires the process and keeps the Telegram client for in-process checks.
func assemble(settings config.Settings) (*kernel.Application, *telegram.Client, error) {
	logger := observability.NewLogger(settings.LogLevel, os.Stdout)
	migrations := make([]contracts.Migration, 0, len(forwarder.Migrations)+len(management.Migrations)+len(summarizer.SummarizerMigrations))
	migrations = append(migrations, forwarder.Migrations...)
	migrations = append(migrations, management.Migrations...)
	migrations = append(migrations, summarizer.SummarizerMigrations...)
	databaseFeature, err := database.NewFeature(settings.DatabaseURL, migrations)
	if err != nil {
		return nil, nil, err
	}
	db := databaseFeature.DB()

	identity := &telegram.AccountIdentity{}
	client := telegram.NewClient(settings.TelegramAPIID, settings.TelegramAPIHash, settings.TelegramSessionPath, telegram.NewRequestLimiter(), identity)
	bus := busAdapter{bus: kernel.NewEventBus(logger)}
	supervisor := kernel.NewTaskSupervisor(logger)
	stream, err := kernel.NewMessageStream(256, settings.ShutdownTimeout, supervisor, logger)
	if err != nil {
		return nil, nil, err
	}
	registry := kernel.NewCommandRegistry()

	forwardRepository := fwdrepo.NewRepository(db)
	gateway := fwdstore.New(client)
	topics := forwarder.NewManagedTopicService(fwdrepo.Topics{Repository: forwardRepository}, gateway)
	syncDeletes := false
	service := forwarder.NewForwarderService(
		fwdrepo.Routes{Repository: forwardRepository},
		fwdrepo.Links{Repository: forwardRepository},
		gateway,
		forwarder.ForwarderOptions{SyncDeletes: &syncDeletes},
		topics,
	)
	forwardManagement, err := forwarder.NewForwarderManagementService(fwdrepo.Routes{Repository: forwardRepository}, forwarder.ForwarderManagementConfig{
		Topics:   topics,
		Sources:  gateway,
		Cursors:  fwdrepo.Cursors{Repository: forwardRepository},
		Accesses: fwdrepo.Access{Repository: forwardRepository},
	})
	if err != nil {
		return nil, nil, err
	}
	rebuilder, err := forwarder.NewMembershipRebuilder(gateway, forwarder.MembershipRebuilderConfig{
		MinInterval: settings.RebuildJoinMinInterval,
		MaxInterval: settings.RebuildJoinMaxInterval,
		Logger:      logger,
	})
	if err != nil {
		return nil, nil, err
	}
	recovery := forwarder.NewMembershipRecoveryService(
		fwdrepo.Routes{Repository: forwardRepository},
		fwdrepo.Access{Repository: forwardRepository},
		gateway,
		rebuilder,
	)
	commands, err := forwarder.NewForwarderCommands(forwardManagement, recovery)
	if err != nil {
		return nil, nil, err
	}
	runner, err := forwarder.NewForwardJobRunner(fwdrepo.Jobs{Repository: forwardRepository}, forwarder.NewForwardJobProcessor(service), forwarder.ForwardJobRunnerConfig{Logger: logger})
	if err != nil {
		return nil, nil, err
	}
	poller, err := forwarder.NewSourcePoller(
		fwdrepo.Routes{Repository: forwardRepository},
		fwdrepo.Cursors{Repository: forwardRepository},
		gateway,
		queuedPublisher{busAdapter: bus, stream: stream, origin: contracts.OriginHistory},
		forwarder.SourcePollerConfig{Logger: logger},
	)
	if err != nil {
		return nil, nil, err
	}
	forwarderFeature, err := forwarder.NewForwarderFeature(bus, runner, forwarderTasks{supervisor: supervisor}, forwarder.ForwarderFeatureConfig{
		Commands:    forwarderCommands{registry: registry},
		Handler:     commands.Handle,
		AlbumDelay:  settings.ForwarderAlbumDelay,
		StopTimeout: settings.ShutdownTimeout,
		Logger:      logger,
		Poller:      poller,
		Rebuilder:   rebuilder,
	})
	if err != nil {
		return nil, nil, err
	}

	summaryRepository := sumstore.NewRepository(db)
	summaryGateway := suminfra.New(client)
	generator := summarizer.NewOpenAISummaryGenerator()
	summaryService, err := summarizer.NewSummarizerService(summaryRepository, summaryRepository, summaryRepository, summaryGateway, generator)
	if err != nil {
		return nil, nil, err
	}
	summaryLifecycle := summarizer.NewLifecycle(summaryCommands{registry: registry}, summarizer.NewSummarizerCommands(summaryService).Handle, generator.Reset)

	managementRepository := mgmtstore.NewRepository(db, identity)
	modules, err := kernel.NewModuleController([]kernel.Feature{forwarderFeature, summaryLifecycle}, managementRepository)
	if err != nil {
		return nil, nil, err
	}
	managementService := management.NewService(managementRepository, moduleAdapter{modules: modules}, identity)
	authorizer := management.NewAuthorizer(managementRepository, identity)
	dispatcher := kernel.NewCommandDispatcher(registry, authorizer, managementRepository, logger)
	router := telegram.NewCommandRouter(commandPlane{dispatcher: dispatcher}, telegram.NewCommandReplySender(client), logger)
	if err := stream.Subscribe("control", router.HandleEvent); err != nil {
		return nil, nil, err
	}
	if err := stream.Subscribe("features", distributeMessages(bus.bus)); err != nil {
		return nil, nil, err
	}
	eventSource := telegram.NewEventSource(client,
		queuedPublisher{busAdapter: bus, stream: stream, origin: contracts.OriginLive},
		telegramTasks{supervisor: supervisor}, settings.ShutdownTimeout)

	lifecycle, err := kernel.NewLifecycleManager([]kernel.Feature{
		databaseFeature,
		client,
		kernel.NewSupervisorLifecycle(supervisor, settings.ShutdownTimeout),
		management.NewFeature(registry, management.NewCommands(managementService)),
		modules,
		stream,
		eventSource,
	}, logger)
	if err != nil {
		return nil, nil, err
	}
	return kernel.NewApplication(lifecycle, supervisor, kernel.NewShutdownCoordinator(), logger), client, nil
}
