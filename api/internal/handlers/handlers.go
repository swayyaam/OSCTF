// Package handlers implements the generated strict-server interface. It maps
// HTTP requests to service calls and domain errors back to problem+json; it holds
// no business logic. Methods below are replaced milestone by milestone; anything
// still unimplemented returns ErrNotImplemented, rendered as a 501.
package handlers

import (
	"context"
	"errors"

	"github.com/osctf/platform/internal/apigen"
)

// ErrNotImplemented marks an endpoint whose milestone has not landed yet.
// The HTTP error handler renders it as a 501 problem.
var ErrNotImplemented = errors.New("not implemented")

// Server implements apigen.StrictServerInterface. Dependencies are added as
// milestones land (services are injected here by the composition root).
type Server struct{}

// New builds the handler set.
func New() *Server { return &Server{} }

var _ apigen.StrictServerInterface = (*Server)(nil)

func (s *Server) AdminListChallenges(ctx context.Context, request apigen.AdminListChallengesRequestObject) (apigen.AdminListChallengesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminCreateChallenge(ctx context.Context, request apigen.AdminCreateChallengeRequestObject) (apigen.AdminCreateChallengeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminDeleteChallenge(ctx context.Context, request apigen.AdminDeleteChallengeRequestObject) (apigen.AdminDeleteChallengeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminGetChallenge(ctx context.Context, request apigen.AdminGetChallengeRequestObject) (apigen.AdminGetChallengeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminUpdateChallenge(ctx context.Context, request apigen.AdminUpdateChallengeRequestObject) (apigen.AdminUpdateChallengeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminUploadAttachment(ctx context.Context, request apigen.AdminUploadAttachmentRequestObject) (apigen.AdminUploadAttachmentResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminDeleteAttachment(ctx context.Context, request apigen.AdminDeleteAttachmentRequestObject) (apigen.AdminDeleteAttachmentResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminDestroyInstance(ctx context.Context, request apigen.AdminDestroyInstanceRequestObject) (apigen.AdminDestroyInstanceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminGetInstance(ctx context.Context, request apigen.AdminGetInstanceRequestObject) (apigen.AdminGetInstanceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminDeployInstance(ctx context.Context, request apigen.AdminDeployInstanceRequestObject) (apigen.AdminDeployInstanceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminGetInstanceLogs(ctx context.Context, request apigen.AdminGetInstanceLogsRequestObject) (apigen.AdminGetInstanceLogsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminRestartInstance(ctx context.Context, request apigen.AdminRestartInstanceRequestObject) (apigen.AdminRestartInstanceResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminGetEvent(ctx context.Context, request apigen.AdminGetEventRequestObject) (apigen.AdminGetEventResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminUpdateEvent(ctx context.Context, request apigen.AdminUpdateEventRequestObject) (apigen.AdminUpdateEventResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminGetStats(ctx context.Context, request apigen.AdminGetStatsRequestObject) (apigen.AdminGetStatsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminListSubmissions(ctx context.Context, request apigen.AdminListSubmissionsRequestObject) (apigen.AdminListSubmissionsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminListTeams(ctx context.Context, request apigen.AdminListTeamsRequestObject) (apigen.AdminListTeamsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminUpdateTeam(ctx context.Context, request apigen.AdminUpdateTeamRequestObject) (apigen.AdminUpdateTeamResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminListUsers(ctx context.Context, request apigen.AdminListUsersRequestObject) (apigen.AdminListUsersResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminUpdateUser(ctx context.Context, request apigen.AdminUpdateUserRequestObject) (apigen.AdminUpdateUserResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) AdminResetPassword(ctx context.Context, request apigen.AdminResetPasswordRequestObject) (apigen.AdminResetPasswordResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) Login(ctx context.Context, request apigen.LoginRequestObject) (apigen.LoginResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) Logout(ctx context.Context, request apigen.LogoutRequestObject) (apigen.LogoutResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) GetMe(ctx context.Context, request apigen.GetMeRequestObject) (apigen.GetMeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) ChangePassword(ctx context.Context, request apigen.ChangePasswordRequestObject) (apigen.ChangePasswordResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) Register(ctx context.Context, request apigen.RegisterRequestObject) (apigen.RegisterResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) ListChallenges(ctx context.Context, request apigen.ListChallengesRequestObject) (apigen.ListChallengesResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) GetChallenge(ctx context.Context, request apigen.GetChallengeRequestObject) (apigen.GetChallengeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) DownloadAttachment(ctx context.Context, request apigen.DownloadAttachmentRequestObject) (apigen.DownloadAttachmentResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) SubmitFlag(ctx context.Context, request apigen.SubmitFlagRequestObject) (apigen.SubmitFlagResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) GetEvent(ctx context.Context, request apigen.GetEventRequestObject) (apigen.GetEventResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) GetScoreboard(ctx context.Context, request apigen.GetScoreboardRequestObject) (apigen.GetScoreboardResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) ListTeams(ctx context.Context, request apigen.ListTeamsRequestObject) (apigen.ListTeamsResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) CreateTeam(ctx context.Context, request apigen.CreateTeamRequestObject) (apigen.CreateTeamResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) JoinTeam(ctx context.Context, request apigen.JoinTeamRequestObject) (apigen.JoinTeamResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) LeaveTeam(ctx context.Context, request apigen.LeaveTeamRequestObject) (apigen.LeaveTeamResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) GetTeam(ctx context.Context, request apigen.GetTeamRequestObject) (apigen.GetTeamResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) RenameTeam(ctx context.Context, request apigen.RenameTeamRequestObject) (apigen.RenameTeamResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) RegenerateInviteCode(ctx context.Context, request apigen.RegenerateInviteCodeRequestObject) (apigen.RegenerateInviteCodeResponseObject, error) {
	return nil, ErrNotImplemented
}

func (s *Server) GetUser(ctx context.Context, request apigen.GetUserRequestObject) (apigen.GetUserResponseObject, error) {
	return nil, ErrNotImplemented
}
