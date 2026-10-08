-- Which agent process owns an ambient session (agentproc.Anchor: "<pid>.<start>").
--
-- The SessionStart hook and the MCP connection of one Claude Code or Codex
-- session are separate channels that share nothing on the wire, so every MCP
-- connection used to start unbound and fall back to guessing its project from
-- whichever agents happened to be active -- which fails as soon as agents run in
-- two repos at once. Both channels are spawned by the same agent process,
-- though: the hook stamps that process here, the connection names it on every
-- request, and the daemon joins the two.
--
-- '' is "no process recorded" (a row from before this column, or a client too
-- old to send one); such a row is reached only through the old fallbacks.
ALTER TABLE sessions ADD COLUMN agent_process TEXT NOT NULL DEFAULT '';

-- "Which session does this process own, on this machine" is asked on every tool
-- call an agent makes -- for a live session first, then for one the idle reaper
-- expired while the agent was away -- so it gets its own partial index. The
-- status is left out of the predicate because both lookups use it; a process
-- owns a handful of rows at most.
CREATE INDEX idx_sessions_ambient_host_process
    ON sessions(host, agent_process)
 WHERE ambient = 1 AND agent_process <> '';
