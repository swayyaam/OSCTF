package handlers

import (
	"context"

	"github.com/osctf/platform/internal/apigen"
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
