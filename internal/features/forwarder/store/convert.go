package store

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
	"github.com/nhirsama/yukibot/internal/storage/dbsql"
)

func timestamptz(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func optString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optTopic(has bool, topic int) *int64 {
	if !has {
		return nil
	}
	value := int64(topic)
	return &value
}

func topicFrom(value *int64) (int, bool) {
	if value == nil {
		return 0, false
	}
	return int(*value), true
}

func optPoll(source forwarder.SourceEndpoint) *int32 {
	if !source.Polled {
		return nil
	}
	seconds := int32(source.PollEvery / time.Second)
	return &seconds
}

type filterJSON struct {
	Keywords               []string `json:"keywords"`
	AllowedContentTypes    []string `json:"allowed_content_types"`
	BlockedContentTypes    []string `json:"blocked_content_types"`
	IncludeServiceMessages bool     `json:"include_service_messages"`
}

func encodeFilter(filter forwarder.MessageFilter) (json.RawMessage, error) {
	payload := filterJSON{
		Keywords:               append([]string(nil), filter.Keywords...),
		AllowedContentTypes:    contentNames(filter.Allowed),
		BlockedContentTypes:    contentNames(filter.Blocked),
		IncludeServiceMessages: filter.IncludeService,
	}
	if payload.Keywords == nil {
		payload.Keywords = []string{}
	}
	return json.Marshal(payload)
}

func decodeFilter(raw json.RawMessage) (forwarder.MessageFilter, error) {
	var payload filterJSON
	if err := json.Unmarshal(raw, &payload); err != nil {
		return forwarder.MessageFilter{}, err
	}
	return forwarder.NewMessageFilter(payload.Keywords, contentTypes(payload.AllowedContentTypes), contentTypes(payload.BlockedContentTypes), payload.IncludeServiceMessages), nil
}

func contentNames(values []contracts.ContentType) []string {
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = string(value)
	}
	sort.Strings(names)
	return names
}

func contentTypes(names []string) []contracts.ContentType {
	out := make([]contracts.ContentType, len(names))
	for i, name := range names {
		out[i] = contracts.ContentType(name)
	}
	return out
}

func routeParams(route forwarder.Route) (dbsql.InsertRouteParams, error) {
	filter, err := encodeFilter(route.Filter)
	if err != nil {
		return dbsql.InsertRouteParams{}, err
	}
	return dbsql.InsertRouteParams{
		SourceChatID:        route.Source.ChatID,
		SourceTopicID:       optTopic(route.Source.HasTopic, route.Source.TopicID),
		DestinationChatID:   route.Destination.ChatID,
		DestinationTopicID:  optTopic(route.Destination.HasTopic, route.Destination.TopicID),
		Mode:                string(route.Mode),
		FilterJson:          filter,
		Enabled:             route.Enabled,
		FallbackToCopy:      route.FallbackToCopy,
		SourceUsername:      optString(route.Source.Username),
		DestinationUsername: optString(route.Destination.Username),
		PollIntervalSeconds: optPoll(route.Source),
	}, nil
}

func routeFromRow(row dbsql.ForwarderRoute) (forwarder.Route, error) {
	filter, err := decodeFilter(row.FilterJson)
	if err != nil {
		return forwarder.Route{}, err
	}
	sourceTopic, hasSourceTopic := topicFrom(row.SourceTopicID)
	destTopic, hasDestTopic := topicFrom(row.DestinationTopicID)
	var sourceCfg forwarder.SourceConfig
	sourceCfg.Username = derefString(row.SourceUsername)
	if hasSourceTopic {
		sourceCfg.TopicID = &sourceTopic
	}
	if row.PollIntervalSeconds != nil {
		sourceCfg.Polled = true
		sourceCfg.PollEvery = time.Duration(*row.PollIntervalSeconds) * time.Second
	}
	source, err := forwarder.NewSourceEndpoint(row.SourceChatID, sourceCfg)
	if err != nil {
		return forwarder.Route{}, err
	}
	var destCfg forwarder.DestinationConfig
	destCfg.Username = derefString(row.DestinationUsername)
	if hasDestTopic {
		destCfg.TopicID = &destTopic
	}
	destination, err := forwarder.NewDestinationEndpoint(row.DestinationChatID, destCfg)
	if err != nil {
		return forwarder.Route{}, err
	}
	return forwarder.NewRouteWith(int(row.ID), source, destination, filter, forwarder.ForwardMode(row.Mode), row.Enabled, row.FallbackToCopy)
}

func withRouteID(id int, params dbsql.InsertRouteParams) dbsql.InsertRouteWithIDParams {
	return dbsql.InsertRouteWithIDParams{
		ID:                  int64(id),
		SourceChatID:        params.SourceChatID,
		SourceTopicID:       params.SourceTopicID,
		DestinationChatID:   params.DestinationChatID,
		DestinationTopicID:  params.DestinationTopicID,
		Mode:                params.Mode,
		FilterJson:          params.FilterJson,
		Enabled:             params.Enabled,
		FallbackToCopy:      params.FallbackToCopy,
		SourceUsername:      params.SourceUsername,
		DestinationUsername: params.DestinationUsername,
		PollIntervalSeconds: params.PollIntervalSeconds,
	}
}

func updateParams(route forwarder.Route, params dbsql.InsertRouteParams) dbsql.UpdateRouteParams {
	return dbsql.UpdateRouteParams{
		ID:                  int64(route.ID),
		SourceChatID:        params.SourceChatID,
		SourceTopicID:       params.SourceTopicID,
		DestinationChatID:   params.DestinationChatID,
		DestinationTopicID:  params.DestinationTopicID,
		Mode:                params.Mode,
		FilterJson:          params.FilterJson,
		Enabled:             params.Enabled,
		FallbackToCopy:      params.FallbackToCopy,
		SourceUsername:      params.SourceUsername,
		DestinationUsername: params.DestinationUsername,
		PollIntervalSeconds: params.PollIntervalSeconds,
	}
}

func fitInt(value int64, name string) (int, error) {
	converted := int(value)
	if int64(converted) != value {
		return 0, forwarder.NewValueError(name + " does not fit in an int: " + strconv.FormatInt(value, 10))
	}
	return converted, nil
}
