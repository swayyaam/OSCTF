package main

import (
	"github.com/swayyaam/OSCTF/internal/config"
	"github.com/swayyaam/OSCTF/internal/fdbudget"
)

// resourceBudget is the file-descriptor apportionment the composition root derives at startup:
// the WebSocket connection cap and the plugin global in-flight cap, both claimed from ONE
// accountant so the DB/Docker/Redis/HTTP reserve is counted exactly once — not once per consumer.
type resourceBudget struct {
	acct         *fdbudget.Budget
	wsMaxConns   int
	pluginGlobal int
}

// deriveResourceBudget claims the process fd budget in priority order: plugin in-flight FIRST
// (essential, fixed — auth/scoring) so it is guaranteed its share, WebSocket connections SECOND
// (elastic, degradable to polling) so they absorb and are clamped to the remainder. fdSoft==0
// (Getrlimit failed / RLIM_INFINITY) yields an unbounded budget where both claims pass through.
func deriveResourceBudget(fdSoft uint64, cfg *config.Config) resourceBudget {
	acct := fdbudget.New(fdSoft)
	pluginGlobal, _ := acct.Claim("plugin-inflight", cfg.PluginMaxInflightTotal)
	wsMaxConns, _ := acct.Claim("websocket-conns", cfg.WSMaxConns)
	return resourceBudget{acct: acct, wsMaxConns: wsMaxConns, pluginGlobal: pluginGlobal}
}
