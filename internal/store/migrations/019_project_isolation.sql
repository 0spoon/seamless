-- Project isolation: a per-project fence against agent-to-agent knowledge
-- leakage. 'open' shares normally; 'confidential' fences outbound (sessions
-- bound elsewhere never read the project, and its sessions cannot write
-- outside it); 'sealed' adds the inbound fence (no global scope, no family,
-- no cross-project reads in either direction). The console is exempt:
-- isolation fences agents, not the owner.
ALTER TABLE projects ADD COLUMN isolation TEXT NOT NULL DEFAULT 'open';
