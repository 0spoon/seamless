-- A fresh install starts the console at the BASIC level (config.Defaults()
-- .Console.Level): the fewest screens and knobs, with a welcome card on Home
-- that offers the other two levels. That default would otherwise hide most of the
-- console from an EXISTING installation whose owner has been using every screen
-- -- an upgrade must never take away a screen someone was using.
--
-- One-time grandfather clause: seed the console_level row to advanced iff this
-- database already recorded sessions (it has been in use) AND no row exists yet.
-- A fresh database runs the same statement over an empty sessions table and
-- seeds nothing, so new installs converge to basic. The row is stamped
-- source "seeded" so the console says "set by the upgrade" instead of crediting
-- the owner with a choice, and welcomed false so the Home welcome card appears
-- once to announce the picker. It is an ordinary stored override afterwards: the
-- console can change it or reset it, which means back to file/env.
--
-- The level is presentation only; nothing an agent receives reads this row.
--
-- The JSON value must match store.consoleLevelRow's json tags
-- (store.SetConsoleLevel writes the same shape).
INSERT INTO settings (key, value)
SELECT 'console_level', '{"level":"advanced","source":"seeded","welcomed":false}'
WHERE EXISTS (SELECT 1 FROM sessions)
  AND NOT EXISTS (SELECT 1 FROM settings WHERE key = 'console_level');
