package forwarder

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

// ForwarderManagementConfig supplies optional route-management dependencies.
type ForwarderManagementConfig struct {
	Topics   *ManagedTopicService
	Sources  TelegramSourceGateway
	Cursors  PollCursorRepository
	Accesses ChatAccessRepository
}

// ForwarderManagementService adds, replaces, and prepares forwarding routes.
type ForwarderManagementService struct {
	routes   RouteRepository
	topics   *ManagedTopicService
	sources  TelegramSourceGateway
	cursors  PollCursorRepository
	accesses ChatAccessRepository
	addMu    sync.Mutex
}

// NewForwarderManagementService returns the route-management use case.
func NewForwarderManagementService(routes RouteRepository, cfg ForwarderManagementConfig) (*ForwarderManagementService, error) {
	if routes == nil {
		return nil, valueErr("route repository is required")
	}
	return &ForwarderManagementService{
		routes:   routes,
		topics:   cfg.Topics,
		sources:  cfg.Sources,
		cursors:  cfg.Cursors,
		accesses: cfg.Accesses,
	}, nil
}

// ResolveChat resolves a reference through the source gateway, or parses a numeric ID.
func (s *ForwarderManagementService) ResolveChat(ctx context.Context, reference string) (ChatIdentity, error) {
	if err := ctx.Err(); err != nil {
		return ChatIdentity{}, err
	}
	if s.sources != nil {
		return s.sources.ResolveChat(ctx, reference)
	}
	chatID, err := strconv.ParseInt(reference, 10, 64)
	if err != nil {
		return ChatIdentity{}, valueErr("chat reference must be a numeric ID")
	}
	return NewChatIdentityOptional(chatID, "", "")
}

// ListRoutes returns every stored route.
func (s *ForwarderManagementService) ListRoutes(ctx context.Context) ([]Route, error) {
	return s.routes.ListAll(ctx)
}

// RememberChatAccesses stores join metadata for the resolved endpoints.
func (s *ForwarderManagementService) RememberChatAccesses(ctx context.Context, identities []ChatIdentity) error {
	if s.accesses == nil {
		return nil
	}
	for _, identity := range identities {
		title := ""
		if s.sources != nil {
			if value, ok := s.sources.ChatTitle(identity.ChatID); ok {
				title = value
			}
		}
		link := identity.InviteLink
		if link == "" && identity.Username != "" {
			link = "https://t.me/" + identity.Username
		}
		if err := s.accesses.Save(ctx, ChatAccess{
			ChatID:     identity.ChatID,
			Title:      title,
			Username:   identity.Username,
			InviteLink: link,
		}); err != nil {
			return err
		}
	}
	return nil
}

// RouteTitles returns the source and destination titles, or empty strings.
func (s *ForwarderManagementService) RouteTitles(route Route) (string, string) {
	if s.sources == nil {
		return "", ""
	}
	source, _ := s.sources.ChatTitle(route.Source.ChatID)
	destination, _ := s.sources.ChatTitle(route.Destination.ChatID)
	return source, destination
}

// GetRoute returns one route or RouteNotFoundError.
func (s *ForwarderManagementService) GetRoute(ctx context.Context, routeID int) (Route, error) {
	routes, err := s.routes.ListAll(ctx)
	if err != nil {
		return Route{}, err
	}
	for _, route := range routes {
		if route.ID == routeID {
			return route, nil
		}
	}
	return Route{}, RouteNotFoundError{msg: fmt.Sprintf("route %d does not exist", routeID)}
}

// AddRoute stores a new route. The same configuration is idempotent.
func (s *ForwarderManagementService) AddRoute(ctx context.Context, route Route) (Route, error) {
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	routes, err := s.routes.ListAll(ctx)
	if err != nil {
		return Route{}, err
	}
	for _, existing := range routes {
		if existing.ID != route.ID {
			continue
		}
		if existing.Equal(route) {
			if err := s.prepareSource(ctx, existing); err != nil {
				return Route{}, err
			}
			if err := s.prepareTopic(ctx, existing); err != nil {
				return Route{}, err
			}
			return existing, nil
		}
		return Route{}, valueErr(fmt.Sprintf("route %d already exists with different configuration", route.ID))
	}
	if err := s.prepareSource(ctx, route); err != nil {
		return Route{}, err
	}
	if err := s.routes.Add(ctx, route); err != nil {
		return Route{}, err
	}
	if err := s.prepareTopic(ctx, route); err != nil {
		return Route{}, err
	}
	return route, nil
}

// AddGeneratedRoute allocates an ID, or returns the route that already matches the draft.
func (s *ForwarderManagementService) AddGeneratedRoute(ctx context.Context, draft RouteDraft) (Route, error) {
	s.addMu.Lock()
	defer s.addMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	routes, err := s.routes.ListAll(ctx)
	if err != nil {
		return Route{}, err
	}
	for _, existing := range routes {
		if !draft.Matches(existing) {
			continue
		}
		if err := s.prepareSource(ctx, existing); err != nil {
			return Route{}, err
		}
		if err := s.prepareTopic(ctx, existing); err != nil {
			return Route{}, err
		}
		return existing, nil
	}
	provisional, err := draft.Bind(1)
	if err != nil {
		return Route{}, err
	}
	if err := s.prepareSource(ctx, provisional); err != nil {
		return Route{}, err
	}
	route, err := s.routes.AddAuto(ctx, draft)
	if err != nil {
		return Route{}, err
	}
	if err := s.prepareTopic(ctx, route); err != nil {
		return Route{}, err
	}
	return route, nil
}

// ReplaceRoute updates an existing route and prepares its source and topic.
func (s *ForwarderManagementService) ReplaceRoute(ctx context.Context, route Route) (Route, error) {
	if _, err := s.GetRoute(ctx, route.ID); err != nil {
		return Route{}, err
	}
	if err := s.prepareSource(ctx, route); err != nil {
		return Route{}, err
	}
	if err := s.routes.Replace(ctx, route); err != nil {
		var missing KeyError
		if errors.As(err, &missing) {
			return Route{}, RouteNotFoundError{msg: fmt.Sprintf("route %d does not exist", route.ID)}
		}
		return Route{}, err
	}
	if err := s.prepareTopic(ctx, route); err != nil {
		return Route{}, err
	}
	return route, nil
}

// SetEnabled enables or disables one route.
func (s *ForwarderManagementService) SetEnabled(ctx context.Context, routeID int, enabled bool) (Route, error) {
	route, err := s.GetRoute(ctx, routeID)
	if err != nil {
		return Route{}, err
	}
	route.Enabled = enabled
	if enabled {
		if err := s.prepareSource(ctx, route); err != nil {
			return Route{}, err
		}
	}
	if err := s.routes.Replace(ctx, route); err != nil {
		return Route{}, err
	}
	if err := s.prepareTopic(ctx, route); err != nil {
		return Route{}, err
	}
	return route, nil
}

// RemoveRoute deletes a route. A missing route is not an error.
func (s *ForwarderManagementService) RemoveRoute(ctx context.Context, routeID int) error {
	_, err := s.routes.Remove(ctx, routeID)
	return err
}

func (s *ForwarderManagementService) prepareTopic(ctx context.Context, route Route) error {
	if !route.Enabled || s.topics == nil {
		return nil
	}
	var title *string
	if s.sources != nil {
		value, ok, err := s.sources.SourceTitle(ctx, route.Source)
		if err != nil {
			return err
		}
		if ok {
			title = &value
		}
	}
	_, err := s.topics.Resolve(ctx, route, title)
	return err
}

func (s *ForwarderManagementService) prepareSource(ctx context.Context, route Route) error {
	if !route.Enabled || s.sources == nil {
		return nil
	}
	if err := s.sources.EnsureSource(ctx, route.Source, !route.Source.IsPolled()); err != nil {
		return err
	}
	if !route.Source.IsPolled() || s.cursors == nil {
		return nil
	}
	if _, ok, err := s.cursors.Get(ctx, route.Source.ChatID); err != nil || ok {
		return err
	}
	latest, err := s.sources.LatestMessageID(ctx, route.Source)
	if err != nil {
		return err
	}
	cursor, err := NewPollCursor(route.Source.ChatID, latest)
	if err != nil {
		return err
	}
	return s.cursors.Save(ctx, cursor)
}
