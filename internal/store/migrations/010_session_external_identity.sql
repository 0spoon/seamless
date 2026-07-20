-- Ambient sessions are identified by the full external session id plus the
-- client that issued it. The readable sessions.name remains unique for display
-- and explicit name-based MCP operations, but hook lifecycle mutations no
-- longer derive identity from a truncated display name.
ALTER TABLE sessions ADD COLUMN external_client TEXT NOT NULL DEFAULT '';

-- Existing ambient rows already carry an unambiguous client prefix. Preserve
-- their historical names while backfilling the new identity discriminator so
-- the first post-upgrade hook resumes the row instead of forking it.
UPDATE sessions
   SET external_client = CASE
       WHEN ambient = 1 AND substr(name, 1, 3) = 'cc/' THEN 'claude-code'
       WHEN ambient = 1 AND substr(name, 1, 3) = 'cx/' THEN 'codex'
       ELSE ''
   END;

-- A named explicit session can be linked to an ambient through the historical
-- claude_session_id column. Backfill it only when the external id maps to one
-- client; an id shared by both clients is intentionally left unclassified
-- rather than guessed.
UPDATE sessions AS linked
   SET external_client = COALESCE((
       SELECT CASE
           WHEN MIN(ambient.external_client) = MAX(ambient.external_client)
               THEN MIN(ambient.external_client)
           ELSE ''
       END
         FROM sessions AS ambient
        WHERE ambient.ambient = 1
          AND ambient.claude_session_id = linked.claude_session_id
          AND ambient.external_client <> ''
   ), '')
 WHERE linked.ambient = 0
   AND linked.claude_session_id <> ''
   AND linked.external_client = '';

-- Application-created display names are deterministic from the full external
-- id, which closes the create race. This constraint also makes the authoritative
-- identity invariant explicit for imported or manually repaired rows.
CREATE UNIQUE INDEX idx_sessions_ambient_external_identity
    ON sessions(external_client, claude_session_id)
 WHERE ambient = 1
   AND external_client <> ''
   AND claude_session_id <> '';
