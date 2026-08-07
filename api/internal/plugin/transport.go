package plugin

import (
	"context"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/osctf/platform/internal/plugin/pluginpb"
)

// Dispense keys — one per plugin type. A plugin serves exactly the one its manifest `type`
// declares; the host dispenses that key and receives the matching gRPC client.
const (
	KeyAuth          = "auth"
	KeyScoring       = "scoring"
	KeyNotification  = "notification"
	KeyChallengeType = "challenge_type"
)

// HostPluginSet is the set the host offers when dialing a plugin. Each entry's Impl is nil on
// the host side; Dispense returns the generated gRPC client. The SDK builds the mirror set
// with Impls set (the plugin author's implementation).
func HostPluginSet() goplugin.PluginSet {
	return goplugin.PluginSet{
		KeyAuth:          &AuthGRPCPlugin{},
		KeyScoring:       &ScoringGRPCPlugin{},
		KeyNotification:  &NotificationGRPCPlugin{},
		KeyChallengeType: &ChallengeTypeGRPCPlugin{},
	}
}

// Each *GRPCPlugin bridges go-plugin <-> the generated stubs: GRPCServer registers the
// plugin author's impl (plugin side), GRPCClient returns the generated client (host side).

// AuthGRPCPlugin bridges the Auth service.
type AuthGRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	Impl pluginpb.AuthServer
}

func (p *AuthGRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	pluginpb.RegisterAuthServer(s, p.Impl)
	return nil
}
func (p *AuthGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return pluginpb.NewAuthClient(c), nil
}

// ScoringGRPCPlugin bridges the Scoring service.
type ScoringGRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	Impl pluginpb.ScoringServer
}

func (p *ScoringGRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	pluginpb.RegisterScoringServer(s, p.Impl)
	return nil
}
func (p *ScoringGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return pluginpb.NewScoringClient(c), nil
}

// NotificationGRPCPlugin bridges the Notification service.
type NotificationGRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	Impl pluginpb.NotificationServer
}

func (p *NotificationGRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	pluginpb.RegisterNotificationServer(s, p.Impl)
	return nil
}
func (p *NotificationGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return pluginpb.NewNotificationClient(c), nil
}

// ChallengeTypeGRPCPlugin bridges the ChallengeType service.
type ChallengeTypeGRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	Impl pluginpb.ChallengeTypeServer
}

func (p *ChallengeTypeGRPCPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	pluginpb.RegisterChallengeTypeServer(s, p.Impl)
	return nil
}
func (p *ChallengeTypeGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return pluginpb.NewChallengeTypeClient(c), nil
}
