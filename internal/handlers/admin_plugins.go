package handlers

import (
	"context"
	"errors"

	"github.com/swayyaam/OSCTF/internal/apigen"
	"github.com/swayyaam/OSCTF/internal/apperr"
	"github.com/swayyaam/OSCTF/internal/plugin"
)

// AdminListPlugins returns every tracked plugin and its lifecycle state — including plugins
// QUARANTINED at load (e.g. invalid config), so an organizer can see WHY a plugin isn't working
// without reading boot logs. The Reason is redacted of secret config values at the source (the
// loader's resolve path), so this handler forwards it verbatim.
func (s *Server) AdminListPlugins(ctx context.Context, _ apigen.AdminListPluginsRequestObject) (apigen.AdminListPluginsResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	var list []apigen.AdminPluginStatus
	if s.d.PluginSnapshot != nil {
		for _, p := range s.d.PluginSnapshot() {
			ps := apigen.AdminPluginStatus{Name: p.Name, Type: p.Type, State: p.State}
			if p.Reason != "" {
				r := p.Reason
				ps.Reason = &r
			}
			list = append(list, ps)
		}
	}
	return apigen.AdminListPlugins200JSONResponse(apigen.AdminPluginList{Plugins: list}), nil
}

// AdminReloadPlugin hot-reloads one plugin: a new instance is launched, swapped in once ready,
// and the old one drained.
//
// A failed reload is deliberately NOT destructive. The loader retains the old instance and keeps
// serving from it, so the operator-visible outcome of "the new binary is broken" is 503 and an
// unchanged deployment — never a plugin that has disappeared because a reload was attempted.
func (s *Server) AdminReloadPlugin(ctx context.Context, request apigen.AdminReloadPluginRequestObject) (apigen.AdminReloadPluginResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if s.d.PluginReload == nil {
		return nil, apperr.ErrNotFound
	}
	if err := s.d.PluginReload(ctx, request.Name); err != nil {
		if errors.Is(err, plugin.ErrNoSuchPlugin) {
			return nil, apperr.ErrNotFound
		}
		// The plugin still exists and the old instance is still serving; the NEW one did not come
		// up. Say so, rather than reporting a success that did not happen.
		s.log().Warn("plugin reload failed; the previous instance is retained", "plugin", request.Name, "error", err.Error())
		return nil, &apperr.Unavailable{Detail: "the plugin did not come up; the previous instance is still serving"}
	}
	return apigen.AdminReloadPlugin204Response{}, nil
}
